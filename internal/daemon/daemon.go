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

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/tools/bridge"
	"github.com/flintwillow-software/opennavi/tools/builtin"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// Runtime is the assembled daemon: everything long-running needs.
type Runtime struct {
	Config    RuntimeParams
	Runtime   config.RuntimeConfig
	Queue     *thought.ThoughtQueue
	Pool      *thought.Pool
	Store     *sqlite.Store
	Stateful  *sqlite.StatefulStore
	Registry  *registry.Registry
	SessionM  *session.SessionManager
	Provider  llm.Provider
	Scheduler *cron.Scheduler // started by Run; ticks cron schedules
	Logger    *logging.Logger
}

// RuntimeParams carries the command-layer inputs (flags already
// merged through the #237 chain's flags layer by the caller). Under
// the go-first authoring split (S000200 §04, S000207) there is no
// navi.yaml: definition-level concerns are compiled into the navi
// binary, and the daemon consumes only runtime config.
type RuntimeParams struct {
	RuntimeFile      string          // runtime config YAML ("" = none)
	SessionDB        string          // final session DB path (post-merge)
	Model            string          // CLI model override (flags layer)
	OllamaHost       string          // CLI host override (flags layer)
	CompactBudget    int             // compaction budget (0 = off)
	WorkDir          string          // working directory for file/shell tools
	SystemPrompt     string          // CLI system prompt override
	Workers          int             // pool worker count (0 = 2)
	Logger           *logging.Logger // structured logger (nil = default stderr/info)
	OverrideProvider llm.Provider    // test seam: replaces the config-built chain
	// NaviID is the per-navi scoping key for stateful tool storage
	// (S130500 §07): todos/notes are keyed (navi_id, id), so two
	// navis on one host sharing a DB never see each other's state.
	NaviID string
}

// Assemble runs the startup chain: runtime merge → store → provider
// registry (ollama/openrouter/mock) → queue → pool.
func Assemble(params RuntimeParams) (*Runtime, error) {
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

	// Validate the runtime config's self-contained invariants (queue
	// bounds, cron schedules). The compiled navi definition is not
	// visible here by design; the navi binary enforces its own
	// compiled-in tool policy.
	if err := rc.Validate(); err != nil {
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

	// Stateful tool storage (S130500 §07): todos + notes tables over
	// the shared DB, scoped per navi. Opened here so stateful tools
	// (todo_*/note_*) can register against it and the health view can
	// read todo state.
	stateful, err := sqlite.NewStatefulStore(store.DB())
	if err != nil {
		store.Close()
		return nil, err
	}

	// Tool registry (S130400): builtins register with full metadata;
	// catalog/author/stateful tools join here as they land. The active
	// set is composed from the runtime ToolPolicy — the LLM-facing
	// surface is registry-first, and tools/bridge binds it into the
	// agent executor per thought run.
	reg := registry.New()
	if err := builtin.Register(reg, params.WorkDir); err != nil {
		store.Close()
		return nil, err
	}
	bindOpts := bindOptions(rc.Tools)
	if err := reg.SetActive(activeNames(reg.AllNames(), rc.Tools)); err != nil {
		store.Close()
		return nil, err
	}

	// Providers: primary from config; fallback chain per #189 when a
	// fallback provider is configured.
	primary, err := buildProvider(rc.Provider)
	if err != nil {
		store.Close()
		return nil, err
	}

	// Test seam: an injected provider replaces the config-built chain.
	var chain llm.Provider
	if params.OverrideProvider != nil {
		chain = params.OverrideProvider
	} else {
		chain = primary
	}
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
		MaxConcurrent:  thoughtQueueMax(rc),
		PrioritySlots:  thoughtPrioritySlots(rc),
		SlotExhaustion: thought.PolicyQueue,
	}
	q, err := thought.NewQueue(defCfg, thoughtStore)
	if err != nil {
		store.Close()
		return nil, err
	}

	// Test seam: an injected logger replaces the env-built one.
	var logger *logging.Logger
	if params.Logger != nil {
		logger = params.Logger
	} else {
		logger = logging.WireFromEnv()
	}

	sched, cerr := WireCron(store.DB(), rc.Cron, q)
	if cerr != nil {
		store.Close()
		return nil, cerr
	}
	sched.OnDebug(func(msg string, fields map[string]any) {
		logger.Debug(msg, fields)
	})
	sessionM := session.NewSessionManager(store)
	runner := NewAgentRunner(agentRunnerDeps{
		chain:         chain,
		sessionM:      sessionM,
		model:         rc.Provider.Model,
		systemPrompt:  params.SystemPrompt,
		workDir:       params.WorkDir,
		compactBudget: params.CompactBudget,
		registry:      reg,
		bind:          bindOpts,
		logger:        logger,
	})
	WireLogging(logger, q, sched)
	workers := params.Workers
	if workers <= 0 {
		workers = 2
	}
	pool := thought.NewPool(q, runner, workers)

	return &Runtime{
		Config:    params,
		Runtime:   rc,
		Queue:     q,
		Pool:      pool,
		Scheduler: sched,
		Store:     store,
		Stateful:  stateful,
		Registry:  reg,
		SessionM:  sessionM,
		Provider:  chain,
		Logger:    logger,
	}, nil
}

// bindOptions maps the runtime ToolPolicy onto the bridge's bind-time
// policy: allowed/blocked name lists (deny wins) and per-tool timeout
// overrides (internal/config ToolPolicy ↔ tools/bridge Policy).
func bindOptions(tp config.ToolPolicy) bridge.Options {
	timeouts := make(map[string]time.Duration, len(tp.Timeout))
	for name, d := range tp.Timeout {
		timeouts[name] = d
	}
	return bridge.Options{
		Policy: bridge.Policy{
			Allowed:  tp.Allowed,
			Blocked:  tp.Blocked,
			Timeouts: timeouts,
		},
	}
}

// activeNames composes the default active set from the registered
// tools and the runtime ToolPolicy: blocked tools are excluded (deny
// wins), and a non-empty Allowed list restricts the set to those
// names. Remaining access-level enforcement (S040200) lands with IAM.
func activeNames(names []string, tp config.ToolPolicy) []string {
	blocked := make(map[string]bool, len(tp.Blocked))
	for _, name := range tp.Blocked {
		blocked[name] = true
	}
	allowed := make(map[string]bool, len(tp.Allowed))
	for _, name := range tp.Allowed {
		allowed[name] = true
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if blocked[name] {
			continue
		}
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

func thoughtQueueMax(rc config.RuntimeConfig) int {
	if rc.ThoughtQueue != nil && rc.ThoughtQueue.MaxConcurrent > 0 {
		return rc.ThoughtQueue.MaxConcurrent
	}
	return 1
}

func thoughtPrioritySlots(rc config.RuntimeConfig) int {
	if rc.ThoughtQueue != nil {
		return rc.ThoughtQueue.PrioritySlots
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
	// Start the scheduler (begins ticking against the queue).
	if d.Scheduler != nil {
		d.Scheduler.Start()
	}

	// Start the worker pool — without this the daemon never claims
	// thoughts (found during live E2E validation: #247).
	if d.Pool != nil {
		d.Pool.Start()
	}

	// Block until ctx is done (signal.NotifyContext in the command),
	// then shut down gracefully.
	<-ctx.Done()

	// Graceful: scheduler stops ticking, pool drains in-flight thoughts
	// (park-with-checkpoint is the terminal path for interrupted runs).
	// Store close is the caller's responsibility (Runtime.Store.Close()).
	if d.Scheduler != nil {
		d.Scheduler.Stop()
	}
	d.Pool.StopAndWait()
	return nil
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
