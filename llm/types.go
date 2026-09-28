// Package llm implements the unified LLM provider layer (spec
// /): provider-agnostic
// message/tool/config types, the Provider contract, and token
// counting. Provider adapters (Ollama, OpenAI, Anthropic, ...) build on
// these types; the agent runtime adapts its own types onto them.
//
// ToolCall carries an ID from day one (#219): providers that supply
// call IDs map them straight through; synthesizing providers fill it —
// no breaking change when the provider layer matures.
package llm

import "time"

// Role is the LLM-facing message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a provider-agnostic conversation message.
type Message struct {
	Role        Role         // "system", "user", "assistant", "tool"
	Content     string       // text content
	ToolCalls   []ToolCall   // for assistant messages with tool calls
	ToolResults []ToolResult // for tool role messages
	Images      []ImageRef   // optional image attachments
	CreatedAt   time.Time    // optional timestamp (export/persistence)
}

// Tool is a tool definition passed to the provider.
type Tool struct {
	Name        string
	Description string
	Parameters  []byte // JSON Schema
}

// ToolCall is one tool invocation requested by the model. ID carries
// the provider's call identifier when it supplies one (#219).
type ToolCall struct {
	ID        string
	Name      string
	Arguments []byte // JSON string of arguments
}

// ToolCallDelta is a streamed tool-call event. Arguments
// may be partial while the call is streaming.
type ToolCallDelta struct {
	ID        string
	Name      string
	Arguments string // JSON string of arguments (partial for streaming)
}

// ToolResult is the outcome of a tool invocation.
type ToolResult struct {
	ToolCallID string
	Name       string
	Content    string
	IsError    bool
}

// ImageRef is an optional image attachment.
type ImageRef struct {
	Ref  string // URL or path
	Data []byte // inline image bytes (base64 on the wire)
}

// ThinkingConfig controls extended-thinking behaviour for providers
// that support it (Anthropic-style).
type ThinkingConfig struct {
	Enabled bool
	Budget  int // thinking token budget
}

// Config is the per-request provider configuration.
type Config struct {
	Provider    string // "anthropic", "openai", "ollama", "openrouter", "minimax"
	Model       string
	Temperature float64
	MaxTokens   int
	Thinking    ThinkingConfig
	Endpoint    string // optional custom endpoint (Ollama, OpenRouter)
	APIKey      string // resolved from env var or config
}

// StreamEventType enumerates stream event kinds. Tool calls
// are split into start/end events per the spec; providers that emit
// complete calls in one chunk emit Start+End back-to-back.
type StreamEventType string

const (
	StreamEventMessageStart  StreamEventType = "message_start"
	StreamEventContentDelta  StreamEventType = "content_delta"
	StreamEventToolCallStart StreamEventType = "tool_call_start"
	StreamEventToolCallEnd   StreamEventType = "tool_call_end"
	StreamEventMessageEnd    StreamEventType = "message_end"
	StreamEventStop          StreamEventType = "stop"
	StreamEventError         StreamEventType = "error"
)

// UsageInfo carries token usage when the provider reports it.
type UsageInfo struct {
	InputTokens    int
	OutputTokens   int
	ThinkingTokens int // Anthropic-specific
}

// StreamEvent is one event of a streaming response.
type StreamEvent struct {
	Type     StreamEventType
	Content  string         // text delta for content events
	ToolCall *ToolCallDelta // tool call start/end
	Usage    *UsageInfo     // token usage (if available)
	Error    error          // stream error
}

// Response is the full (non-streaming) completion result.
type Response struct {
	Message Message    // the assistant's response message
	Usage   *UsageInfo // token usage, when the provider reports it
}
