// Package daemon wires the opennavi runtime into a long-running
// process (#240, daemon MVP step 2): config → providers → thought
// queue → worker pool → agent-backed Runner, plus graceful shutdown.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/internal/ops/metrics"
	"github.com/flintwillow-software/opennavi/internal/portal"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/llm"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool/bridge"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	toolhttp "github.com/flintwillow-software/opennavi/tool/http"
	"github.com/flintwillow-software/opennavi/tool/note"
	"github.com/flintwillow-software/opennavi/tool/registry"
	"github.com/flintwillow-software/opennavi/tool/thoughts"
	timepkg "github.com/flintwillow-software/opennavi/tool/time"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

// Runtime is the assembled daemon: everything long-running needs.
type Runtime struct {
	Config    RuntimeParams
	Runtime   config.RuntimeConfig
	Queue     *thought.Queue
	Pool      *thought.Pool
	Store     *sqlite.Store
	Stateful  *sqlite.StatefulStore
	Registry  *registry.Registry
	SessionM  *session.SessionManager
	Provider  llm.Provider
	Scheduler *cron.Scheduler // started by Run; ticks cron schedules
	// StopReconcile halts the queue's store-reconcile loop (NAVI-336):
	// external-writer adoption — invoked by Run/RunForeground shutdown.
	// nil when the loop is disabled.
	StopReconcile func()
	// StopFlusher halts the queue's dirty-set flusher (NAVI-338
	// memory-first persistence): the final drain persists remaining
	// dirty thoughts. Invoked by Run/RunForeground shutdown after the
	// pool drain. nil in synchronous (legacy) mode.
	StopFlusher func()
	// StopFlusher halts the queue's dirty-set flusher (NAVI-338
	// memory-first persistence): the final drain persists remaining
	// dirty thoughts. Invoked by Run/RunForeground shutdown after the
	// pool drain. nil in synchronous (legacy) mode.
	Logger  *logging.Logger
	Metrics *metrics.Collector // queue metrics; nil when disabled

	// inflight resolves a running thought's in-flight agent for the
	// steering bridge (running path; turn-boundary
	// injection). Registered by the runner per run; the queue's
	// SetSteeringRouter closure delegates here. Test seam: read via
	// steeringSnapshot to verify injection.
	inflight *inFlightAgents
	// Portal is the control + observation service. It is always
	// assembled so in-process front-ends can use it; the gateway is
	// mounted only when PortalAddr is set.
	Portal      *portal.Hub
	Observation pubportal.Observer
}

// RuntimeParams carries the command-layer inputs (flags already
// merged through the #237 chain's flags layer by the caller). Under
// the go-first authoring split there is no YAML definition file:
// definition-level concerns are compiled into the navi binary, and the
// daemon consumes only runtime config.
type RuntimeParams struct {
	RuntimeFile string // runtime config YAML ("" = none)
	SessionDB   string // final session DB path (post-merge)
	Model       string // CLI model override (flags layer)
	OllamaHost  string // CLI host override (flags layer)
	// LogFormat/LogLevel are CLI log-knob overrides (NAVI-328, flags
	// layer; empty = unset so the logging config section wins).
	LogFormat        string
	LogLevel         string
	CompactBudget    int             // compaction budget (0 = off)
	WorkDir          string          // working directory for file/shell tools
	SystemPrompt     string          // CLI system prompt override
	Workers          int             // pool worker count (0 = 2)
	Logger           *logging.Logger // structured logger (nil = default stderr/info)
	OverrideProvider llm.Provider    // test seam: replaces the config-built chain
	// MetricsAddr serves the Prometheus exposition on "host:port";
	// empty (the default) disables the metrics endpoint entirely —
	// zero cost when unset.
	MetricsAddr string
	// PortalAddr serves the portal observation + control attachment
	// on "host:port" — the metrics-addr pattern. Empty
	// (the default) disables the portal serve path entirely.
	PortalAddr string
	// OverrideAuthorizer is a test seam replacing the fail-closed nil
	// authorizer: production wires nil, so every portal
	// attachment is refused until IAM lands.
	OverrideAuthorizer portal.PortalAuthorizer
	// NaviID is the per-navi scoping key for stateful tool storage
	// todos/notes are keyed (navi_id, id), so two
	// navis on one host sharing a DB never see each other's state.
	NaviID string
	// Foreground marks an interactive session attached to the binary
	// run: the session's thought occupies a reserved
	// foreground slot (Config.ForegroundSlots >= 1) and the run exits
	// when the session ends and the queue drains (RunForeground). The
	// runtime assembles IDENTICALLY in every mode (one assembly,
	// NAVI-325: connectors — cron, metrics, portal — always load);
	// the foreground slot is reserved only when a session (TUI) is
	// attached — headless `--daemon` mode has no foreground thought.
	Foreground bool
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
	// --ollama-host is a flags-layer override (NAVI-325): the endpoint
	// comes from the flag (top layer) over env/YAML. MergeRuntime's env
	// fill is gated on the flag value being empty, so the precedence is
	// flag > env > YAML by construction.
	if params.OllamaHost != "" {
		flags.Provider.Endpoint = params.OllamaHost
	}
	// Log-format knob (NAVI-328): --log-format/--log-level are flags-
	// layer overrides — the config surface turns the same knobs (flag
	// wins on conflict, per the merge chain); empty = unset, so the
	// config section wins when the flag is absent.
	if params.LogFormat != "" {
		if flags.Logging == nil {
			flags.Logging = &config.LoggingConfig{}
		}
		flags.Logging.Format = params.LogFormat
	}
	if params.LogLevel != "" {
		if flags.Logging == nil {
			flags.Logging = &config.LoggingConfig{}
		}
		flags.Logging.Level = params.LogLevel
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

	// Stateful tool storage: todos + notes tables over
	// the shared DB, scoped per navi. Opened here so stateful tools
	// (todo_*/note_*) can register against it and the health view can
	// read todo state.
	stateful, err := sqlite.NewStatefulStore(store.DB())
	if err != nil {
		store.Close()
		return nil, err
	}

	// Tool registry: builtins register with full metadata;
	// catalog/author/stateful tools join here as they land. The active
	// set is composed from the runtime ToolPolicy — the LLM-facing
	// surface is registry-first, and tool/bridge binds it into the
	// agent executor per thought run.
	reg := registry.New()
	if err := builtin.Register(reg, params.WorkDir); err != nil {
		store.Close()
		return nil, err
	}
	// Stateful catalog tools: the todo tools register for
	// this navi's identity; note/time/http tools join as they land.
	// Empty NaviID falls back to the shared default scope.
	naviID := params.NaviID
	if naviID == "" {
		naviID = "default"
	}
	if err := todo.RegisterTools(reg, naviID, stateful); err != nil {
		store.Close()
		return nil, err
	}
	if err := note.Register(reg, naviID, stateful, noteCaps(rc.ToolCaps)...); err != nil {
		store.Close()
		return nil, err
	}
	// Stateless catalog tools: time_read needs no store
	// handle.
	if err := timepkg.RegisterTools(reg); err != nil {
		store.Close()
		return nil, err
	}
	// http_request: stateless egress tool; the SSRF allow/deny lists
	// and response cap come from tool_caps config (deny still wins in
	// the tool). Auth stays nil — credential resolution lands
	// separately, so authRef calls fail closed.
	if err := toolhttp.Register(reg, httpEgress(rc.ToolCaps)...); err != nil {
		store.Close()
		return nil, err
	}
	bindOpts := bindOptions(rc.Tools)
	// Active set is composed ONCE, here, after all registrations (the
	// thoughts tools need the queue created first).

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
	// Foreground reservation (, NAVI-320): a binary run
	// reserves one slot for the interactive session's thought. The
	// reservation is carved from the pool (disjoint from priority
	// slots), so a binary run needs at least two slots of capacity —
	// bump MaxConcurrent when the config is too tight (an operator
	// setting of 1 cannot express a reservation; the interactive
	// session would otherwise starve all daemon-style work).
	if params.Foreground {
		if defCfg.MaxConcurrent < 2 {
			defCfg.MaxConcurrent = 2
		}
		if defCfg.PrioritySlots+1 > defCfg.MaxConcurrent {
			defCfg.PrioritySlots = defCfg.MaxConcurrent - 1
		}
		defCfg.ForegroundSlots = 1
	}
	q, err := thought.NewQueue(defCfg, thoughtStore)
	if err != nil {
		store.Close()
		return nil, err
	}
	// Thought-management tools (, NAVI-323): the navi manages
	// its own queue through the catalog mechanism — queue-attached via
	// thought.RegisterQueueTools, bound to THIS runtime's queue. The
	// #287 rails apply at Enqueue (origin:tool tag, self-scheduled
	// priority clamp); no new config surface (clamp band default).
	// The active set is recomputed after registration so the tools
	// surface only when active + permitted (same governance as any
	// catalog tool).
	if err := thoughts.Register(reg, q); err != nil {
		store.Close()
		return nil, err
	}
	if err := reg.SetActive(activeNames(reg.AllNames(), rc.Tools)); err != nil {
		store.Close()
		return nil, err
	}

	// Observation fanout (NAVI-352): the public bounded broker merges
	// queue lifecycle and running-agent events; the portal hub and any
	// in-process front-end subscribe through it.
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })

	// Portal control service: assembled unconditionally and
	// bound from Deps, so an in-process front-end can observe without the
	// gateway. It is served on PortalAddr when set. No authorizer is
	// wired in production (OverrideAuthorizer is a test seam): IAM lands
	// later, so attachments and control fail closed until then
	// (fail-closed stub).
	portalPlane := portal.NewHub(params.OverrideAuthorizer)
	if err := portalPlane.Start(context.Background(), pubportal.Deps{
		ThoughtSystem: q,
		Observation:   broker,
	}); err != nil {
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
	// Log-format knob application (NAVI-328): the merged logging
	// section turns the same knobs as the flags — applied to the
	// runtime's logger (SetFormat/SetLevel; empty = unset, so the
	// cmd-layer TTY default survives when nothing is configured).
	if rc.Logging != nil {
		if rc.Logging.Format != "" {
			logger.SetFormat(logging.ParseFormat(rc.Logging.Format))
		}
		if rc.Logging.Level != "" {
			logger.SetLevel(logging.ParseLevel(rc.Logging.Level))
		}
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

	// In-flight agent tracker: the steering bridge resolves a running
	// thought's agent for turn-boundary message injection
	// (running path). The runner registers each agent on run
	// start; the router closure runs outside q.mu (RouteMessage
	// guarantee) so snapshot reads are safe.
	tracker := newInFlightAgents()
	// Per-thought agent events (NAVI-352): the runtime publishes
	// them onto the observation broker, where any subscriber sees them.
	agentEvents := func(thoughtID string, ev agent.AgentEvent) {
		broker.Publish(pubportal.Event{
			Category:  pubportal.CategoryAgent,
			Kind:      string(ev.Type),
			ThoughtID: thoughtID,
			Detail:    agentDetail(ev),
			At:        time.Now().UTC(),
		})
	}
	runner := NewAgentRunner(agentRunnerDeps{
		chain:          chain,
		sessionM:       sessionM,
		model:          rc.Provider.Model,
		systemPrompt:   params.SystemPrompt,
		workDir:        params.WorkDir,
		compactBudget:  params.CompactBudget,
		registry:       reg,
		bind:           bindOpts,
		logger:         logger,
		tracker:        tracker,
		agentEvents:    agentEvents,
		providerConfig: rc.Provider,
	})
	// Steering bridge: binary runs route messages to
	// the in-flight agent's steering queue at turn boundaries. Daemon
	// mode works too (messages may arrive for any running thought) —
	// the bridge is harmless when no route targets a running thought.
	q.SetSteeringRouter(tracker.steer)
	WireLogging(logger, q, sched)

	// Metrics (NAVI-258): subscribe the collector to the queue's event
	// fanout — seeded from a one-time snapshot so recovered thoughts
	// are visible immediately. Disabled when MetricsAddr is unset.
	var collector *metrics.Collector
	if params.MetricsAddr != "" {
		collector = metrics.New()
		collector.WireQueue(q)
	}

	workers := params.Workers
	if workers <= 0 {
		workers = 2
	}
	pool := thought.NewPool(q, runner, workers)

	return &Runtime{
		Config:        params,
		Runtime:       rc,
		Queue:         q,
		Pool:          pool,
		Scheduler:     sched,
		Store:         store,
		Stateful:      stateful,
		Registry:      reg,
		SessionM:      sessionM,
		Provider:      chain,
		Logger:        logger,
		Metrics:       collector,
		inflight:      tracker,
		StopReconcile: q.StartReconcile(), // NAVI-336: adopt external writers' rows
		StopFlusher:   q.StartFlusher(),   // NAVI-338: drain the dirty set
		Portal:        portalPlane,
		Observation:   broker,
	}, nil
}

// agentDetail summarizes an agent event for the observation stream
// (bounded length; never raw credentials). NAVI-352 moved the neutral
// event type to the public contract; the agent-specific summary stays
// here, where the agent package is already in scope.
func agentDetail(ev agent.AgentEvent) string {
	if ev.Tool != nil {
		snippet := string(ev.Tool.Input)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		return strings.TrimSpace(ev.Tool.Name + " " + snippet)
	}
	if ev.Message != nil {
		c := strings.TrimSpace(ev.Message.Content)
		if len(c) > 256 {
			c = c[:256] + "…"
		}
		return c
	}
	if ev.Error != nil {
		return ev.Error.Error()
	}
	return ""
}

// bindOptions maps the runtime ToolPolicy onto the bridge's bind-time
// policy: allowed/blocked name lists (deny wins) and per-tool timeout
// overrides (internal/config ToolPolicy ↔ tool/bridge Policy).
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

// noteCaps maps tool_caps.notes config onto note registration options
// (zero fields keep the package defaults).
func noteCaps(tc *config.ToolCapsConfig) []note.Options {
	if tc == nil || tc.Notes == nil {
		return nil
	}
	return []note.Options{{
		MaxBodyBytes: tc.Notes.MaxBodyBytes,
		MaxNaviBytes: tc.Notes.MaxNaviBytes,
	}}
}

// httpEgress maps tool_caps.http config onto http registration options
// (allow/deny lists, response cap). The auth resolver stays nil until
// lands — authRef calls fail closed.
func httpEgress(tc *config.ToolCapsConfig) []toolhttp.Options {
	if tc == nil || tc.HTTP == nil {
		return nil
	}
	return []toolhttp.Options{{
		MaxResponseBytes: tc.HTTP.MaxResponseBytes,
		Allowlist:        tc.HTTP.Allowlist,
		Denylist:         tc.HTTP.Denylist,
	}}
}

// activeNames composes the default active set from the registered
// tools and the runtime ToolPolicy: blocked tools are excluded (deny
// wins), and a non-empty Allowed list restricts the set to those
// names. Remaining access-level enforcement lands with IAM.
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
	// One assembly (NAVI-325): connectors start identically in every
	// run mode — RunForeground uses the same starters. Shutdown
	// ordering preserved: servers first (stop closure), then
	// scheduler stop, then the pool drain by the caller below.
	stopConnectors := d.startConnectors()

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
	stopConnectors()
	d.Pool.StopAndWait()
	if d.StopReconcile != nil {
		d.StopReconcile()
	}
	if d.StopFlusher != nil {
		d.StopFlusher() // final dirty drain
	}
	return nil
}

// startConnectors starts the shared connector set every run mode
// serves (NAVI-325 one assembly): the scheduler, the metrics
// exposition, and the portal attachment. Scrape/attach failures never
// take the run down: listener errors are logged, not propagated (a
// headless run without a port free still processes work).
//
// Returns the connector-stop closure; the caller invokes it at
// shutdown — servers close first, then the scheduler stop, then the
// caller's pool drain (the #280/#290 shutdown semantics preserved).
// The closures run at STOP time, not at start-return (defers inside a
// starter would close the servers before the run began).
func (d *Runtime) startConnectors() (stopConnectors func()) {
	// Start the scheduler (begins ticking against the queue).
	if d.Scheduler != nil {
		d.Scheduler.Start()
	}

	var shutdowns []func()
	stopConnectors = func() {
		for _, fn := range shutdowns {
			fn()
		}
		// Connector shutdown step: the scheduler stops ticking. Pool
		// drain remains the caller's (StopAndWait is the park path,
		// ordered against the caller's own session teardown).
		if d.Scheduler != nil {
			d.Scheduler.Stop()
		}
	}

	// Serve the Prometheus exposition when metrics are enabled
	// (Runtime.Metrics non-nil iff MetricsAddr was set).
	if d.Metrics != nil {
		addr := d.Config.MetricsAddr
		metricsSrv := &http.Server{
			Addr:              addr,
			Handler:           d.Metrics.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			d.Logger.Error("metrics listener failed", map[string]any{"addr": addr, "error": err.Error()})
		} else {
			d.Logger.Info("metrics endpoint serving", map[string]any{"addr": addr, "path": "/metrics"})
			go func() {
				if serr := metricsSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
					d.Logger.Error("metrics server error", map[string]any{"error": serr.Error()})
				}
			}()
			shutdowns = append(shutdowns, func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = metricsSrv.Shutdown(shutdownCtx)
			})
		}
	}

	// Serve the portal attachment when PortalAddr is set
	// (Runtime.Portal non-nil iff PortalAddr was set): same listener
	// lifecycle and graceful shutdown ordering as the metrics server.
	// The serve shim (portal.go) is a loopback-grade JSON mapping;
	// the real wire protocol is deferred. Control and attach fail
	// closed until IAM lands (no authorizer wired).
	if d.Config.PortalAddr != "" {
		addr := d.Config.PortalAddr
		portalSrv := &http.Server{
			Addr:              addr,
			Handler:           PortalHandler(d),
			ReadHeaderTimeout: 5 * time.Second,
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			d.Logger.Error("portal listener failed", map[string]any{"addr": addr, "error": err.Error()})
		} else {
			d.Logger.Info("portal attachment serving", map[string]any{"addr": addr, "path": "/portal/events"})
			go func() {
				if serr := portalSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
					d.Logger.Error("portal server error", map[string]any{"error": serr.Error()})
				}
			}()
			shutdowns = append(shutdowns, func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = portalSrv.Shutdown(shutdownCtx)
			})
		}
	}

	return stopConnectors
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
