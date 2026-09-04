package agent

// ── Provider Interface ───────────────────────────────────────────────────

// ProviderInterface defines the minimal methods an LLM provider must implement.
type ProviderInterface interface {
	Stream(messages []Message, tools []AgentTool, config AgentConfig) (chan Event, error)
	Close() error
}

// ── Agent ────────────────────────────────────────────────────────────────

// Agent holds the core state and execution logic.
type Agent struct {
	ID            string
	state         *AgentState
	Config        AgentConfig
	Provider      ProviderInterface
	EventBus      *EventBus
	Steering      *SteeringQueue
	followUpQueue *FollowUpQueue
	Barrier       *AgentEndBarrier
	Executor      *ToolExecutor
}

// NewAgent creates a new Agent with the given configuration.
func NewAgent(config AgentConfig) *Agent {
	a := &Agent{
		state:         &AgentState{Status: StatusIdle},
		Config:        config,
		EventBus:      NewEventBus(),
		Steering:      NewSteeringQueue(),
		followUpQueue: NewFollowUpQueue(),
		Barrier:       NewAgentEndBarrier(nil),
		Executor:      NewToolExecutor("parallel", 4),
	}
	RegisterDemoTools(a.Executor)
	return a
}

// Prompt starts (or resumes) processing of a prompt.
func (a *Agent) Prompt(text string, images []string) error {
	a.state.Status = StatusRunning
	_ = text
	_ = images
	return nil
}

// Continue resumes processing after a pause or yield.
func (a *Agent) Continue() error {
	a.state.Status = StatusRunning
	return nil
}

// Abort stops the current run immediately.
func (a *Agent) Abort() {
	a.state.Status = StatusError
}

// WaitForIdle blocks until the agent reaches idle status.
func (a *Agent) WaitForIdle() {
	// placeholder
}

// Subscribe registers a handler for agent events.
func (a *Agent) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
	a.EventBus.Subscribe("default", fn, once)
	return func() {}
}

// Reset reinitialises the agent state.
func (a *Agent) Reset() {
	a.state = &AgentState{Status: StatusIdle}
	a.state.Messages = nil
}

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

// State returns the current agent state.
func (a *Agent) State() *AgentState {
	return a.state
}

// FireEvent dispatches an event to all subscribers.
func (a *Agent) FireEvent(ev AgentEvent) {
	a.EventBus.Fire(ev)
}
