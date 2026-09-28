package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool/bridge"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// checkpointPrefix is the self-describing magic for the checkpoint
// blob format (ruling condition 2: the queue treats the blob as
// opaque, so only the runner can detect a stale format).
const checkpointPrefix = "navicp1:"

// AgentRunner is the daemon's Runner: one agent + one session per
// thought, running the existing agent loop. Satisfies thought.Runner.
// Checkpoint-on-park: on ctx cancellation the runner snapshots the
// in-flight conversation into the thought's Checkpoint (opaque bytes
// to the queue, versioned for the runner), so a later restore resumes
// mid-conversation. Spec: checkpoint-save is paired with session
// log flush on the running→paused transition.
type AgentRunner struct {
	deps agentRunnerDeps
}

// agentRunnerDeps carries everything the runner needs.
type agentRunnerDeps struct {
	chain         llm.Provider
	sessionM      *session.SessionManager
	model         string
	systemPrompt  string
	workDir       string
	compactBudget int
	logger        *logging.Logger
	// providerConfig is the runtime provider config: the fallback
	// source for per-thought provider overrides. The
	// prebuilt chain + model are the runtime primary / fallback chain
	// used when the thought carries no override.
	providerConfig config.ProviderConfig
	// registry holds the assembled tool registry; bind carries the
	// bridge options (ToolPolicy mapping) used to bind its active set
	// into each thought run's executor. When nil, agents keep the
	// executor NewAgent built (builtin-only, no registry governance).
	registry *registry.Registry
	bind     bridge.Options
	// tracker lets the steering bridge resolve a running thought's
	// in-flight agent (running path): registered on run start,
	// unregistered when the run returns. May be nil (no routing).
	tracker *inFlightAgents
	// agentEvents, when set, receives every agent event of each
	// thought as (thoughtID, event) — the portal observation hook
	// It must be cheap: the agent bus already delivers
	// asynchronously per subscriber.
	agentEvents func(thoughtID string, ev agent.AgentEvent)
}

// NewAgentRunner builds the agent-backed Runner.
func NewAgentRunner(deps agentRunnerDeps) *AgentRunner {
	return &AgentRunner{deps: deps}
}

// resolvePerThoughtProvider resolves the provider + model for ONE
// thought: a thought-level Provider override replaces the
// runtime chain (built from the runtime provider config with the
// existing buildProviderFor — the same construction the primary uses;
// unknown names fail with a clear error, never a silent fallback).
// Model override applies on top; empty fields keep the runtime
// primary/fallback chain and runtime model.
func (r *AgentRunner) resolvePerThoughtProvider(t *thought.Thought) (llm.Provider, string, error) {
	if t.Provider == "" {
		return r.deps.chain, r.deps.model, nil
	}
	p, err := buildProviderFor(t.Provider, r.deps.providerConfig)
	if err != nil {
		return nil, "", fmt.Errorf("daemon: thought %s: provider %q: %w (no silent fallback — check the runtime provider config)", t.ID, t.Provider, err)
	}
	model := r.deps.model
	if t.Model != "" {
		model = t.Model
	}
	return p, model, nil
}

// Run executes one thought (one thought ≈ one agent run).
//
// Outcomes:
//   - nil error → pool marks the thought complete
//   - ctx cancelled (preemption/Pool.Cancel) → checkpoint snapshot
//     lands on the thought via Park; the worker parks it
//   - any other error → pool marks the thought failed
//
// Checkpoint restore failure degrades to a fresh re-run from the
// thought prompt (ruling condition 3): an unrestorable checkpoint
// never wedges a worker or loses the thought — worst case is a re-run.
func (r *AgentRunner) Run(ctx context.Context, t *thought.Thought) error {
	start := time.Now()
	// Provider/model per thought: the thought's override,
	// when present, replaces the runtime chain; the resolution fails
	// the thought on unknown providers.
	chain, model, perr := r.resolvePerThoughtProvider(t)
	if perr != nil {
		return perr
	}
	// One agent per thought: fresh state isolation (one worker
	// per thought, isolated processing).
	a := agent.NewAgent(agent.AgentConfig{
		Provider:     "daemon-wired",
		Model:        model,
		SystemPrompt: r.deps.systemPrompt,
		Compaction:   compactionConfig(r.deps.compactBudget),
	})
	a.Provider = agentProviderAdapter{chain: chain}

	// Registry-first tool surface (NAVI-272): the executor is
	// rebuilt from the registry's active set via tool/bridge, so the
	// LLM-facing tool list is exactly the permitted active set and
	// policy (allowed/blocked, timeout overrides) governs it. This
	// replaces the executor NewAgent seeded with raw builtins — when
	// the registry is governed, it is authoritative.
	if err := r.bindTools(a); err != nil {
		return fmt.Errorf("daemon: bind tool registry for thought %s: %w", t.ID, err)
	}

	// Register the in-flight agent for the steering bridge
	// (running path): RouteMessage hands messages to the tracker, which
	// injects them into THIS agent's steering queue at the turn
	// boundary. Unregistered when the run returns — a message routed
	// outside the run window rejects with a clear error (the ack
	// event surfaces it).
	if r.deps.tracker != nil {
		unregister := r.deps.tracker.track(t.ID, a)
		defer unregister()
	}
	// Portal observation hook: forward the thought's agent
	// events (content deltas, tool calls, transitions) to the portal
	// seam. The agent delivers async-per-subscriber, so this never
	// slows the run.
	if r.deps.agentEvents != nil {
		a.Subscribe(func(ev agent.AgentEvent) {
			r.deps.agentEvents(t.ID, ev)
		}, false)
	}

	// Crash recovery / resume: restore a checkpoint if present and
	// restorable; a stale/corrupt checkpoint degrades to a fresh run
	// (ruling condition 3). Degrading never loses the thought — the
	// worst case is a re-run from the prompt.
	if len(t.Checkpoint) > 0 {
		msgs, err := decodeCheckpoint(t.Checkpoint)
		if err != nil {
			t.Checkpoint = nil // degrade to fresh run
		} else {
			state := &agent.AgentState{Status: agent.StatusIdle, Messages: msgs}
			if lerr := a.LoadState(state); lerr != nil {
				t.Checkpoint = nil // degrade to fresh run
			}
		}
	}

	// Bridge the daemon ctx to the agent: agent runs derive their own
	// context internally; the only interrupt path is Abort. Without
	// this, Pool.Cancel/preemption/StopAndWait cannot interrupt a
	// running thought.
	go func() {
		select {
		case <-ctx.Done():
			a.Abort()
		case <-a.Done():
		}
	}()

	if err := a.Prompt(t.Prompt, nil); err != nil {
		return fmt.Errorf("daemon: prompt thought %s: %w", t.ID, err)
	}

	a.WaitForIdle()

	if r.deps.logger != nil {
		r.deps.logger.Info("thought complete", map[string]any{
			"thought":     t.ID,
			"duration_ms": time.Since(start).Milliseconds(),
		})
	}

	// Park on cancellation (ruling: park-with-checkpoint is the
	// terminal path for interrupted thoughts — never kill-and-lose).
	// Spec pairs checkpoint-save with session-log flush: persist
	// the conversation via SessionManager before returning.
	if ctx.Err() != nil {
		checkpoint, cerr := encodeCheckpoint(a.State().Clone().Messages)
		if cerr == nil {
			t.Checkpoint = checkpoint
		}
		if perr := r.persistConversation(t, a.State().Clone().Messages); perr != nil {
			fmt.Fprintf(os.Stderr, "daemon: session flush for %s: %v\n", t.ID, perr)
		}
		return ctx.Err()
	}

	st := a.State()
	switch st.Status {
	case agent.StatusError:
		if st.Error != nil {
			return st.Error
		}
		return fmt.Errorf("daemon: thought %s ended in error state", t.ID)
	default:
		// Completed: persist the conversation as a session record.
		if perr := r.persistConversation(t, a.State().Clone().Messages); perr != nil {
			fmt.Fprintf(os.Stderr, "daemon: session flush for %s: %v\n", t.ID, perr)
		}
		return nil
	}
}

// Park snapshots the in-flight conversation into the thought's
// Checkpoint AND flushes the session log (pairs
// checkpoint-save with session-log flush on the running→paused
// transition).
func (r *AgentRunner) Park(t *thought.Thought, a *agent.Agent) error {
	blob, err := encodeCheckpoint(a.State().Clone().Messages)
	if err != nil {
		return err
	}
	t.Checkpoint = blob
	// Session-log flush: record the thought's conversation as a
	// session entry (SessionManager = history, per the recorded
	// decision).
	if r.deps.sessionM != nil {
		if _, serr := r.deps.sessionM.CreateSession("daemon", t.ID); serr != nil {
			return fmt.Errorf("daemon: session log flush for %s: %w", t.ID, serr)
		}
	}
	return nil
}

// Restore loads a checkpoint into an agent (crash recovery / resume).
// Returns an error if the checkpoint is stale/corrupt — callers
// degrade to a fresh run.
func (r *AgentRunner) Restore(t *thought.Thought, a *agent.Agent) error {
	if len(t.Checkpoint) == 0 {
		return nil
	}
	msgs, err := decodeCheckpoint(t.Checkpoint)
	if err != nil {
		return fmt.Errorf("daemon: restore checkpoint for %s: %w", t.ID, err)
	}
	state := &agent.AgentState{Status: agent.StatusIdle, Messages: msgs}
	return a.LoadState(state)
}

// sessionFor returns a session bound to the thought (one session per
// thought); the manager persists it.
func (r *AgentRunner) sessionFor(t *thought.Thought) (*session.Session, error) {
	return r.deps.sessionM.CreateSession("daemon", t.ID)
}

// ── checkpoint codec (versioned, self-describing) ───────────────────────
func encodeCheckpoint(messages []agent.AgentMessage) ([]byte, error) {
	payload, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("daemon: encode checkpoint: %w", err)
	}
	out := make([]byte, 0, len(checkpointPrefix)+len(payload))
	out = append(out, checkpointPrefix...)
	return append(out, payload...), nil
}

// decodeCheckpoint parses a versioned checkpoint blob. Unrecognized
// formats error (the runner degrades to a fresh run per ruling
// condition 3).
func decodeCheckpoint(blob []byte) ([]agent.AgentMessage, error) {
	if !strings.HasPrefix(string(blob), checkpointPrefix) {
		return nil, fmt.Errorf("daemon: checkpoint: unrecognized format")
	}
	payload := strings.TrimPrefix(string(blob), checkpointPrefix)
	var msgs []agent.AgentMessage
	if err := json.Unmarshal([]byte(payload), &msgs); err != nil {
		return nil, fmt.Errorf("daemon: decode checkpoint payload: %w", err)
	}
	return msgs, nil
}

// persistConversation records the agent conversation as session
// entries (session-log flush; #240 ruling — sessions record
// history). Creates the session if needed, then appends one entry per
// agent message.
func (r *AgentRunner) persistConversation(t *thought.Thought, msgs []agent.AgentMessage) error {
	sess, err := r.deps.sessionM.CreateSession("daemon", t.ID)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		content := m.Content
		if m.Role == agent.RoleTool && m.ToolResult != "" {
			content = m.ToolResult
		}
		e := session.NewEntry(session.Role(strings.ToLower(m.Role.String())), content)
		e.ToolCallID = m.ToolCallID
		sess.AppendEntry(e)
	}
	return r.deps.sessionM.SaveSession(sess)
}

// bindTools rebuilds the agent's executor from the registry's active
// set via tool/bridge (registry authority: only active, permitted
// tools are bound — inactive builtins disappear). No-op without a
// registry (agents keep NewAgent's builtin seeding).
func (r *AgentRunner) bindTools(a *agent.Agent) error {
	if r.deps.registry == nil {
		return nil
	}
	ex := agent.NewToolExecutor("parallel", 4)
	bound, err := bridge.Bind(ex, r.deps.registry, r.deps.bind)
	if err != nil {
		return err
	}
	a.Executor = ex
	if r.deps.logger != nil && len(bound) > 0 {
		r.deps.logger.Debug("tools bound", map[string]any{"tools": bound})
	}
	return nil
}

// compactionConfig maps the CLI compact-budget flag onto the agent's
func compactionConfig(budget int) *agent.CompactionConfig {
	if budget <= 0 {
		return nil
	}
	return &agent.CompactionConfig{Enabled: true, MaxTokens: budget}
}
