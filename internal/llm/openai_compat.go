package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Shared OpenAI-compatible wire logic (spec §05: input normalization
// OpenNavi → provider). Used by the OpenRouter adapter now and the
// OpenAI adapter (#184) next — both speak the OpenAI chat-completions
// wire format.

// ── request body ─────────────────────────────────────────────────────────

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []openaiTool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  []openaiToolUse `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type openaiToolUse struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function openaiFn `json:"function"`
}

type openaiFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiToolSpec `json:"function"`
}

type openaiToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildOpenAIRequest assembles the chat-completions request body.
// Tool results become tool-role messages (content keyed by call ID);
// assistant tool calls become tool_calls arrays with arguments as
// strings (OpenAI wire format).
func buildOpenAIRequest(model string, messages []Message, tools []Tool, config Config, stream bool) openaiRequest {
	req := openaiRequest{
		Model:       model,
		Stream:      stream,
		Temperature: config.Temperature,
		MaxTokens:   config.MaxTokens,
	}
	for _, m := range messages {
		om := openaiMessage{Role: string(m.Role), Content: m.Content}
		switch {
		case len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, openaiToolUse{
					ID:       orSynthID(tc.ID, len(req.Messages), len(om.ToolCalls)),
					Type:     "function",
					Function: openaiFn{Name: tc.Name, Arguments: string(tc.Arguments)},
				})
			}
		case len(m.ToolResults) > 0:
			for _, tr := range m.ToolResults {
				om.Role = "tool"
				om.ToolCallID = tr.ToolCallID
				om.Content = tr.Content
			}
		}
		req.Messages = append(req.Messages, om)
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, openaiTool{
			Type: "function",
			Function: openaiToolSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return req
}

// orSynthID returns the call's ID, or a synthesized placeholder when
// the caller didn't assign one (#219: IDs are always present on the
// wire; providers that don't supply them get positional placeholders).
func orSynthID(id string, msgIdx, callIdx int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("call_%d_%d", msgIdx, callIdx)
}

// ── SSE stream parsing ───────────────────────────────────────────────────

type openaiChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// openaiSSEParser consumes an SSE body (data: {...} / data: [DONE]) and
// emits spec StreamEvents, accumulating partial tool-call arguments
// across chunks (OpenAI streams them as fragments keyed by index).
type openaiSSEParser struct {
	provider string // for error classification
	tools    []Tool // for validation (currently unused; reserved)

	pending  map[int]*openaiToolUse // accumulating tool calls by index
	started  bool
	sawUsage bool
}

func newOpenAISSEParser(provider string) *openaiSSEParser {
	return &openaiSSEParser{provider: provider, pending: make(map[int]*openaiToolUse)}
}

// feed processes one SSE line, returning the events it produces plus a
// bool indicating the stream is done ([DONE] received).
func (p *openaiSSEParser) feed(line string) ([]StreamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return nil, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		var evts []StreamEvent
		if p.started {
			evts = append(evts, StreamEvent{Type: StreamEventMessageEnd})
		}
		evts = append(evts, StreamEvent{Type: StreamEventStop})
		return evts, true
	}

	var chunk openaiChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return []StreamEvent{{Type: StreamEventError, Error: fmt.Errorf("%s: decode chunk: %w", p.provider, err)}}, true
	}
	if chunk.Error != nil {
		return []StreamEvent{{Type: StreamEventError, Error: &ProviderError{
			Provider:    p.provider,
			Code:        chunk.Error.Code,
			Message:     chunk.Error.Message,
			IsRetryable: IsOpenAIRetryable(chunk.Error.Code),
		}}}, true
	}

	var evts []StreamEvent
	if !p.started && len(chunk.Choices) > 0 {
		p.started = true
		evts = append(evts, StreamEvent{Type: StreamEventMessageStart})
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			evts = append(evts, StreamEvent{Type: StreamEventContentDelta, Content: choice.Delta.Content})
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc := p.pending[tc.Index]
			if acc == nil {
				acc = &openaiToolUse{}
				p.pending[tc.Index] = acc
				// Start event on first fragment.
				evts = append(evts, StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallDelta{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}})
			}
			if tc.ID != "" {
				acc.ID = tc.ID
			}
			if tc.Function.Name != "" {
				acc.Function.Name = tc.Function.Name
			}
			acc.Function.Arguments += tc.Function.Arguments
		}
		// finish_reason set: flush accumulated tool calls as complete
		// (end events carry full ID/name/arguments).
		if choice.FinishReason == "tool_calls" {
			for _, acc := range p.pendingToolUses() {
				evts = append(evts, StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallDelta{
					ID:        acc.ID,
					Name:      acc.Function.Name,
					Arguments: acc.Function.Arguments,
				}})
			}
		}
	}

	if chunk.Usage != nil {
		p.sawUsage = true
		evts = append(evts, StreamEvent{Type: StreamEventMessageEnd, Usage: &UsageInfo{
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
		}})
	}
	return evts, false
}

// pendingToolUses returns accumulated tool calls in index order.
func (p *openaiSSEParser) pendingToolUses() []*openaiToolUse {
	idxs := make([]int, 0, len(p.pending))
	for i := range p.pending {
		idxs = append(idxs, i)
	}
	sortIdx(idxs)
	out := make([]*openaiToolUse, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, p.pending[i])
	}
	return out
}

func sortIdx(idxs []int) {
	for i := 0; i < len(idxs); i++ {
		for j := i + 1; j < len(idxs); j++ {
			if idxs[j] < idxs[i] {
				idxs[i], idxs[j] = idxs[j], idxs[i]
			}
		}
	}
}
