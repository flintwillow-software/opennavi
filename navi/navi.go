// Package navi is the composition root (ADR 0002): the object a navi
// author constructs, appends to, and runs. It owns typed collections
// populated at build time, and it freezes them when serving begins.
//
//	n := navi.New("oncall-bot", navi.WithVersion("1.0.0"))
//	n.Skills.Add(skills.IncidentResponse)
//	n.Tools.Add(tools.TodoRead(store))
//	n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"})
//	n.Connectors.Add(connectors.Cron())
//	n.Portals.Add(portals.TUI())
//	if err := n.Run(ctx); err != nil { log.Fatal(err) }
//
// Nothing registers through init() or a global: every component is an
// explicit value appended to a collection, so a navi's whole capability
// surface is visible in its main.go.
package navi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/memory"
	"github.com/flintwillow-software/opennavi/internal/provider"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// ErrNoName is returned by Run for a navi declared without a name.
var ErrNoName = errors.New("navi requires a name")

// Navi is a composed navi: a name, its metadata, and its typed
// collections. Declare, then Run.
type Navi struct {
	name            string
	version         string
	personality     string
	defaultProvider string

	// Collections. Each is populated during declaration and frozen by
	// Run.
	Skills     *Collection[skill.Skill]
	Tools      *Collection[tool.Tool]
	Models     *Collection[model.Provider]
	Connectors *Collection[connector.Connector]
	Portals    *Collection[portal.Portal]
	Modes      *Collection[Mode]

	// Datastores is the compiled candidate set of persistence backends
	// (ADR 0011). The active one is selected from runtime config at
	// assembly; consumers take per-aggregate views from its handle.
	Datastores *DatastoreCollection

	// Permissions is the compiled permission ceiling (ADR 0006): what
	// this binary is capable of at all. Modes may narrow it and can
	// never widen it.
	Permissions PermissionPolicy

	// Thoughts is the thought system. It is core, not added (ADR 0003).
	Thoughts *thought.Queue

	// observation is the bounded observation fanout (NAVI-352): queue
	// lifecycle events and running-agent events merge here and reach
	// portals through portal.Deps.
	observation *portal.Broker

	// thoughtStore / queueConfig configure the thought system; the
	// defaults (memory store, off reconcile/flush) suit tests, while a
	// durable navi supplies a sqlite store and enables both.
	thoughtStore thought.Store
	queueConfig  *thought.Config

	// datastoreFlushInterval tunes the datastore's RAM-first flush cadence
	// (WithDatastoreFlushInterval).
	datastoreFlushInterval time.Duration

	// shortTermMemory records the WithShortTermMemory declaration.
	shortTermMemory bool
	// shortMemory is the always-loaded short-term memory tier, built at
	// assembly when declared.
	shortMemory *memory.Service

	// skillIndex is the compiled skill set (authored + generated core),
	// built at assembly; skillOrder preserves declaration order.
	skillIndex map[string]skill.Skill
	skillOrder []string

	// inflight is the steering bridge's lookup: maps a running
	// thought to its agent so RouteMessage can inject at the turn boundary.
	inflight *inFlightAgents

	// fallback is the provider fallback chain for thoughts that name no
	// provider (ADR 0009: fallback stays at the provider-config layer).
	fallback     llm.Provider
	fallbackName string

	log *slog.Logger

	// runtimeFile is the runtime config path (WithRuntimeConfig).
	runtimeFile string

	// Resolved by assemble, before serving starts.
	selection DefaultSelection
	providers *provider.Set
	// registry is the runtime tool registry built from the declared Tools
	// collection (built once, at assemble).
	registry *registry.Registry
	// transcripts is the thought/transcript view the runner persists
	// through. It is resolved from the active datastore at assembly and is
	// nil when no datastore is declared (history is then not persisted).
	transcripts datastore.Thoughts
	// override replaces provider resolution entirely (test/embedding seam).
	override llm.Provider

	// Execution.
	runner   Runner
	workers  int
	dispatch *dispatch

	mu      sync.Mutex
	serving bool
	initErr error
}

// Option adjusts a navi during New. Options are applied before any
// collection is populated, so they configure defaults rather than items.
type Option func(*Navi)

// WithThoughtStore overrides where the queue persists thoughts. The
// default adapts the datastore's ordered writer (NAVI-374), so thoughts
// persist through the active backend; an explicit store bypasses that.
func WithThoughtStore(s thought.Store) Option {
	return func(n *Navi) {
		if s != nil {
			n.thoughtStore = s
		}
	}
}

// WithQueueConfig overrides the queue's scheduling configuration
// (concurrency, priority slots, foreground slots, reconcile/flush
// cadence). The default keeps reconciliation and flushing off, which is
// correct for the in-memory store; a durable navi enables both.
func WithQueueConfig(cfg thought.Config) Option {
	return func(n *Navi) { n.queueConfig = &cfg }
}

// WithDatastoreFlushInterval sets the datastore's RAM-first flush cadence
// (0 selects the default, negative is synchronous write-through). The
// pattern mirrors thought.Config's FlushInterval.
func WithDatastoreFlushInterval(d time.Duration) Option {
	return func(n *Navi) { n.datastoreFlushInterval = d }
}

// WithShortTermMemory declares that the navi keeps short-term memory: the
// always-loaded tier of user and session notes (ADR 0013), injected into
// every thought's system prompt and updated through the built-in
// memory_write tool. It requires a configured datastore — Run fails at
// assembly without one rather than silently keeping memory volatile.
func WithShortTermMemory() Option {
	return func(n *Navi) { n.shortTermMemory = true }
}

// WithProviderFallback declares a fallback provider for thoughts that name
// no provider (ADR 0009). The runtime config file's provider.fallback is
// used when this option is absent; an unknown fallback fails assembly.
func WithProviderFallback(providerName string) Option {
	return func(n *Navi) {
		if providerName != "" {
			n.fallbackName = providerName
		}
	}
}

// WithVersion sets the navi's version string.
func WithVersion(v string) Option { return func(n *Navi) { n.version = v } }

// WithPersonality sets the personality/system-prompt seed.
func WithPersonality(p string) Option { return func(n *Navi) { n.personality = p } }

// WithLogger sets the shared structured logger handed to connectors and
// portals through Deps. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(n *Navi) {
		if l != nil {
			n.log = l
		}
	}
}

// WithPermissions sets the compiled permission ceiling.
func WithPermissions(p PermissionPolicy) Option {
	return func(n *Navi) { n.Permissions = p }
}

// WithDefaultModel marks which declared provider is the default
// (ADR 0009: the navi declares a set, one is default). Run validates
// that the name refers to a declared provider.
func WithDefaultModel(providerName string) Option {
	return func(n *Navi) { n.defaultProvider = providerName }
}

// New builds a navi. It does not validate: declaration is expected to
// follow, and Run is where the whole thing is checked. New is
// deliberately infallible so the authoring path reads as plain
// construction; anything that cannot be built is reported by Run.
func New(name string, opts ...Option) *Navi {
	n := &Navi{
		name: name,
		log:  slog.Default(),
		Skills: newCollection("skill",
			func(s skill.Skill) string { return s.Name }, validateSkill),
		Tools: newCollection("tool",
			func(t tool.Tool) string { return t.Def.Name }, validateTool),
		Models: newCollection("model",
			func(p model.Provider) string { return p.Name }, validateProvider),
		Connectors: newCollection[connector.Connector](
			"connector", nil, validateConnector),
		Portals: newCollection[portal.Portal](
			"portal", nil, validatePortal),
		Modes: newCollection("mode",
			func(m Mode) string { return m.Name }, validateMode),
		Datastores: newDatastoreCollection(),
	}
	for _, o := range opts {
		if o != nil {
			o(n)
		}
	}

	// The thought system is constructed here, not added.
	//
	// FlushInterval defaults to the async dirty-set flusher (0 → 250ms):
	// the default store is the datastore's ordered writer, so mutations
	// must not write through synchronously under q.mu — a slow backend
	// would then block a lifecycle transition. Reconcile stays off unless
	// configured (the reference navi enables it), because a navi with no
	// external writers needs no adoption loop.
	cfg := thought.Config{
		MaxConcurrent:     2,
		ReconcileInterval: -1,
		FlushInterval:     0,
		SlotExhaustion:    thought.PolicyQueue,
	}
	if n.queueConfig != nil {
		cfg = *n.queueConfig
	}
	queueStore := n.thoughtStore
	if queueStore == nil {
		// Default: persist through the datastore's ordered writer (NAVI-374).
		// Before assembly binds a backend the adapter keeps thoughts in
		// memory; assembly re-runs recovery once the datastore resolves.
		queueStore = newDatastoreThoughtStore(n.Datastores.handle)
	}
	q, err := thought.NewQueue(cfg, queueStore)
	if err != nil {
		n.initErr = fmt.Errorf("navi: thought queue: %w", err)
	} else {
		n.Thoughts = q
	}

	// Execution defaults. The runner is built after options so it logs
	// through the logger the author chose.
	if n.workers == 0 {
		n.workers = 2
	}
	n.dispatch = newDispatch()
	if n.runner == nil {
		n.runner = &AgentRunner{navi: n, log: n.log}
	}
	// The steering bridge: a running thought's RouteMessage is
	// injected into its agent at the turn boundary. The runner registers
	// each agent on run start.
	n.inflight = newInFlightAgents()
	if n.Thoughts != nil {
		n.Thoughts.SetSteeringRouter(n.inflight.steer)
		n.watchForWork()
	}
	// The observation fanout merges queue lifecycle and running-agent
	// events; portals subscribe to it through portal.Deps (NAVI-352).
	n.observation = portal.NewBroker()
	if n.Thoughts != nil {
		n.Thoughts.OnEvent(func(ev thought.Event) {
			n.observation.Publish(portal.QueueEvent(ev))
		})
	}
	return n
}

// Name returns the navi's name.
func (n *Navi) Name() string { return n.name }

// Version returns the navi's version string.
func (n *Navi) Version() string { return n.version }

// Personality returns the navi's personality seed.
func (n *Navi) Personality() string { return n.personality }

// Register implements model.Registry. It fails once the navi is serving:
// the collection is frozen, and a component registering a provider at
// runtime would be widening the compiled surface (ADR 0006).
func (n *Navi) Register(p model.Provider) error {
	return n.Models.Add(p)
}

// Find implements model.Registry: components resolve providers by name
// through the navi, without importing it (ADR 0004 rule 1). The returned
// descriptor is a copy, so a caller cannot mutate the frozen collection.
func (n *Navi) Find(name string) (*model.Provider, bool) {
	p, ok := n.Models.Get(name)
	if !ok {
		return nil, false
	}
	return &p, true
}

// Run validates the navi, freezes its collections, assembles it, and
// serves until ctx is cancelled. It returns when every started
// component has stopped, or with the first error that prevented or
// interrupted serving.
func (n *Navi) Run(ctx context.Context) error {
	if err := n.validate(); err != nil {
		return err
	}
	if err := n.assemble(); err != nil {
		return err
	}
	n.freeze()
	return n.serve(ctx)
}

// Validate is the cross-cutting check Run performs: duplicate names that
// slipped past Add, mode references to unknown tools, portal address
// conflicts, mode/ceiling permission consistency, and the default-model
// declaration. It is exported for authors who want to check a navi
// without running it.
func (n *Navi) Validate() error { return n.validate() }

func (n *Navi) validate() error {
	if strings.TrimSpace(n.name) == "" {
		return ErrNoName
	}
	if n.initErr != nil {
		return n.initErr
	}
	if n.Thoughts == nil {
		return fmt.Errorf("navi: thought system missing")
	}

	// Shape re-validation: an Add error the author ignored must not slip
	// a malformed component into a serving navi.
	for _, c := range []interface{ revalidate() error }{
		n.Skills, n.Tools, n.Models, n.Connectors, n.Portals, n.Modes, n.Datastores,
	} {
		if err := c.revalidate(); err != nil {
			return err
		}
	}
	// Duplicate names, likewise: Add catches them eagerly, this catches
	// them whether or not Add's error was read.
	for _, d := range []interface{ duplicates() (string, bool) }{
		n.Skills, n.Tools, n.Models, n.Modes, n.Datastores,
	} {
		if name, dup := d.duplicates(); dup {
			return fmt.Errorf("%w: %q", ErrDuplicate, name)
		}
	}

	if err := n.validateModes(); err != nil {
		return err
	}
	if err := n.validatePorts(); err != nil {
		return err
	}
	return n.validateDefaultModel()
}

// validateModes checks every mode against the declared tools, the
// declared models, and the compiled ceiling.
func (n *Navi) validateModes() error {
	for _, m := range n.Modes.All() {
		for _, name := range m.Tools {
			if _, ok := n.Tools.Get(name); !ok {
				return fmt.Errorf("%w: mode %q activates unknown tool %q",
					ErrInvalid, m.Name, name)
			}
		}
		if m.Model != "" && n.Models.Len() > 0 {
			if !n.hasModel(m.Model) {
				return fmt.Errorf("%w: mode %q selects unknown model %q",
					ErrInvalid, m.Name, m.Model)
			}
		}
		if err := m.Permissions.Exceeds(n.Permissions); err != nil {
			return fmt.Errorf("%w: mode %q: %v", ErrInvalid, m.Name, err)
		}
	}
	return nil
}

// validatePorts rejects two portals claiming the same listen address.
// Only portals implementing portal.Addressed participate; a portal that
// serves no address is simply not part of the check.
func (n *Navi) validatePorts() error {
	seen := map[string]string{}
	for _, p := range n.Portals.All() {
		ap, ok := p.(portal.Addressed)
		if !ok {
			continue
		}
		addr := strings.TrimSpace(ap.Addr())
		if addr == "" {
			continue
		}
		if other, dup := seen[addr]; dup {
			return fmt.Errorf("%w: portals %s and %s both bind %s",
				ErrInvalid, other, describe(p), addr)
		}
		seen[addr] = describe(p)
	}
	return nil
}

// validateDefaultModel enforces ADR 0009 at build time: a declared set
// has exactly one designated default, so the binary is self-describing.
// Runtime layers may later *override* which provider is used (env >
// config, see resolveDefault) — but the compiled artifact must always
// say what it does by default.
func (n *Navi) validateDefaultModel() error {
	if n.Models.Len() == 0 {
		if n.defaultProvider != "" {
			return fmt.Errorf("%w: default model %q declared but no models are declared",
				ErrInvalid, n.defaultProvider)
		}
		return nil
	}

	marked := 0
	for _, p := range n.Models.All() {
		if p.Default {
			marked++
		}
	}
	if marked > 1 {
		return fmt.Errorf("%w: %d providers marked default; exactly one is allowed",
			ErrInvalid, marked)
	}

	// An explicit WithDefaultModel is the more specific statement, but
	// disagreeing with a descriptor marker is an ambiguity, not a
	// precedence puzzle — refuse to guess.
	if n.defaultProvider != "" {
		if !n.hasModel(n.defaultProvider) {
			return fmt.Errorf("%w: default model %q is not a declared provider",
				ErrInvalid, n.defaultProvider)
		}
		for _, p := range n.Models.All() {
			if p.Default && !strings.EqualFold(p.Name, n.defaultProvider) {
				return fmt.Errorf(
					"%w: WithDefaultModel(%q) conflicts with the provider marked default (%q); declare one",
					ErrInvalid, n.defaultProvider, p.Name)
			}
		}
		return nil
	}

	if marked == 0 {
		return fmt.Errorf(
			"%w: %d models declared but none marked default (use model.Default() or WithDefaultModel)",
			ErrInvalid, n.Models.Len())
	}
	return nil
}

// hasModel reports whether any declared provider serves the model name.
func (n *Navi) hasModel(name string) bool {
	for _, p := range n.Models.All() {
		if p.Name == name || p.Model == name {
			return true
		}
	}
	return false
}

// freeze makes every collection read-only for the life of the navi.
func (n *Navi) freeze() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.serving = true
	n.Skills.freeze()
	n.Tools.freeze()
	n.Models.freeze()
	n.Connectors.freeze()
	n.Portals.freeze()
	n.Modes.freeze()
	n.Datastores.freeze()
}

// Serving reports whether Run has started.
func (n *Navi) Serving() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.serving
}

// serve starts every connector and portal with the Deps they need, waits
// for ctx cancellation, then stops them in reverse order. A component
// that fails to start aborts the run and everything already started is
// stopped — a half-serving navi is not a state worth having.
func (n *Navi) serve(ctx context.Context) error {
	cdeps := connector.Deps{ThoughtSystem: n.Thoughts, Model: n, Log: n.log}
	pdeps := portal.Deps{ThoughtSystem: n.Thoughts, Model: n, Log: n.log, Observation: n.observation}

	var started []func()
	stopAll := func() {
		for i := len(started) - 1; i >= 0; i-- {
			started[i]()
		}
	}

	for _, c := range n.Connectors.All() {
		if err := c.Start(ctx, cdeps); err != nil {
			stopAll()
			return fmt.Errorf("navi: connector %s: start: %w", describe(c), err)
		}
		c := c
		started = append(started, func() {
			if err := c.Stop(); err != nil {
				n.log.Error("connector stop", "connector", describe(c), "err", err)
			}
		})
	}
	for _, p := range n.Portals.All() {
		if err := p.Start(ctx, pdeps); err != nil {
			stopAll()
			return fmt.Errorf("navi: portal %s: start: %w", describe(p), err)
		}
		p := p
		started = append(started, func() {
			if err := p.Stop(); err != nil {
				n.log.Error("portal stop", "portal", describe(p), "err", err)
			}
		})
	}

	// Background persistence loops (memory-first flusher + external-writer
	// reconcile). Both are no-ops when their interval is disabled; a
	// durable navi sets intervals and needs them to persist at all.
	stopReconcile := n.Thoughts.StartReconcile()
	stopFlusher := n.Thoughts.StartFlusher()
	// The datastore's ordered writer (no-op when no datastore is bound or
	// it is in synchronous mode).
	stopWriter := n.Datastores.StartWriter()

	workers := n.startWorkers(ctx)

	<-ctx.Done()

	// Stop the doors first, then let in-flight executions finish: a
	// worker whose runner returns on cancellation parks its thought
	// rather than failing it.
	stopAll()
	workers.Wait()

	// Final drain after the pool stops: the flushers persist whatever the
	// run left dirty (the park checkpoints are already synchronous).
	stopReconcile()
	stopFlusher()
	stopWriter()
	return nil
}

// --- shape validators (eager, at Add; re-applied at Run) ---------------

func validateSkill(s skill.Skill) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: skill requires a name", ErrInvalid)
	}
	if strings.HasPrefix(s.Name, "core_") {
		return fmt.Errorf("%w: skill %q uses the reserved core_ namespace (SDK core skills)", ErrInvalid, s.Name)
	}
	return nil
}

func validateTool(t tool.Tool) error {
	switch {
	case strings.TrimSpace(t.Def.Name) == "":
		return fmt.Errorf("%w: tool requires a name", ErrInvalid)
	case strings.TrimSpace(t.Def.Description) == "":
		return fmt.Errorf("%w: tool %q requires a description", ErrInvalid, t.Def.Name)
	case t.Def.Access == tool.AccessAny:
		return fmt.Errorf("%w: tool %q must declare an access level", ErrInvalid, t.Def.Name)
	case t.Def.Mode == tool.ModeAny:
		return fmt.Errorf("%w: tool %q must declare an execution mode", ErrInvalid, t.Def.Name)
	case t.Def.SourceInfo.Origin == tool.OriginAny:
		return fmt.Errorf("%w: tool %q must declare its provenance", ErrInvalid, t.Def.Name)
	case len(t.Def.InputSchema.Raw()) == 0:
		return fmt.Errorf("%w: tool %q requires an input schema", ErrInvalid, t.Def.Name)
	case t.Handler == nil:
		return fmt.Errorf("%w: tool %q requires a handler", ErrInvalid, t.Def.Name)
	}
	if _, err := tool.ParseSchema(t.Def.InputSchema.Raw()); err != nil {
		return fmt.Errorf("%w: tool %q: %v", ErrInvalid, t.Def.Name, err)
	}
	return nil
}

func validateProvider(p model.Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: provider requires a name", ErrInvalid)
	}
	return nil
}

func validateConnector(c connector.Connector) error {
	if c == nil {
		return fmt.Errorf("%w: nil connector", ErrInvalid)
	}
	return nil
}

func validatePortal(p portal.Portal) error {
	if p == nil {
		return fmt.Errorf("%w: nil portal", ErrInvalid)
	}
	return nil
}

func validateMode(m Mode) error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: mode requires a name", ErrInvalid)
	}
	return nil
}
