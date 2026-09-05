package agent

// Agent-facing provider seam. The concrete Ollama client lives in the
// provider layer (internal/llm, #187) — this file adapts it onto the
// agent's own Message/Event types so the run loop is untouched. When
// the provider layer matures further (#184+), this adapter is the only
// place that changes.

import (
	"context"
	"strings"

	"github.com/flintwillow-software/opennavi/internal/llm"
)

// OllamaProvider adapts the provider layer's Ollama client onto the
// agent's types.
type OllamaProvider struct {
	inner *llm.OllamaProvider
}

// NewOllamaProvider creates an Ollama-backed provider. The base URL is
// taken from $OLLAMA_HOST (with or without scheme), defaulting to
// http://localhost:11434.
func NewOllamaProvider() *OllamaProvider {
	return &OllamaProvider{inner: llm.NewOllamaProvider()}
}

// NewOllamaProviderAt creates an Ollama-backed provider at an explicit
// URL.
func NewOllamaProviderAt(baseURL string) *OllamaProvider {
	return &OllamaProvider{inner: llm.NewOllamaProviderAt(baseURL)}
}

// OllamaHostFromEnv resolves the Ollama base URL from OLLAMA_HOST
// (delegates to the provider layer).
func OllamaHostFromEnv() string { return llm.OllamaHostFromEnv() }

// Ping checks connectivity to the Ollama instance.
func (p *OllamaProvider) Ping(ctx context.Context) error { return p.inner.Ping(ctx) }

// ListModels returns the models available on the instance.
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	return p.inner.ListModels(ctx)
}

// Close releases provider resources.
func (p *OllamaProvider) Close() error { return p.inner.Close() }

// Stream adapts agent types onto llm.Provider.Stream and maps llm
// stream events back onto agent Events.
func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	events, err := p.inner.Stream(ctx, toLLMMessages(messages), toLLMTools(tools), agentConfigToLLM(config))
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 32)
	go func() {
		defer close(out)
		for ev := range events {
			switch ev.Type {
			case llm.StreamEventMessageStart:
				deliver(ctx, out, Event{Type: EventMessageStart})
			case llm.StreamEventContentDelta:
				deliver(ctx, out, Event{Type: EventContentDelta, Content: ev.Content})
			case llm.StreamEventToolCallStart:
				if ev.ToolCall != nil {
					deliver(ctx, out, Event{
						Type: EventToolCall,
						Tool: &ToolUpdate{
							ToolCallID: ev.ToolCall.ID,
							Name:       ev.ToolCall.Name,
							Input:      []byte(ev.ToolCall.Arguments),
						},
					})
				}
			case llm.StreamEventToolCallEnd:
				// The agent loop assembles calls from ToolCallStart
				// events; the end event carries no new information.
			case llm.StreamEventMessageEnd:
				deliver(ctx, out, Event{Type: EventMessageEnd})
			case llm.StreamEventStop:
				deliver(ctx, out, Event{Type: EventStop})
				return
			case llm.StreamEventError:
				deliver(ctx, out, Event{Type: EventError, Error: errString(ev.Error)})
				return
			}
		}
	}()
	return out, nil
}

func deliver(ctx context.Context, out chan Event, ev Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// errString renders an error as a string (agent events carry string
// errors for stream failures).
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func agentConfigToLLM(config AgentConfig) llm.Config {
	return llm.Config{
		Provider:    "ollama",
		Model:       config.Model,
		Temperature: config.Temperature,
		MaxTokens:   config.MaxTokens,
	}
}

func toLLMMessages(messages []Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		lm := llm.Message{
			Role:    llm.Role(strings.ToLower(m.Role)),
			Content: FlattenContent(m.Content),
		}
		for _, tc := range m.ToolCalls {
			lm.ToolCalls = append(lm.ToolCalls, llm.ToolCall{
				ID:        tc.ToolCallID,
				Name:      tc.Name,
				Arguments: append([]byte(nil), tc.Arguments...),
			})
		}
		out = append(out, lm)
	}
	return out
}

func toLLMTools(tools []AgentTool) []llm.Tool {
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, llm.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  append([]byte(nil), t.Parameters...),
		})
	}
	return out
}

// Compile-time check: the adapter satisfies the agent-side contract.
var _ ProviderInterface = (*OllamaProvider)(nil)
