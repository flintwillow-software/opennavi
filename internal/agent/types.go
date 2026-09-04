package agent

import "encoding/json"

// ── Status ───────────────────────────────────────────────────────────────

// AgentStatus tracks the agent's execution state.
type AgentStatus string

const (
	StatusIdle    AgentStatus = "idle"
	StatusRunning AgentStatus = "running"
	StatusPaused  AgentStatus = "paused"
	StatusError   AgentStatus = "error"
)

// ── Event Types ──────────────────────────────────────────────────────────

// AgentEventType enumerates the kinds of events the agent can emit.
type AgentEventType string

const (
	AgentEventStart        AgentEventType = "agent_start"
	AgentEventTurnStart    AgentEventType = "turn_start"
	AgentEventMessageStart AgentEventType = "message_start"
	AgentEventMessageEnd   AgentEventType = "message_end"
	AgentEventContentDelta AgentEventType = "content_delta"
	AgentEventToolCall     AgentEventType = "tool_call"
	AgentEventToolResult   AgentEventType = "tool_result"
	AgentEventTurnEnd      AgentEventType = "turn_end"
	AgentEventError        AgentEventType = "error"
	AgentEventEnd          AgentEventType = "agent_end"
)

// ── Core Data Types ──────────────────────────────────────────────────────

// AgentMessage represents a single message in the agent's state.
type AgentMessage struct {
	Role        int        `json:"role"`
	Content     string     `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolResult  string     `json:"tool_result,omitempty"`
	IsStreaming bool       `json:"is_streaming,omitempty"`
}

// ToolCall represents a call to a tool during agent execution.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// AgentTool describes a tool available to the LLM.
type AgentTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// AgentConfig holds the configuration for an agent.
type AgentConfig struct {
	Provider       string      `json:"provider"`
	Model          string      `json:"model"`
	Temperature    float64     `json:"temperature"`
	MaxTokens      int         `json:"max_tokens"`
	Thinking       bool        `json:"thinking"`
	Tools          []AgentTool `json:"tools,omitempty"`
	ToolPlugins    []string    `json:"tool_plugins,omitempty"`
	QueueMode      string      `json:"queue_mode,omitempty"`
	ContextFiles   []string    `json:"context_files,omitempty"`
	SystemPrompt   string      `json:"system_prompt,omitempty"`
	CacheRetention int         `json:"cache_retention,omitempty"`
}

// AgentEvent carries information about an agent lifecycle event.
type AgentEvent struct {
	Type    AgentEventType `json:"type"`
	Message *AgentMessage  `json:"message,omitempty"`
	Tool    *ToolUpdate    `json:"tool,omitempty"`
	Error   error          `json:"error,omitempty"`
}

// ToolUpdate carries the input and result of a tool call.
type ToolUpdate struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input,omitempty"`
	Result     string          `json:"result,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// AgentState tracks the runtime state of an agent.
type AgentState struct {
	Messages []AgentMessage `json:"messages"`
	Tools    []AgentTool    `json:"tools,omitempty"`
	Config   AgentConfig    `json:"config,omitempty"`
	Status   AgentStatus    `json:"status"`
	Error    error          `json:"error,omitempty"`
}

// ── Copy-on-Write Helpers ────────────────────────────────────────────────

// AppendMessage returns a new AgentState with msg appended to Messages.
func (s *AgentState) AppendMessage(msg AgentMessage) *AgentState {
	next := *s
	next.Messages = make([]AgentMessage, len(s.Messages)+1)
	copy(next.Messages, s.Messages)
	next.Messages[len(s.Messages)] = msg
	return &next
}

// SetStatus returns a new AgentState with the given status.
func (s *AgentState) SetStatus(st AgentStatus) *AgentState {
	next := *s
	next.Status = st
	return &next
}

// ── LLM-Facing Types ─────────────────────────────────────────────────────

// Message is the LLM-facing message format.
type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content,omitempty"`
}

// Content represents a block within a message (text, image, etc.).
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolResult holds the output of a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Event is the internal bus event type.
type Event struct {
	Type    string       `json:"type"`
	Message *AgentMessage `json:"message,omitempty"`
	Tool    *ToolUpdate  `json:"tool,omitempty"`
	Error   string       `json:"error,omitempty"`
}
