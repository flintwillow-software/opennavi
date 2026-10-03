package agent

import "encoding/json"

// ── Role ─────────────────────────────────────────────────────────────────

// Role identifies the author of an AgentMessage.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool
	RoleSystem
)

// String returns the LLM-facing role name.
func (r Role) String() string {
	switch r {
	case RoleAssistant:
		return "assistant"
	case RoleTool:
		return "tool"
	case RoleSystem:
		return "system"
	default:
		return "user"
	}
}

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
	AgentEventAbort        AgentEventType = "agent_abort"
	AgentEventTurnEnd      AgentEventType = "turn_end"
	AgentEventError        AgentEventType = "error"
	AgentEventEnd          AgentEventType = "agent_end"
)

// Stream event types emitted by providers on the Event channel.
const (
	EventMessageStart = "message_start"
	EventContentDelta = "content_delta"
	EventToolCall     = "tool_call"
	EventMessageEnd   = "message_end"
	EventError        = "error"
	EventStop         = "stop"
)

// ── Queue Modes ──────────────────────────────────────────────────────────

// QueueMode controls how steering messages are queued.
type QueueMode string

const (
	QueueModeOneAtATime QueueMode = "one-at-a-time" // replace last queued message
	QueueModeAll        QueueMode = "all"           // queue all messages
)

// ── Core Data Types ──────────────────────────────────────────────────────

// AgentMessage represents a single message in the agent's state.
type AgentMessage struct {
	Role        Role       `json:"role"`
	Content     string     `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolResult  string     `json:"tool_result,omitempty"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	IsStreaming bool       `json:"is_streaming,omitempty"`
}

// ToolCall represents a call to a tool during agent execution.
type ToolCall struct {
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
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
	QueueMode      QueueMode   `json:"queue_mode,omitempty"`
	ContextFiles   []string    `json:"context_files,omitempty"`
	SystemPrompt   string      `json:"system_prompt,omitempty"`
	CacheRetention int         `json:"cache_retention,omitempty"`
	WorkDir        string      `json:"work_dir,omitempty"`
	// MaxTurns caps provider round-trips within one run (think→act→
	// observation iterations). Guards against a model issuing tool
	// calls forever. 0 means the default (32).
	MaxTurns int `json:"max_turns,omitempty"`
	// ContextBudget is the thought's context budget in tokens (0 =
	// disabled). As the run approaches it, the loop injects one deferral
	// notice (ADR 0013) — there is no compaction.
	ContextBudget int `json:"context_budget,omitempty"`
	// BudgetWarning is the fraction of ContextBudget at which the deferral
	// notice fires (default 0.8).
	BudgetWarning float64 `json:"budget_warning,omitempty"`
}

// DefaultMaxTurns is the effective MaxTurns when unset.
const DefaultMaxTurns = 32

// AgentEvent carries information about an agent lifecycle event.
type AgentEvent struct {
	Type    AgentEventType `json:"type"`
	Message *AgentMessage  `json:"message,omitempty"`
	Tool    *ToolUpdate    `json:"tool,omitempty"`
	Error   error          `json:"error,omitempty"`
	// Sequence is an optional monotonically increasing counter set by
	// producers (or tests) to assert per-subscriber delivery order.
	Sequence int `json:"sequence,omitempty"`
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

// Clone returns a deep copy of the state, safe to hand out or store
// while the live state keeps mutating (copy-on-write snapshots).
func (s *AgentState) Clone() *AgentState {
	out := *s
	out.Messages = make([]AgentMessage, len(s.Messages))
	for i, m := range s.Messages {
		out.Messages[i] = m
		if len(m.ToolCalls) > 0 {
			out.Messages[i].ToolCalls = make([]ToolCall, len(m.ToolCalls))
			copy(out.Messages[i].ToolCalls, m.ToolCalls)
		}
	}
	if len(s.Tools) > 0 {
		out.Tools = make([]AgentTool, len(s.Tools))
		copy(out.Tools, s.Tools)
	}
	return &out
}

// SetError returns a new AgentState with the given error recorded.
func (s *AgentState) SetError(err error) *AgentState {
	next := *s
	next.Error = err
	return &next
}

// ── LLM-Facing Types ─────────────────────────────────────────────────────

// Message is the LLM-facing message format.
type Message struct {
	Role      string     `json:"role"`
	Content   []Content  `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Content represents a block within a message (text, image, etc.).
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent is a convenience constructor for a text content block.
func TextContent(text string) Content {
	return Content{Type: "text", Text: text}
}

// ToolResult holds the output of a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Event is a stream event emitted by a provider onto the Event channel.
type Event struct {
	Type    string        `json:"type"`
	Content string        `json:"content,omitempty"`
	Message *AgentMessage `json:"message,omitempty"`
	Tool    *ToolUpdate   `json:"tool,omitempty"`
	Error   string        `json:"error,omitempty"`
}
