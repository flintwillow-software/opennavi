package navi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tools/bridge"
)

// Runner executes one thought to a terminal state. It is a seam: the
// shipped AgentRunner runs a real agent loop with a mode-selected tool
// surface; tests inject their own.
//
// A Runner returns nil for success and an error for a failed thought —
// the navi records the terminal state, so a Runner never needs to know
// how the queue represents success or failure.
type Runner interface {
	Run(ctx context.Context, t *thought.Thought) error
}

// WithRunner replaces the default runner. Tests inject one.
func WithRunner(r Runner) Option {
	return func(n *Navi) {
		if r != nil {
			n.runner = r
		}
	}
}

// WithSessionStore sets where thought transcripts are recorded. Defaults
// to an in-memory store, which means transcripts do not survive the
// process — the durable store arrives with the store port.
func WithSessionStore(s store.Store) Option {
	return func(n *Navi) {
		if s != nil {
			n.sessions = s
		}
	}
}

// WithWorkers sets how many thoughts may execute concurrently. The queue's
// own slot policy is the real limiter; extra workers simply find nothing
// claimable.
func WithWorkers(count int) Option {
	return func(n *Navi) {
		if count > 0 {
			n.workers = count
		}
	}
}

// SessionIDFor returns the session id recording a thought's transcript.
// One session per thought, keyed by the thought id. Exported because
// portals and tooling need to find a thought's history from its id.
func SessionIDFor(thoughtID string) string { return "thought:" + thoughtID }

// checkpointPrefix is the self-describing magic for the checkpoint blob
// format. The queue treats the blob as opaque; only the runner can detect
// a stale format. It mirrors the daemon runner's format so a checkpoint
// written by either path is readable by the other.
const checkpointPrefix = "navicp1:"

// AgentRunner is the real runner: one agent per thought, running the
// agent loop with a tool surface resolved from the thought's mode
// (ADR 0007) and the navi's compiled permission ceiling (ADR 0006).
//
// Checkpoint-on-park: on ctx cancellation it snapshots the in-flight
// conversation into the thought's Checkpoint so a later restore resumes
// mid-conversation rather than restarting. Provider resolution is
// per-thought (ADR 0009): an unknown provider fails the thought loudly.
type AgentRunner struct {
	navi *Navi
	log  *slog.Logger
}

// Run executes one thought to completion.
//
// Outcomes:
//   - nil → the worker marks the thought complete with t.Result
//   - ctx cancelled → snapshot Checkpoint and return ctx.Err(); the
//     worker parks the thought (park-not-kill)
//   - any other error → the worker marks the thought failed
func (r *AgentRunner) Run(ctx context.Context, t *thought.Thought) error {
	provider, modelName, err := r.navi.resolveForMode(t)
	if err != nil {
		// An unknown provider/mode fails THIS thought, loudly (ADR 0009).
		return err
	}

	a := agent.NewAgent(agent.AgentConfig{
		Provider:     provider.Name(),
		Model:        modelName,
		SystemPrompt: r.navi.systemPromptFor(t),
		Compaction:   compactionConfig(r.navi.compactBudget),
	})
	a.Provider = agent.NewProviderAdapter(provider, provider.Name())

	// Registry-first tool surface: the executor is rebuilt from the
	// registry's active set through tools/bridge, narrowed by the
	// thought's mode and the compiled ceiling. This replaces the builtins
	// NewAgent seeded, so the LLM-facing list is exactly the permitted
	// surface.
	if err := r.navi.bindTools(a, t); err != nil {
		return fmt.Errorf("navi: thought %s: %w", t.ID, err)
	}

	// Steering bridge: register the in-flight agent so a
	// message routed to this thought mid-run is injected at the turn
	// boundary. Unregistered when the run returns.
	unregister := r.navi.inflight.track(t.ID, a)
	defer unregister()

	// Observation (NAVI-352): forward the agent's events into the navi's
	// bounded observation fanout, tagged with the thought id. Subscribers
	// are bounded and asynchronous, so this never slows the run.
	unsubscribe := a.Subscribe(func(ev agent.AgentEvent) {
		r.navi.observation.Publish(agentObservation(t.ID, ev))
	}, false)
	defer unsubscribe()

	// Crash recovery / resume: restore a checkpoint if present and
	// restorable; a stale/corrupt checkpoint degrades to a fresh run
	// rather than wedging the thought.
	if len(t.Checkpoint) > 0 {
		msgs, derr := decodeCheckpoint(t.Checkpoint)
		if derr != nil {
			t.Checkpoint = nil
		} else if lerr := a.LoadState(&agent.AgentState{Status: agent.StatusIdle, Messages: msgs}); lerr != nil {
			t.Checkpoint = nil
		}
	}

	// Bridge the navi ctx to the agent: the agent derives its own context
	// internally, so Abort is the only interrupt path.
	go func() {
		select {
		case <-ctx.Done():
			a.Abort()
		case <-a.Done():
		}
	}()

	if err := a.Prompt(userPrompt(t), nil); err != nil {
		return fmt.Errorf("navi: prompt thought %s: %w", t.ID, err)
	}
	a.WaitForIdle()

	if ctx.Err() != nil {
		if blob, cerr := encodeCheckpoint(a.State().Clone().Messages); cerr == nil {
			t.Checkpoint = blob
		}
		if perr := r.navi.persistConversation(t, a.State().Clone().Messages); perr != nil {
			r.log.Error("persist interrupted thought", "thought", t.ID, "err", perr)
		}
		return ctx.Err()
	}

	st := a.State()
	if st.Status == agent.StatusError && st.Error != nil {
		return st.Error
	}
	// The completion message rides the thought, so the queue itself can
	// answer "what did this produce?" without going to the transcript.
	t.Result = lastAssistantContent(st.Messages)
	return r.navi.persistConversation(t, st.Clone().Messages)
}

// resolveForMode resolves the provider/model for one thought, letting the
// thought's mode pin a model when the thought itself names none (ADR
// 0007 composes with ADR 0009: mode default, per-thought override wins).
func (n *Navi) resolveForMode(t *thought.Thought) (llm.Provider, string, error) {
	if t.Mode == "" || t.Model != "" {
		return n.resolveFor(t)
	}
	m, ok := n.Modes.Get(t.Mode)
	if !ok {
		return nil, "", fmt.Errorf("%w: thought %s: unknown mode %q",
			ErrInvalid, t.ID, t.Mode)
	}
	if m.Model == "" {
		return n.resolveFor(t)
	}
	clone := *t
	clone.Model = m.Model
	return n.resolveFor(&clone)
}

// systemPromptFor assembles the system prompt: the navi's personality
// seed, plus the thought mode's fragment when the mode declares one.
func (n *Navi) systemPromptFor(t *thought.Thought) string {
	prompt := n.personality
	if t.Mode == "" {
		return prompt
	}
	m, ok := n.Modes.Get(t.Mode)
	if !ok || m.Prompt == "" {
		return prompt
	}
	if prompt == "" {
		return m.Prompt
	}
	return prompt + "\n\n" + m.Prompt
}

// userPrompt builds the user turn for a thought: its prompt plus the
// context accumulated through steering/routing.
func userPrompt(t *thought.Thought) string {
	body := t.Prompt
	if t.Context != "" {
		body += "\n\n" + t.Context
	}
	return body
}

// bindTools rebuilds the agent's executor from the registry's active set
// via tools/bridge, narrowed by the thought's mode and the compiled
// ceiling. The registry stays authoritative for metadata, validation, and
// dispatch; the executor only carries the resolved surface and timeout
// envelopes.
func (n *Navi) bindTools(a *agent.Agent, t *thought.Thought) error {
	if n.registry == nil {
		return fmt.Errorf("tool registry is not assembled")
	}
	opts, err := n.bindOptions(t)
	if err != nil {
		return err
	}
	ex := agent.NewToolExecutor("parallel", 4)
	if _, err := bridge.Bind(ex, n.registry, opts); err != nil {
		return fmt.Errorf("bind tools: %w", err)
	}
	a.Executor = ex
	return nil
}

// compactionConfig maps the navi's compact budget onto the agent's
// compaction settings (nil = disabled).
func compactionConfig(budget int) *agent.CompactionConfig {
	if budget <= 0 {
		return nil
	}
	return &agent.CompactionConfig{Enabled: true, MaxTokens: budget}
}

// lastAssistantContent returns the final visible assistant reply — the
// thought's completion message.
func lastAssistantContent(msgs []agent.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleAssistant && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// agentObservation normalizes a per-thought agent event onto the public
// observation stream (NAVI-352): the neutral event carries a bounded
// human-short detail, never the raw conversation.
func agentObservation(thoughtID string, ev agent.AgentEvent) portal.Event {
	return portal.Event{
		Category:  portal.CategoryAgent,
		Kind:      string(ev.Type),
		ThoughtID: thoughtID,
		Detail:    agentDetail(ev),
		At:        time.Now().UTC(),
	}
}

// agentDetail summarizes an agent event for the stream (bounded length;
// never raw credentials).
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

// ── checkpoint codec (versioned, self-describing) ───────────────────────

func encodeCheckpoint(messages []agent.AgentMessage) ([]byte, error) {
	payload, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("navi: encode checkpoint: %w", err)
	}
	out := make([]byte, 0, len(checkpointPrefix)+len(payload))
	out = append(out, checkpointPrefix...)
	return append(out, payload...), nil
}

func decodeCheckpoint(blob []byte) ([]agent.AgentMessage, error) {
	if !strings.HasPrefix(string(blob), checkpointPrefix) {
		return nil, fmt.Errorf("navi: checkpoint: unrecognized format")
	}
	payload := strings.TrimPrefix(string(blob), checkpointPrefix)
	var msgs []agent.AgentMessage
	if err := json.Unmarshal([]byte(payload), &msgs); err != nil {
		return nil, fmt.Errorf("navi: decode checkpoint payload: %w", err)
	}
	return msgs, nil
}

// ── transcript persistence ──────────────────────────────────────────────

// persistConversation records the agent conversation as session entries
// (sessions record history, #240). Creates the session if needed, then
// appends one entry per agent message; tool results carry their output.
func (n *Navi) persistConversation(t *thought.Thought, msgs []agent.AgentMessage) error {
	sess, err := n.sessionFor(t)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		content := m.Content
		if m.Role == agent.RoleTool && m.ToolResult != "" {
			content = m.ToolResult
		}
		if content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		sess.AppendEntry(store.NewEntry(store.Role(strings.ToLower(m.Role.String())), content))
	}
	return n.sessions.SaveSession(sess)
}

// sessionFor loads the thought's session, creating it on first run.
func (n *Navi) sessionFor(t *thought.Thought) (*store.Session, error) {
	id := SessionIDFor(t.ID)
	sess, err := n.sessions.LoadSession(id)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, store.ErrSessionNotFound) {
		return nil, fmt.Errorf("load session for thought %s: %w", t.ID, err)
	}
	sess = store.NewSession(n.name, t.ID)
	sess.ID = id
	return sess, nil
}

// SessionStore returns the store backing thought transcripts.
func (n *Navi) SessionStore() store.Store { return n.sessions }

// ── execution pool ──────────────────────────────────────────────────────

// dispatch wakes workers when work may be claimable. It is
// level-triggered through a one-slot channel: a signal that arrives while
// a worker is busy is dropped, but the worker re-drains the queue before
// sleeping, so no wakeup is lost.
type dispatch struct{ wake chan struct{} }

func newDispatch() *dispatch { return &dispatch{wake: make(chan struct{}, 1)} }

func (d *dispatch) signal() {
	select {
	case d.wake <- struct{}{}:
	default: // a wakeup is already pending
	}
}

// startWorkers runs the execution pool until ctx is cancelled.
func (n *Navi) startWorkers(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < n.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.workLoop(ctx)
		}()
	}
	return &wg
}

func (n *Navi) workLoop(ctx context.Context) {
	for {
		// Drain everything claimable before sleeping: one wakeup may
		// cover several thoughts, and Dequeue returns false both for
		// "empty" and "no slot free".
		for {
			t, ok := n.Thoughts.Dequeue()
			if !ok {
				break
			}
			n.runOne(ctx, t)
		}
		select {
		case <-ctx.Done():
			return
		case <-n.dispatch.wake:
		}
	}
}

// runOne executes a thought and records its terminal state.
func (n *Navi) runOne(ctx context.Context, t *thought.Thought) {
	err := n.runner.Run(ctx, t)
	if err == nil {
		if cerr := n.Thoughts.Complete(t.ID, t.Result); cerr != nil {
			n.log.Error("record thought completion", "thought", t.ID, "err", cerr)
		}
		return
	}

	// Cancellation is shutdown, not failure: park the thought so it
	// resumes rather than being marked failed. This is the
	// park-not-kill promise the queue already keeps for the daemon.
	if ctx.Err() != nil {
		if perr := n.Thoughts.Pause(t.ID); perr != nil {
			n.log.Error("park interrupted thought", "thought", t.ID, "err", perr)
		}
		return
	}

	n.log.Error("thought failed", "thought", t.ID, "err", err)
	if ferr := n.Thoughts.Fail(t.ID, err.Error()); ferr != nil {
		n.log.Error("record thought failure", "thought", t.ID, "err", ferr)
	}
}

// watchForWork wakes the execution pool whenever the thought system
// reports activity — enqueue, resume, retry, or a freed slot.
func (n *Navi) watchForWork() {
	n.Thoughts.OnEvent(func(thought.Event) { n.dispatch.signal() })
}
