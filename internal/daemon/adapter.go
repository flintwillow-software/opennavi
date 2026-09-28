package daemon

import (
	"context"
	"strings"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/llm"
)

// agentProviderAdapter adapts the daemon's llm.Provider (possibly a
// fallback chain) onto the agent's ProviderInterface. The
// agent's LLM-facing Message type is converted to the llm layer's.
type agentProviderAdapter struct {
	chain llm.Provider
}

func (a agentProviderAdapter) Stream(ctx context.Context, messages []agent.Message, tools []agent.AgentTool, config agent.AgentConfig) (<-chan agent.Event, error) {
	// The agent package already owns the agent->llm conversion (its
	// OllamaProvider adapter); reuse it by constructing that adapter on
	// top of the daemon chain.

	events, err := a.chain.Stream(ctx, toLLMDaemonMessages(messages), toLLMDaemonTools(tools), llm.Config{Provider: "daemon", Model: config.Model, Temperature: config.Temperature, MaxTokens: config.MaxTokens})
	if err != nil {
		return nil, err
	}

	out := make(chan agent.Event, 32)
	go func() {
		defer close(out)
		for ev := range events {
			switch ev.Type {
			case llm.StreamEventMessageStart:
				deliverEvent(ctx, out, agent.Event{Type: agent.EventMessageStart})
			case llm.StreamEventContentDelta:
				deliverEvent(ctx, out, agent.Event{Type: agent.EventContentDelta, Content: ev.Content})
			case llm.StreamEventToolCallStart:
				if ev.ToolCall != nil {
					deliverEvent(ctx, out, agent.Event{
						Type: agent.EventToolCall,
						Tool: &agent.ToolUpdate{
							ToolCallID: ev.ToolCall.ID,
							Name:       ev.ToolCall.Name,
							Input:      []byte(ev.ToolCall.Arguments),
						},
					})
				}
			case llm.StreamEventToolCallEnd:
				// The agent loop assembles calls from Start events.
			case llm.StreamEventMessageEnd:
				deliverEvent(ctx, out, agent.Event{Type: agent.EventMessageEnd})
			case llm.StreamEventStop:
				deliverEvent(ctx, out, agent.Event{Type: agent.EventStop})
				return
			case llm.StreamEventError:
				deliverEvent(ctx, out, agent.Event{Type: agent.EventError, Error: errString(ev.Error)})
				return
			}
		}
	}()
	return out, nil
}

func (a agentProviderAdapter) Close() error { return nil }

func deliverEvent(ctx context.Context, out chan agent.Event, ev agent.Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// toLLMDaemonMessages converts agent LLM-facing messages to llm types
// (same mapping as the agent package's own adapter).
func toLLMDaemonMessages(messages []agent.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		var content strings.Builder
		for _, c := range m.Content {
			content.WriteString(c.Text)
		}
		lm := llm.Message{Role: llm.Role(strings.ToLower(m.Role)), Content: content.String()}
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

// toLLMDaemonTools converts agent tool descriptors to llm tools.
func toLLMDaemonTools(tools []agent.AgentTool) []llm.Tool {
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

// errString renders an error as a string (agent events carry string
// errors for stream failures).
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
