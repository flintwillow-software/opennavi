package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ── Provider Interface ───────────────────────────────────────────────────

// ProviderInterface defines the minimal methods an LLM provider must implement.
type ProviderInterface interface {
	// Stream sends the conversation to the LLM and returns a channel of
	// stream events. The channel is closed when the response completes.
	// Context cancellation should terminate the request and close the channel.
	Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error)
	Close() error
}

// ── Agent ────────────────────────────────────────────────────────────────

// Agent holds the core state and execution logic.
type Agent struct {
	ID            string
	Config        AgentConfig
	Provider      ProviderInterface
	EventBus      *EventBus
	Steering      *SteeringQueue
	followUpQueue *FollowUpQueue
	Barrier       *AgentEndBarrier
	Executor      *ToolExecutor

	mu     sync.RWMutex // guards state swaps and run handles
	state  *AgentState
	cancel context.CancelFunc
	done   chan struct{} // closed when the current run finishes
	runMu  sync.Mutex    // serialises starting runs
}

// NewAgent creates a new Agent with the given configuration.
func NewAgent(config AgentConfig) *Agent {
	queueMode := config.QueueMode
	if queueMode == "" {
		queueMode = QueueModeOneAtATime
	}
	a := &Agent{
		state:         &AgentState{Status: StatusIdle, Config: config},
		Config:        config,
		EventBus:      NewEventBus(),
		Steering:      NewSteeringQueue(queueMode),
		followUpQueue: NewFollowUpQueue(),
		Barrier:       NewAgentEndBarrier(),
		Executor:      NewToolExecutor("parallel", 4),
	}
	RegisterBuiltinTools(a.Executor, config.WorkDir)
	return a
}

// ── Lifecycle ────────────────────────────────────────────────────────────

// Prompt starts processing of a user prompt. It launches the run loop in
// a goroutine; use WaitForIdle to block until the run completes.
func (a *Agent) Prompt(text string, images []string) error {
	return a.startRun(func(st *AgentState) *AgentState {
		return st.AppendMessage(AgentMessage{Role: RoleUser, Content: text})
	})
}

// Continue resumes processing from the current state (e.g. after a pause
// or a tool-result-only state).
func (a *Agent) Continue() error {
	return a.startRun(nil)
}

// startRun transitions the agent to running and launches runLoop.
func (a *Agent) startRun(mutate func(*AgentState) *AgentState) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()

	a.mu.Lock()
	if a.state.Status == StatusRunning {
		a.mu.Unlock()
		return errors.New("agent is already running")
	}
	st := a.state
	if mutate != nil {
		st = mutate(st)
	}
	a.state = st.SetStatus(StatusRunning)
	a.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a.mu.Lock()
	if a.cancel != nil {
		a.cancel() // defensive: release any prior run's context
	}
	a.cancel = cancel
	a.done = done
	a.mu.Unlock()

	a.FireEvent(AgentEvent{Type: AgentEventStart})

	go func() {
		defer close(done)
		defer cancel()

		a.runLoop(ctx)

		// Drain the follow-up queue: each pending message starts a new turn.
		for ctx.Err() == nil {
			msg, ok := a.followUpQueue.Next()
			if !ok {
				break
			}
			a.setState(a.state.AppendMessage(AgentMessage{Role: RoleUser, Content: msg}))
			a.setState(a.state.SetStatus(StatusRunning))
			a.runLoop(ctx)
		}

		// agent_end fires only after all one-shot subscribers complete.
		a.Barrier.Wait()
		a.FireEvent(AgentEvent{Type: AgentEventEnd})

		a.mu.Lock()
		if a.state.Status == StatusRunning {
			a.state = a.state.SetStatus(StatusIdle)
		}
		a.mu.Unlock()
	}()

	return nil
}

// Abort stops the current run immediately by cancelling its context.
func (a *Agent) Abort() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitForIdle blocks until the agent completes its current run. If no
// run is active it returns immediately.
func (a *Agent) WaitForIdle() {
	a.mu.RLock()
	done := a.done
	a.mu.RUnlock()
	if done != nil {
		<-done
	}
}

// isRunning reports whether a run is currently active.
func (a *Agent) isRunning() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state.Status == StatusRunning
}

// ── Events ───────────────────────────────────────────────────────────────

// Subscribe registers a handler for agent events. If once is true the
// handler fires for the first event only, and its completion is tracked
// by the AgentEndBarrier so agent_end waits for it. Returns an
// unsubscribe func.
func (a *Agent) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
	if !once {
		return a.EventBus.Subscribe(fn, false)
	}
	a.Barrier.Add()
	var fired int32
	unsub := a.EventBus.Subscribe(func(ev AgentEvent) {
		if atomic.CompareAndSwapInt32(&fired, 0, 1) {
			defer a.Barrier.Signal()
		}
		fn(ev)
	}, true)
	return func() {
		unsub()
		if atomic.CompareAndSwapInt32(&fired, 0, 1) {
			a.Barrier.Signal()
		}
	}
}

// FireEvent dispatches an event to all subscribers.
func (a *Agent) FireEvent(ev AgentEvent) {
	a.EventBus.Fire(ev)
}

// ── Queues & State ───────────────────────────────────────────────────────

// Steer injects a message into the current turn.
func (a *Agent) Steer(text string) {
	a.Steering.Add(text)
}

// FollowUp queues a message for after the current turn.
func (a *Agent) FollowUp(text string) {
	a.followUpQueue.Add(text)
}

// ClearQueues discards pending steering and follow-up messages.
func (a *Agent) ClearQueues() {
	a.Steering.Clear()
	a.followUpQueue.Clear()
}

// Reset reinitialises the agent state and clears the queues. A running
// run is aborted first.
func (a *Agent) Reset() {
	a.Abort()
	a.WaitForIdle()
	a.ClearQueues()
	a.setState(&AgentState{Status: StatusIdle, Config: a.Config})
}

// State returns a snapshot of the current agent state.
func (a *Agent) State() *AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

// setState swaps the agent state under lock (copy-on-write swap).
func (a *Agent) setState(next *AgentState) {
	a.mu.Lock()
	a.state = next
	a.mu.Unlock()
}

// String renders a short agent description for debugging.
func (a *Agent) String() string {
	return fmt.Sprintf("agent{status=%s}", a.State().Status)
}
