package agent

// Agent-facing provider seam. The concrete Ollama client lives in the
// provider layer (llm, #187) — this file adapts it onto the
// agent's own Message/Event types so the run loop is untouched. When
// the provider layer matures further (#184+), this adapter is the only
// place that changes.

import (
	"context"
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/internal/llm"
)

// OllamaProvider adapts the provider layer's Ollama client onto the
// agent's types.
type OllamaProvider struct {
	inner llm.Provider // interface: tests inject the scripted mock
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

// ListModels returns the models available on the instance (only when
// the underlying provider supports discovery).
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	if lm, ok := p.inner.(interface {
		ListModels(context.Context) ([]string, error)
	}); ok {
		return lm.ListModels(ctx)
	}
	return nil, fmt.Errorf("provider does not support model discovery")
}

// Close releases provider resources (delegates when the underlying
// provider supports it).
func (p *OllamaProvider) Close() error {
	if closer, ok := p.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// Stream adapts agent types onto llm.Provider.Stream and maps llm
// stream events back onto agent Events.
func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	return streamFromLLM(ctx, p.inner, "ollama", messages, tools, config)
}

// LLMProviderAdapter adapts any llm.Provider onto the agent's
// ProviderInterface. Unlike OllamaProvider — which binds the provider
// layer's Ollama client — this accepts an arbitrary llm.Provider, so a
// composition root (or a test) can drive the agent loop with the
// provider it resolved (ADR 0009) without depending on the ollama
// adapter. name is the logical provider name carried on llm.Config.
type LLMProviderAdapter struct {
	inner llm.Provider
	name  string
}

// NewProviderAdapter wraps p, forwarding name to llm.Config.Provider.
func NewProviderAdapter(p llm.Provider, name string) *LLMProviderAdapter {
	return &LLMProviderAdapter{inner: p, name: name}
}

// Stream adapts agent types onto the wrapped provider.
func (p *LLMProviderAdapter) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	return streamFromLLM(ctx, p.inner, p.name, messages, tools, config)
}

// Close releases the wrapped provider's resources when it supports
// closing.
func (p *LLMProviderAdapter) Close() error {
	if closer, ok := p.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// streamFromLLM is the shared agent<->llm stream bridge: it forwards the
// conversation and maps llm stream events onto agent events. Both
// OllamaProvider and LLMProviderAdapter route through it, so the mapping
// lives in one place.
func streamFromLLM(ctx context.Context, inner llm.Provider, name string, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	events, err := inner.Stream(ctx, toLLMMessages(messages), toLLMTools(tools), agentConfigToLLM(name, config))
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

func agentConfigToLLM(name string, config AgentConfig) llm.Config {
	return llm.Config{
		Provider:    name,
		Model:       config.Model,
		Temperature: config.Temperature,
		MaxTokens:   config.MaxTokens,
		// Per-model request options (ADR 0034) cross the agent boundary with
		// the model they belong to, so the provider adapter emits the routing
		// and reasoning the declaration asked for — and nothing when it asked
		// for nothing.
		Routing:   toLLMRouting(config.Routing),
		Reasoning: toLLMReasoning(config.Reasoning),
	}
}

// toLLMRouting converts the agent-side routing to the provider-layer value
// (ADR 0034), or nil when there is nothing to send.
func toLLMRouting(r *Routing) *llm.Routing {
	if r == nil {
		return nil
	}
	return &llm.Routing{
		Only:              r.Only,
		Order:             r.Order,
		Ignore:            r.Ignore,
		AllowFallbacks:    r.AllowFallbacks,
		RequireParameters: r.RequireParameters,
		Quantizations:     r.Quantizations,
		Sort:              r.Sort,
	}
}

// toLLMReasoning converts the agent-side reasoning request to the
// provider-layer value (ADR 0034), or nil when none is declared.
func toLLMReasoning(r *Reasoning) *llm.Reasoning {
	if r == nil {
		return nil
	}
	return &llm.Reasoning{Effort: r.Effort}
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

// Compile-time check: the adapters satisfy the agent-side interface.
var (
	_ ProviderInterface = (*OllamaProvider)(nil)
	_ ProviderInterface = (*LLMProviderAdapter)(nil)
)
