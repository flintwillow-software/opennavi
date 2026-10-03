package llm

import (
	"context"
	"errors"
	"unicode/utf8"
)

// Provider is the unified LLM provider interface: every
// provider adapter (Ollama, OpenAI, Anthropic, ...) implements it.
type Provider interface {
	// Stream sends the conversation and returns a channel of stream
	// events. The channel closes when the response completes or ctx is
	// cancelled; when the response completes normally a terminal
	// StreamEventStop (or StreamEventError on failure) is delivered
	// before closure — on ctx cancellation the provider may close the
	// channel without a terminal event.
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
// Per-provider implementations: exact API-reported counts
// where available, tiktoken for OpenAI-compatible models (deferred —
// new dependency), heuristic estimation otherwise.
type TokenCounter interface {
	CountTokens(messages []Message) int
	CountText(text string) int
}

// HeuristicTokenCounter estimates tokens: ~4 characters
// per token for ordinary text (counted in RUNES — bytes overestimate
// multibyte content ~3x for CJK), ~3 characters per token for
// structured tool output (JSON-shaped text compresses better than
// prose). Plus a small per-message overhead for role framing. Good
// enough for rough budgeting and diagnostics; never for
// billing.
type HeuristicTokenCounter struct{}

const (
	heuristicCharsPerToken     = 4 // prose
	toolOutputCharsPerToken    = 3 // structured/JSON-shaped tool output
	heuristicMsgOverheadSystem = 4
	heuristicMsgOverheadTurn   = 3
)

func countRunes(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + heuristicCharsPerToken - 1) / heuristicCharsPerToken
}

func (HeuristicTokenCounter) CountText(text string) int {
	return countRunes(text)
}

func (c HeuristicTokenCounter) CountTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			total += heuristicMsgOverheadSystem
		case RoleUser, RoleAssistant:
			total += heuristicMsgOverheadTurn
		case RoleTool:
			total += heuristicMsgOverheadTurn
		}
		total += countRunes(msg.Content)
		for _, tc := range msg.ToolCalls {
			// Spec mandates the 3-chars/token rule for tool OUTPUT;
			// tool call ARGUMENTS are the same shape (model-generated
			// structured JSON), so the same ratio is applied there —
			// documented judgment call.
			total += toolChars(utf8.RuneCountInString(string(tc.Arguments)))
		}
		for _, tr := range msg.ToolResults {
			// Tool output: 3 chars/token
			total += toolChars(utf8.RuneCountInString(tr.Content))
		}
	}
	return total
}

func toolChars(runes int) int {
	if runes == 0 {
		return 0
	}
	return (runes + toolOutputCharsPerToken - 1) / toolOutputCharsPerToken
}

// ErrProviderNotFound is returned by registries for unknown names.
var ErrProviderNotFound = errors.New("not found")
