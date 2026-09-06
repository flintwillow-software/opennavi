// Package daemon wires the opennavi runtime into a long-running
// process (#240, daemon MVP step 2): config → providers → thought
// queue → worker pool → agent-backed Runner, plus graceful shutdown.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// Runtime is the assembled daemon: everything long-running needs.
type Runtime struct {
	Config    RuntimeParams
	Def       *config.NaviDefinition
	Runtime   config.RuntimeConfig
	Queue     *thought.ThoughtQueue
	Pool      *thought.Pool
	Store     *sqlite.Store
	SessionM  *session.SessionManager
	Provider  llm.Provider
	Scheduler *cron.Scheduler // started by Run; ticks cron schedules
}

// RuntimeParams carries the command-layer inputs (flags already
// merged through the #237 chain's flags layer by the caller).
type RuntimeParams struct {
	DefinitionPath string // navi.yaml (empty = minimal built-in definition)
	RuntimeFile    string // runtime config YAML ("" = none)
	SessionDB      string // final session DB path (post-merge)
	Model          string // CLI model override (flags layer)
	OllamaHost     string // CLI host override (flags layer)
	CompactBudget  int    // compaction budget (0 = off)
	SystemPrompt   string // CLI system prompt override
	Workers        int    // pool worker count (0 = 2)
}

// Assemble runs the startup chain: definition → runtime merge →
// store → provider registry (ollama/openrouter/mock) → queue → pool.
func Assemble(params RuntimeParams) (*Runtime, error) {
	var def *config.NaviDefinition
	if params.DefinitionPath != "" {
		loaded, err := config.LoadDefinition(params.DefinitionPath)
		if err != nil {
			return nil, err
		}
		def = loaded
	} else {
		def = &config.NaviDefinition{Name: "navi", Version: "0.1.0"}
	}

	// Runtime merge chain (defaults → file → env → flags): the flags
	// layer is pre-merged by the caller into RuntimeParams fields.
	fileRC := config.RuntimeConfig{}
	if params.RuntimeFile != "" {
		var err error
		fileRC, err = config.LoadRuntimeFile(params.RuntimeFile)
		if err != nil {
			return nil, err
		}
	}
	flags := config.RuntimeConfig{}
	if params.Model != "" {
		flags.Provider.Model = params.Model
	}
	if params.SessionDB != "" {
		flags.SessionDB = params.SessionDB
	}
	rc := config.MergeRuntime(fileRC, flags)

	// Validate tool policy against the compiled definition.
	if err := rc.Validate(def); err != nil {
		return nil, err
	}

	// Store layer (sessions + thoughts + memory share this DB).
	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return nil, err
	}
	store, err := sqlite.Open(rc.SessionDB, sqlite.Options{Cipher: cipher})
	if err != nil {
		return nil, err
	}

	// Providers: primary from config; fallback chain per #189 when a
	// fallback provider is configured.
	primary, err := buildProvider(rc.Provider)
	if err != nil {
		store.Close()
		return nil, err
	}

	var chain llm.Provider = primary
	if rc.Provider.Fallback != "" {
		fb, err := buildProviderFor(rc.Provider.Fallback, rc.Provider)
		if err != nil {
			store.Close()
			return nil, err
		}
		chain, err = llm.NewProviderFallback(primary, fb, retryFromConfig(rc.Provider))
		if err != nil {
			store.Close()
			return nil, err
		}
	}

	// Queue over the shared store.
	thoughtStore, err := sqlite.NewThoughtStore(store.DB())
	if err != nil {
		store.Close()
		return nil, err
	}
	defCfg := thought.Config{
		MaxConcurrent:  thoughtQueueMax(def),
		PrioritySlots:  thoughtPrioritySlots(def),
		SlotExhaustion: thought.PolicyQueue,
	}
	q, err := thought.NewQueue(defCfg, thoughtStore)
	if err != nil {
		store.Close()
		return nil, err
	}

	// Cron scheduler wiring (#242 seam): MigrateState, scheduler with
	// definition-derived schedule defs, RecordFire wiring (queue started
	// events advance last_fired/fire_count), and MissedTick for
	// schedules whose next_due passed while down.
	if err := cron.MigrateState(store.DB()); err != nil {
		store.Close()
		return nil, err
	}
	state := cron.NewStateStore(store.DB())
	sched := cron.NewScheduler(q)
	sched.OnEvent(func(ev thought.QueueEvent) {
		if ev.Kind == thought.EventStarted {
			_ = state.RecordFire(ev.ThoughtID, time.Now().UTC())
		}
	})
	for _, sd := range def.Cron.ToSchedulerDefs() {
		if err := sched.Add(sd); err != nil {
			store.Close()
			return nil, err
		}
	}
	// MissedTick: schedules whose next_due passed while down enqueue
	// one coalescing thought at startup (crash-recovery coalescing,
	// spec section 3.5).
	for _, sd := range def.Cron.ToSchedulerDefs() {
		last, lerr := state.LastFired(sd.Name)
		if lerr != nil {
			store.Close()
			return nil, lerr
		}
		if last.IsZero() {
			continue
		}
		if sd.Expr != "" {
			if ce, perr := cron.Parse(sd.Expr); perr == nil {
				next := ce.Next(last)
				if next.Before(time.Now()) {
					if merr := sched.MissedTick(sd.Name); merr != nil {
						store.Close()
						return nil, merr
					}
				}
			}
		}
	}
	sessionM := session.NewSessionManager(store)
	runner := NewAgentRunner(agentRunnerDeps{
		chain:         chain,
		sessionM:      sessionM,
		model:         rc.Provider.Model,
		systemPrompt:  params.SystemPrompt,
		compactBudget: params.CompactBudget,
	})
	workers := params.Workers
	if workers <= 0 {
		workers = 2
	}
	pool := thought.NewPool(q, runner, workers)

	return &Runtime{
		Config:    params,
		Def:       def,
		Runtime:   rc,
		Queue:     q,
		Pool:      pool,
		Scheduler: sched,
		Store:     store,
		SessionM:  sessionM,
		Provider:  chain,
	}, nil
}

func thoughtQueueMax(def *config.NaviDefinition) int {
	if def.ThoughtQueue != nil && def.ThoughtQueue.MaxConcurrent > 0 {
		return def.ThoughtQueue.MaxConcurrent
	}
	return 1
}

func thoughtPrioritySlots(def *config.NaviDefinition) int {
	if def.ThoughtQueue != nil {
		return def.ThoughtQueue.PrioritySlots
	}
	return 0
}

// buildProvider constructs the primary provider from config.
func buildProvider(pc config.ProviderConfig) (llm.Provider, error) {
	return buildProviderFor(pc.Primary, pc)
}

// buildProviderFor constructs a named provider (primary or fallback).
func buildProviderFor(name string, pc config.ProviderConfig) (llm.Provider, error) {
	switch strings.ToLower(name) {
	case "ollama":
		host := pc.Endpoint
		if host == "" {
			host = llm.OllamaHostFromEnv()
		}
		p := llm.NewOllamaProviderAt(host)
		p.Model = pc.Model
		return p, nil
	case "openrouter":
		return llm.NewOpenRouterProviderWithClient(llm.DefaultOpenRouterURL, pc.APIKey, http.DefaultClient), nil
	case "mock":
		return llm.NewMockProvider(), nil
	default:
		return nil, fmt.Errorf("daemon: unknown provider %q", name)
	}
}

// retryFromConfig maps runtime retry settings onto llm.RetryConfig.
func retryFromConfig(pc config.ProviderConfig) *llm.RetryConfig {
	if pc.MaxRetries == 0 && pc.InitialDelay == 0 {
		return nil // defaults
	}
	return &llm.RetryConfig{
		MaxRetries:    pc.MaxRetries,
		InitialDelay:  pc.InitialDelay,
		MaxDelay:      pc.MaxDelay,
		BackoffFactor: pc.BackoffFactor,
		JitterEnabled: true,
	}
}

// marshalState encodes agent state messages for the checkpoint blob.
func marshalState(messages []agent.AgentMessage) ([]byte, error) {
	return json.Marshal(messages)
}

// unmarshalState decodes a checkpoint blob back into messages.
func unmarshalState(blob []byte) ([]agent.AgentMessage, error) {
	var msgs []agent.AgentMessage
	if err := json.Unmarshal(blob, &msgs); err != nil {
		return nil, fmt.Errorf("daemon: decode checkpoint: %w", err)
	}
	return msgs, nil
}

// Run blocks serving the daemon until ctx is done (SIGINT/SIGTERM via
// signal.NotifyContext in the command layer).
//
// Shutdown semantics (documented per review): the scheduler stops
// first, then the pool's StopAndWait drains in-flight thoughts — a
// thought still running at drain time finishes its agent run (or is
// parked-with-checkpoint on cancellation). No thought is killed-and-
// lost; park-with-checkpoint is the spec-correct terminal path.
func (d *Runtime) Run(ctx context.Context) error {
	// Cron scheduler first: it stops accepting new ticks immediately.
	if d.Scheduler != nil {
		d.Scheduler.Stop()
	}

	// Pool drains in-flight thoughts.
	d.Pool.StopAndWait()

	// Session/store flush: SQLite commits are synchronous (nothing
	// buffered), so closing the store is the final flush.
	return d.Store.Close()
}

// Shutdown is the graceful path used by signal handling: stops the
// scheduler and drains the pool, leaving the store open for final
// reads. Call Store.Close() after any last reads.
func (d *Runtime) Shutdown() {
	if d.Scheduler != nil {
		d.Scheduler.Stop()
	}
	d.Pool.StopAndWait()
}
