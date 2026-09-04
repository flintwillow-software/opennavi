package agent

import (
	"context"
	"fmt"
)

// runLoop is the core execution loop (section 07 of the spec).
// It checks steering, transforms context, streams LLM responses,
// processes events, executes tool calls, and repeats until stop.
func (a *Agent) runLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			a.state = a.state.SetStatus(StatusIdle)
			a.FireEvent(AgentEvent{Type: AgentEventEnd})
			return
		default:
		}

		// 1. Check for steering messages
		if msg, ok := a.Steering.Next(); ok {
			a.state = a.state.AppendMessage(AgentMessage{Role: 0, Content: msg})
		}

		// 2. Transform context to LLM format
		messages := TransformContext(a.Config, a.state.Messages)

		// 3. Convert executor tools to agent tools
		tools := a.Executor.Tools()

		// 4. Send to provider
		evts, err := a.Provider.Stream(messages, tools, a.Config)
		if err != nil {
			a.FireEvent(AgentEvent{
				Type:  AgentEventError,
				Error: fmt.Errorf("stream error: %w", err),
			})
			a.state = a.state.SetStatus(StatusError)
			return
		}

		// 5. Process stream events
		a.FireEvent(AgentEvent{Type: AgentEventTurnStart})

		var fullText string
		var toolCalls []ToolCall

		for ev := range evts {
			switch ev.Type {
			case "message_start":
				a.FireEvent(AgentEvent{Type: AgentEventMessageStart})
			case "content_delta":
				fullText += ev.Error
				a.FireEvent(AgentEvent{Type: AgentEventContentDelta})
			case "tool_call":
				if ev.Tool != nil {
					toolCalls = append(toolCalls, ToolCall{Name: ev.Tool.Name})
					a.FireEvent(AgentEvent{Type: AgentEventToolCall, Tool: ev.Tool})
				}
			case "error":
				a.FireEvent(AgentEvent{
					Type:  AgentEventError,
					Error: fmt.Errorf("provider error: %s", ev.Error),
				})
				a.state = a.state.SetStatus(StatusError)
				return
			case "stop":
				a.state = a.state.AppendMessage(AgentMessage{
					Role:    1, // assistant
					Content: fullText,
				})
				a.state = a.state.SetStatus(StatusIdle)
				a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
				a.FireEvent(AgentEvent{Type: AgentEventEnd})
				return
			}
		}

		// 6. Execute tool calls if any
		if len(toolCalls) > 0 {
			results := a.Executor.Execute(toolCalls)
			for _, r := range results {
				a.state = a.state.AppendMessage(AgentMessage{
					Role:       2, // tool
					Content:    r.Result,
					ToolResult: r.Result,
				})
			}
		}

		// 7. If no tool calls and we got here, we're done
		if len(toolCalls) == 0 {
			a.state = a.state.SetStatus(StatusIdle)
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			a.FireEvent(AgentEvent{Type: AgentEventEnd})
			return
		}
	}
}
