package navi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/bridge"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
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

// checkpointPrefix is the self-describing magic for the checkpoint blob
// format. The queue treats the blob as opaque; only the runner can detect
// a stale format. It matches the retired daemon runner's format, so
// checkpoints written before the daemon path was removed stay readable.
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

	// The thought carries the context it was created with (ADR 0013 decision
	// 2): the runner consumes it rather than rebuilding it per turn.
	a := agent.NewAgent(agent.AgentConfig{
		Provider:      provider.Name(),
		Model:         modelName,
		SystemPrompt:  t.Assembled,
		ContextBudget: r.navi.thoughtBudget,
	})
	a.Provider = agent.NewProviderAdapter(provider, provider.Name())

	// Registry-first tool surface: the executor is rebuilt from the
	// registry's active set through tool/bridge, narrowed by the
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
	// are bounded and asynchronous, so this never slows the run. The same
	// events feed the object surface's live path (ADR 0017 decision 6):
	// streaming content becomes an append on the thought's transcript.
	unsubscribe := a.Subscribe(func(ev agent.AgentEvent) {
		r.navi.observation.Publish(agentObservation(t.ID, ev))
		r.navi.objects.observeAgent(t.ID, ev)
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

// joinBlocks joins two context blocks, tolerating either being empty.
func joinBlocks(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
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
// via tool/bridge, narrowed by the thought's mode and the compiled
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
// never raw credentials). A content delta is a FRAGMENT — its whitespace is
// preserved so a consumer that concatenates deltas joins words correctly
// (NAVI-380). A tool result reports the tool's OUTPUT, not its input.
func agentDetail(ev agent.AgentEvent) string {
	if ev.Tool != nil {
		if ev.Type == agent.AgentEventToolResult || ev.Tool.Result != "" {
			out := strings.TrimSpace(ev.Tool.Result)
			if ev.Tool.IsError {
				out = "error: " + out
			}
			return clip(out, 256)
		}
		input := string(ev.Tool.Input)
		if len(input) > 120 {
			input = input[:120] + "…"
		}
		return strings.TrimSpace(ev.Tool.Name + " " + input)
	}
	if ev.Message != nil {
		if ev.Type == agent.AgentEventContentDelta {
			return clip(ev.Message.Content, 256)
		}
		return clip(strings.TrimSpace(ev.Message.Content), 256)
	}
	if ev.Error != nil {
		return ev.Error.Error()
	}
	return ""
}

// clip bounds a detail string, appending an ellipsis when it truncates.
func clip(s string, limit int) string {
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
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

// persistConversation records the agent conversation as the thought's
// transcript. A thought is the persistence aggregate (ADR 0011 decision
// 8): its transcript is keyed by thought id and written through the
// thought/transcript view, never through a session handle. The write is
// asynchronous (NAVI-374): it is staged on the datastore's ordered writer,
// so a slow backend cannot block the run, and a hard kill may lose the
// unflushed tail. A navi that declares no datastore keeps no history, so
// this is a no-op.
func (n *Navi) persistConversation(t *thought.Thought, msgs []agent.AgentMessage) error {
	if n.transcripts == nil {
		return nil
	}
	entries := make([]datastore.Entry, 0, len(msgs)+1)
	if t.Assembled != "" {
		// The assembled context is the thought's first transcript entry
		// (index 0, ADR 0013 decision 3), so it is readable through the same
		// contract as the rest of the transcript.
		entries = append(entries, datastore.NewEntry(datastore.RoleSystem, t.Assembled))
	}
	for _, m := range msgs {
		content := m.Content
		if m.Role == agent.RoleTool && m.ToolResult != "" {
			content = m.ToolResult
		}
		if content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		e := datastore.NewEntry(datastore.Role(strings.ToLower(m.Role.String())), content)
		switch {
		case len(m.ToolCalls) > 0:
			e.ToolCallID = m.ToolCalls[0].ToolCallID
			e.ToolName = m.ToolCalls[0].Name
			e.ToolInput = append(json.RawMessage(nil), m.ToolCalls[0].Arguments...)
		case m.ToolCallID != "":
			e.ToolCallID = m.ToolCallID
		}
		entries = append(entries, e)
	}
	n.Datastores.Handle().SubmitTranscript(t.ID, entries)
	return nil
}

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
	// resumes rather than being marked failed, carrying the checkpoint the
	// runner snapshotted so resume continues rather than restarts. This is
	// the park-not-kill promise the queue already keeps for the runtime.
	if ctx.Err() != nil {
		if perr := n.Thoughts.PauseWithCheckpoint(t.ID, t.Checkpoint); perr != nil {
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
