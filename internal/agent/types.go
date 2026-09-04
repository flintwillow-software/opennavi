package agent

// Type definitions for the agent system
type AgentStatus string

type AgentEventType string

type AgentMessage struct {
	Role      int
	Content   string
	ToolCalls []ToolCall
	ToolResult string
	IsStreaming bool
}

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
}

type AgentTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type AgentConfig struct {
	Provider        string
	Model           string
	Temperature     float64
	MaxTokens       int
	Thinking        bool
	Tools           []AgentTool
	ToolPlugins     []string
	QueueMode       string
	ContextFiles    []string
	SystemPrompt    string
	CacheRetention  int // minutes
}

type AgentEvent struct {
	Type    AgentEventType
	Message *AgentMessage
	Tool    *ToolUpdate
	Error   error
}

type ToolUpdate struct {
	ToolCallID string
	Name       string
	Input      json.RawMessage
	Result     string
	IsError    bool
}

type AgentState struct {
	Messages    []AgentMessage
	Tools       []AgentTool
	Config      AgentConfig
	Status      AgentStatus
	Error       error
}

type Event struct {
	Type    string `json:"type"`
	Message *AgentMessage `json:"message,omitempty"`
	Tool    *ToolUpdate `json:"tool,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type ToolResult struct {
	ToolCallID string
	Result     string
	IsError    bool
}

type Content struct {
	Type string
	Text string
}

type Agent struct {
	ID        string
	State     *AgentState
	Config    AgentConfig
	Provider  ProviderInterface // interface for LLM calls
	EventBus  *EventBus
	Steering  *SteeringQueue
	followUpQueue *FollowUpQueue
	Barrier   *AgentEndBarrier
	Executor  *ToolExecutor
}

type Message struct {
	Role    string
	Content []Content
}

type FollowUpQueue struct {
	queue []string
	mu    chan struct{}
}

func NewFollowUpQueue() *FollowUpQueue {
	return &FollowUpQueue{}
}

func (q *FollowUpQueue) Add(msg string) {
	q.queue = append(q.queue, msg)
}

func (q *FollowUpQueue) Next() (string, bool) {
	if len(q.queue) == 0 {
		return "", false
	}
	msg := q.queue[0]
	q.queue = q.queue[1:]
	return msg, true
}

type AgentEventType string

type ProviderInterface interface {
	Stream(messages []Message, tools []AgentTool, config AgentConfig) (chan Event, error)
	Close() error
}

type Agent struct {
	ID        string
	State     *AgentState
	Config    AgentConfig
	Provider  ProviderInterface
	EventBus  *EventBus
	Steering  *SteeringQueue
	followUpQueue *FollowUpQueue
	Barrier   *AgentEndBarrier
	Executor  *ToolExecutor
}

func NewAgent(config AgentConfig) *Agent {
	a := &Agent{
		State:    &AgentState{Status: StatusIdle},
		Config:   config,
		EventBus: NewEventBus(),
		Steering: NewSteeringQueue(),
		followUpQueue: NewFollowUpQueue(),
		Barrier:  NewAgentEndBarrier(nil),
		Executor: NewToolExecutor("parallel", 4),
	}
	// Register a couple of demo tools so the executor is not empty.
	RegisterDemoTools()
	return a
}

func (a *Agent) Prompt(text string, images []string) error {
	a.State.Status = StatusRunning
	_ = text
	_ = images
	return nil
}

func (a *Agent) Continue() error {
	a.State.Status = StatusRunning
	return nil
}

func (a *Agent) Abort() {
	a.State.Status = StatusError
}

func (a *Agent) WaitForIdle() {
	// placeholder
}

func (a *Agent) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
	a.EventBus.Subscribe("default", fn, once)
	return func() {
		// no‑op unsubscribe for now
	}
}

func (a *Agent) Reset() {
	a.State = &AgentState{Status: StatusIdle}
	a.State.Messages = nil
}

func (a *Agent) Steer(text string) {
	a.Steering.Add(text)
}

func (a *Agent) FollowUp(text string) {
	a.followUpQueue.Add(text)
}

func (a *Agent) ClearQueues() {
	a.Steering.queue = nil
	a.followUpQueue.queue = nil
}

func (a *Agent) State() *AgentState {
	return a.State
}

func init() {
	_ = fmt.Sprintf
}