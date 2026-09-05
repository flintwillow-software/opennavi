package llm

import (
	"context"
)

// Provider is the unified LLM provider contract (spec §01): every
// provider adapter (Ollama, OpenAI, Anthropic, ...) implements it.
type Provider interface {
	// Stream sends the conversation and returns a channel of stream
	// events. The channel closes when the response completes or ctx is
	// cancelled; a terminal StreamEventStop or StreamEventError always
	// precedes closure.
	Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error)

	// Complete is a non-streaming variant that returns the full
	// response.
	Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error)

	// Ping checks provider connectivity and auth.
	Ping(ctx context.Context) error

	// Name returns the provider identifier ("ollama", "openai", ...).
	Name() string

	// TokenCount estimates tokens for a message sequence.
	TokenCount(messages []Message) int

	// MaxContextWindow returns the model's context window size in
	// tokens.
	MaxContextWindow(config Config) int
}

// TokenCounter counts tokens for a message sequence or plain text
// (spec §4.2). Per-provider implementations: exact API-reported counts
// where available, tiktoken for OpenAI-compatible models (deferred —
// new dependency), heuristic estimation otherwise (§4.3).
type TokenCounter interface {
	CountTokens(messages []Message) int
	CountText(text string) int
}

// HeuristicTokenCounter estimates tokens at ~4 characters per token
// (spec §4.3) plus a small per-message overhead for role framing. Good
// enough for compaction triggering and budget checks; never for
// billing.
type HeuristicTokenCounter struct{}

const heuristicCharsPerToken = 4

func (HeuristicTokenCounter) CountText(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + heuristicCharsPerToken - 1) / heuristicCharsPerToken
}

func (c HeuristicTokenCounter) CountTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			total += 4 // system message overhead
		case RoleUser, RoleAssistant:
			total += 3
		case RoleTool:
			total += 3
		}
		total += c.CountText(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += c.CountText(string(tc.Arguments))
		}
		for _, tr := range msg.ToolResults {
			total += c.CountText(tr.Content)
		}
	}
	return total
}
