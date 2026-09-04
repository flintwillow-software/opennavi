package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ── Ollama Provider ──────────────────────────────────────────────────────

// OllamaProvider implements ProviderInterface using an Ollama server's
// /api/chat endpoint with NDJSON streaming.
type OllamaProvider struct {
	BaseURL string // e.g. "http://localhost:11434"
	Model   string // default model; overridden by config.Model
	client  *http.Client
}

// NewOllamaProvider creates an OllamaProvider. The base URL is taken from
// the OLLAMA_HOST environment variable when set (with or without a
// scheme), defaulting to http://localhost:11434.
func NewOllamaProvider() *OllamaProvider {
	return &OllamaProvider{
		BaseURL: OllamaHostFromEnv(),
		client:  &http.Client{},
	}
}

// OllamaHostFromEnv resolves the Ollama base URL from OLLAMA_HOST.
func OllamaHostFromEnv() string {
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		return "http://localhost:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimSuffix(host, "/")
}

// ollamaChatRequest is the /api/chat request body.
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Options  ollamaOptions   `json:"options,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

// ollamaMessage is Ollama's chat message format (content is a string).
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaTool struct {
	Type     string        `json:"type"`
	Function ollamaToolDef `json:"function"`
}

type ollamaToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ollamaChatResponse is one NDJSON chunk of the /api/chat stream.
type ollamaChatResponse struct {
	Message *ollamaMessage `json:"message"`
	Done    bool           `json:"done"`
	Error   string         `json:"error"`
}

// Stream sends a chat completion request to Ollama and returns a channel
// of stream events: message_start, content_delta, tool_call,
// message_end, stop (or error). The channel closes when the stream ends
// or ctx is cancelled.
func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	model := config.Model
	if model == "" {
		model = p.Model
	}
	if model == "" {
		return nil, fmt.Errorf("ollama provider: no model configured")
	}

	body := ollamaChatRequest{
		Model:    model,
		Messages: toOllamaMessages(messages),
		Stream:   true,
		Tools:    toOllamaTools(tools),
		Options: ollamaOptions{
			Temperature: config.Temperature,
			NumPredict:  config.MaxTokens,
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama provider: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ollama provider: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama provider: request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama provider: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	out := make(chan Event, 32)
	go p.consume(ctx, resp.Body, out)
	return out, nil
}

// consume parses the NDJSON stream and maps chunks to Events on out,
// closing the channel when the stream ends or ctx is cancelled.
func (p *OllamaProvider) consume(ctx context.Context, body io.ReadCloser, out chan Event) {
	defer close(out)
	defer body.Close()

	dec := json.NewDecoder(body)
	started := false

	for {
		var chunk ollamaChatResponse
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			// Cancellation closes the body mid-read; treat as normal end.
			if ctx.Err() != nil {
				return
			}
			p.send(ctx, out, Event{Type: EventError, Error: fmt.Sprintf("decode stream: %v", err)})
			return
		}

		if chunk.Error != "" {
			p.send(ctx, out, Event{Type: EventError, Error: chunk.Error})
			return
		}

		if !started && chunk.Message != nil {
			started = true
			p.send(ctx, out, Event{Type: EventMessageStart})
		}

		if chunk.Message != nil {
			if chunk.Message.Content != "" {
				p.send(ctx, out, Event{Type: EventContentDelta, Content: chunk.Message.Content})
			}
			for _, tc := range chunk.Message.ToolCalls {
				args, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					args = []byte(`{}`)
				}
				p.send(ctx, out, Event{Type: EventToolCall, Tool: &ToolUpdate{
					Name:  tc.Function.Name,
					Input: args,
				}})
			}
		}

		if chunk.Done {
			if started {
				p.send(ctx, out, Event{Type: EventMessageEnd})
			}
			p.send(ctx, out, Event{Type: EventStop})
			return
		}
	}

	// Stream ended without a done chunk: emit close events if we started.
	if started {
		p.send(ctx, out, Event{Type: EventMessageEnd})
	}
	p.send(ctx, out, Event{Type: EventStop})
}

// send delivers an event, aborting silently if ctx is cancelled.
func (p *OllamaProvider) send(ctx context.Context, out chan Event, ev Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// toOllamaMessages converts LLM-facing messages to Ollama's flat-string
// content format.
func toOllamaMessages(messages []Message) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(messages))
	for _, m := range messages {
		om := ollamaMessage{
			Role:    m.Role,
			Content: FlattenContent(m.Content),
		}
		for _, tc := range m.ToolCalls {
			var args map[string]any
			_ = json.Unmarshal(tc.Arguments, &args)
			if args == nil {
				args = map[string]any{}
			}
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
				Function: ollamaFunctionCall{Name: tc.Name, Arguments: args},
			})
		}
		out = append(out, om)
	}
	return out
}

// toOllamaTools converts tool descriptors to Ollama's function format.
func toOllamaTools(tools []AgentTool) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ollamaTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, ollamaTool{
			Type: "function",
			Function: ollamaToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

// FlattenContent concatenates the text of all content blocks.
func FlattenContent(blocks []Content) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// Close cleans up provider resources.
func (p *OllamaProvider) Close() error {
	return nil
}
