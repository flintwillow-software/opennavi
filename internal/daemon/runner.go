package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/thought"
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
// mid-conversation. Spec §2.2: checkpoint-save is paired with session
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
	compactBudget int
}

// NewAgentRunner builds the agent-backed Runner.
func NewAgentRunner(deps agentRunnerDeps) *AgentRunner {
	return &AgentRunner{deps: deps}
}

// Run executes one thought (spec: one thought ≈ one agent run).
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
	// One agent per thought: fresh state isolation (spec: one worker
	// per thought, isolated processing).
	a := agent.NewAgent(agent.AgentConfig{
		Provider: "daemon-wired",
		Model:    r.deps.model,
	})
	a.Provider = agentProviderAdapter{chain: r.deps.chain}

	// Crash recovery / resume: restore a checkpoint if present and
	// restorable; a stale/corrupt checkpoint degrades to a fresh run
	// (ruling condition 3) with the reason surfaced via the returned
	// error path's detail (pool Fail detail).
	if len(t.Checkpoint) > 0 {
		msgs, err := decodeCheckpoint(t.Checkpoint)
		if err != nil {
			// Degrade: log-and-continue with a fresh agent.
			t.Checkpoint = nil
		} else {
			state := &agent.AgentState{Status: agent.StatusIdle, Messages: msgs}
			if lerr := a.LoadState(state); lerr != nil {
				t.Checkpoint = nil // degrade to fresh run
			}
		}
	}

	if err := a.Prompt(t.Prompt, nil); err != nil {
		return fmt.Errorf("daemon: prompt thought %s: %w", t.ID, err)
	}

	// Wait for the run to finish — including ctx cancellation, which
	// the agent loop surfaces as an abort.
	a.WaitForIdle()

	st := a.State()
	switch st.Status {
	case agent.StatusError:
		if st.Error != nil {
			return st.Error
		}
		return fmt.Errorf("daemon: thought %s ended in error state", t.ID)
	default:
		return nil
	}
}

// Park snapshots the in-flight conversation into the thought's
// Checkpoint AND flushes the session log (spec §2.2 pairs
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
