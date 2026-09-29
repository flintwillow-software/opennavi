package agent

import (
	"context"
	"fmt"
)

// runLoop is the core execution loop.
// It checks steering, transforms context, streams LLM responses,
// processes events, executes tool calls, and repeats until stop.
//
// agent_end is NOT fired here; the run manager in agent.go fires it
// after waiting on the AgentEndBarrier so one-shot subscribers can
// finish processing first.
func (a *Agent) runLoop(ctx context.Context) {
	maxTurns := a.Config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	turns := 0
loop:
	for {
		select {
		case <-ctx.Done():
			a.setState(a.state.SetStatus(StatusIdle))
			a.FireEvent(AgentEvent{Type: AgentEventAbort})
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		default:
		}

		// 1. Check for steering messages (user input during execution)
		if msg, ok := a.Steering.Next(); ok {
			a.setState(a.state.AppendMessage(AgentMessage{Role: RoleUser, Content: msg}))
		}

		// 2. Transform context to LLM format
		messages := TransformContext(a.Config, a.state.Messages)

		// 3. Convert executor tools to agent tool descriptors
		tools := a.Executor.Tools()

		// 4. Context window compaction: runs between turns,
		// before the next provider call, when the budget is crossed.
		if err := a.maybeCompact(ctx); err != nil {
			a.setState(a.state.SetStatus(StatusError).SetError(err))
			a.FireEvent(AgentEvent{Type: AgentEventError, Error: err})
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		}

		// 5. Send to provider (counts as a turn: one provider round-trip)
		turns++
		if turns > maxTurns {
			err := fmt.Errorf("run exceeded MaxTurns (%d): the model kept issuing tool calls", maxTurns)
			a.setState(a.state.SetStatus(StatusError).SetError(err))
			a.FireEvent(AgentEvent{Type: AgentEventError, Error: err})
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		}
		evts, err := a.Provider.Stream(ctx, messages, tools, a.Config)
		if err != nil {
			a.setState(a.state.SetStatus(StatusError).SetError(err))
			a.FireEvent(AgentEvent{
				Type:  AgentEventError,
				Error: fmt.Errorf("stream error: %w", err),
			})
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		}

		// 6. Process stream events
		a.FireEvent(AgentEvent{Type: AgentEventTurnStart})

		fullMessage := AgentMessage{Role: RoleAssistant, IsStreaming: true}
		haveMessage := false
		toolCalls := []ToolCall{}

		for ev := range evts {
			switch ev.Type {
			case EventMessageStart:
				a.FireEvent(AgentEvent{Type: AgentEventMessageStart})

			case EventContentDelta:
				// Keep the full message assembly internal (it's appended
				// to state at stop); fire only the NEW text in the delta
				// event — cumulative payloads in a delta-named event make
				// every UI staircase-print (the #227 REPL did exactly
				// that). message_end still carries the full message.
				fullMessage.Content += ev.Content
				fullMessage.IsStreaming = true
				haveMessage = true
				a.FireEvent(AgentEvent{Type: AgentEventContentDelta, Message: &AgentMessage{
					Role:        RoleAssistant,
					Content:     ev.Content,
					IsStreaming: true,
				}})

			case EventToolCall:
				if ev.Tool != nil {
					tc := ToolCall{Name: ev.Tool.Name, Arguments: ev.Tool.Input}
					// Synthesize a unique call ID when the provider doesn't
					// supply one (Ollama never does): tc_<turn>_<index>.
					tc.ToolCallID = ev.Tool.ToolCallID
					if tc.ToolCallID == "" {
						tc.ToolCallID = fmt.Sprintf("tc_%d_%d", turns, len(toolCalls))
					}
					toolCalls = append(toolCalls, tc)
					fullMessage.ToolCalls = append(fullMessage.ToolCalls, tc)
					haveMessage = true
					tool := *ev.Tool
					tool.ToolCallID = tc.ToolCallID
					a.FireEvent(AgentEvent{Type: AgentEventToolCall, Tool: &tool})
				}

			case EventMessageEnd:
				fullMessage.IsStreaming = false
				msg := fullMessage
				a.FireEvent(AgentEvent{Type: AgentEventMessageEnd, Message: &msg})

			case EventError:
				err := fmt.Errorf("provider error: %s", ev.Error)
				a.setState(a.state.SetStatus(StatusError).SetError(err))
				a.FireEvent(AgentEvent{Type: AgentEventError, Error: err})
				a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
				return

			case EventStop:
				if haveMessage {
					fullMessage.IsStreaming = false
					a.setState(a.state.AppendMessage(fullMessage))
				}

				// 7. Execute tool calls if any, feed results back, keep looping
				if len(toolCalls) > 0 {
					results := a.Executor.Execute(toolCalls)
					for i, r := range results {
						a.setState(a.state.AppendMessage(AgentMessage{
							Role:       RoleTool,
							Content:    r.Result,
							ToolResult: r.Result,
							ToolCallID: r.ToolCallID,
						}))
						name := ""
						if i < len(toolCalls) {
							name = toolCalls[i].Name
						}
						a.FireEvent(AgentEvent{Type: AgentEventToolResult, Tool: &ToolUpdate{
							ToolCallID: r.ToolCallID,
							Name:       name,
							Input:      toolCalls[i].Arguments,
							Result:     r.Result,
							IsError:    r.IsError,
						}})
					}
					continue loop // think -> act -> observation
				}

				// 8. No tool calls: the turn is complete
				a.setState(a.state.SetStatus(StatusIdle))
				a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
				return
			}
		}

		// Stream ended without an explicit stop event (e.g. the provider
		// closed its channel, including on ctx cancellation).
		if haveMessage {
			fullMessage.IsStreaming = false
			a.setState(a.state.AppendMessage(fullMessage))
		}
		if len(toolCalls) == 0 {
			a.setState(a.state.SetStatus(StatusIdle))
			if ctx.Err() != nil {
				a.FireEvent(AgentEvent{Type: AgentEventAbort})
			}
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		}
		if ctx.Err() != nil {
			// Cancelled with pending tool calls: don't run tools on a dead
			// run; report the abort before closing the turn.
			a.setState(a.state.SetStatus(StatusIdle))
			a.FireEvent(AgentEvent{Type: AgentEventAbort})
			a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
			return
		}
		// Tool calls present: loop again so the model sees the results.
	}
}
