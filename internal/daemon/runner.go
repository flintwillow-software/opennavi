package daemon

import (
	"context"
	"fmt"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// AgentRunner is the daemon's Runner: one agent + one session per
// thought, running the existing agent loop. Satisfies thought.Runner.
// Checkpoint-on-park: on ctx cancellation the runner snapshots the
// in-flight conversation into the thought's Checkpoint (opaque bytes
// — the queue never inspects them), so a later Dequeue+restore
// resumes mid-conversation.
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
func (r *AgentRunner) Run(ctx context.Context, t *thought.Thought) error {
	// One agent per thought: fresh state isolation (spec: one worker
	// per thought, isolated processing).
	a := agent.NewAgent(agent.AgentConfig{
		Provider: "daemon-wired",
		Model:    r.deps.model,
	})
	a.Provider = agentProviderAdapter{chain: r.deps.chain}

	// Crash recovery / resume: restore a checkpoint if present.
	if len(t.Checkpoint) > 0 {
		msgs, err := unmarshalState(t.Checkpoint)
		if err != nil {
			return fmt.Errorf("daemon: restore checkpoint for %s: %w", t.ID, err)
		}
		state := &agent.AgentState{Status: agent.StatusIdle, Messages: msgs}
		if err := a.LoadState(state); err != nil {
			return fmt.Errorf("daemon: load restored state for %s: %w", t.ID, err)
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
// Checkpoint (worker-side park flow: ctx was cancelled mid-run).
func (r *AgentRunner) Park(t *thought.Thought, a *agent.Agent) error {
	blob, err := marshalState(a.State().Clone().Messages)
	if err != nil {
		return err
	}
	t.Checkpoint = blob
	return nil
}

// Restore loads a checkpoint into an agent (crash recovery / resume).
func (r *AgentRunner) Restore(t *thought.Thought, a *agent.Agent) error {
	if len(t.Checkpoint) == 0 {
		return nil
	}
	msgs, err := unmarshalState(t.Checkpoint)
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
