package llm

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

// OllamaProvider implements Provider against an Ollama server's native
// /api/chat endpoint with NDJSON streaming. Decision note:
// the spec suggests delegating to the OpenAI adapter over the /v1
// endpoint; we use the native format instead (the spec note explicitly
// blesses it) because the OpenAI adapter (#184) doesn't exist yet and
// the native client is proven in production use since #217.
type OllamaProvider struct {
	BaseURL string // e.g. "http://localhost:11434"
	Model   string // default model; overridden by config.Model
	// Think requests the model's extended reasoning, or its absence. Nil
	// sends no `think` parameter at all, leaving the model's own default
	// (a reasoning model reasons, a plain one does not); false asks a
	// reasoning model not to, which is the difference between a fast answer
	// and a budget spent on hidden reasoning before any visible token.
	Think  *bool
	client *http.Client
}

// NewOllamaProvider creates an OllamaProvider. The base URL is taken
// from the OLLAMA_HOST environment variable when set (with or without
// a scheme), defaulting to http://localhost:11434.
func NewOllamaProvider() *OllamaProvider {
	return &OllamaProvider{
		BaseURL: OllamaHostFromEnv(),
		client:  &http.Client{},
	}
}

// NewOllamaProviderAt creates an OllamaProvider at an explicit URL.
func NewOllamaProviderAt(baseURL string) *OllamaProvider {
	return &OllamaProvider{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
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

// Name implements Provider.
func (p *OllamaProvider) Name() string { return "ollama" }

// ModelName reports the model this adapter was built for, so a fallback chain
// can retarget a request at it (see fallbackConfigFor).
func (p *OllamaProvider) ModelName() string { return p.Model }

// ── wire types (Ollama /api/chat) ────────────────────────────────────────

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	// Think is omitted when unset, so a navi that says nothing about
	// reasoning keeps the model's own default.
	Think   *bool         `json:"think,omitempty"`
	Tools   []ollamaTool  `json:"tools,omitempty"`
	Options ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

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

type ollamaChatResponse struct {
	Message         *ollamaMessage `json:"message"`
	Done            bool           `json:"done"`
	Error           string         `json:"error"`
	PromptEvalCount int            `json:"prompt_eval_count"`
	EvalCount       int            `json:"eval_count"`
}

// ── Stream ───────────────────────────────────────────────────────────────

func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
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
		Think:    p.Think,
		Tools:    toOllamaTools(tools),
		Options: ollamaOptions{
			Temperature: config.Temperature,
			NumPredict:  config.MaxTokens,
		},
	}
	// Synthesized call IDs must be unique within the WHOLE conversation
	callSeq := countPriorToolCalls(messages)
	// (the next request re-sends prior turns), so the base is offset by
	// the tool calls already present in the incoming message list.

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

	out := make(chan StreamEvent, 32)
	go p.consume(ctx, resp.Body, out, callSeq)
	return out, nil
}

// consume parses the NDJSON stream and maps chunks to StreamEvents on
// out, closing the channel when the stream ends or ctx is cancelled.
// Tool calls arrive complete (Ollama doesn't stream partial calls), so
// each maps to a tool_call_start + tool_call_end pair with a
// synthesized ID (Ollama never supplies one — #219).
func (p *OllamaProvider) consume(ctx context.Context, body io.ReadCloser, out chan StreamEvent, callSeq int) {
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
			p.send(ctx, out, StreamEvent{Type: StreamEventError, Error: fmt.Errorf("decode stream: %v", err)})
			return
		}

		if chunk.Error != "" {
			p.send(ctx, out, StreamEvent{Type: StreamEventError, Error: fmt.Errorf("%s", chunk.Error)})
			return
		}

		if !started && chunk.Message != nil {
			started = true
			p.send(ctx, out, StreamEvent{Type: StreamEventMessageStart})
		}

		if chunk.Message != nil {
			if chunk.Message.Content != "" {
				p.send(ctx, out, StreamEvent{Type: StreamEventContentDelta, Content: chunk.Message.Content})
			}
			for _, tc := range chunk.Message.ToolCalls {
				args, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					args = []byte(`{}`)
				}
				delta := &ToolCallDelta{
					ID:        fmt.Sprintf("call_%d", callSeq),
					Name:      tc.Function.Name,
					Arguments: string(args),
				}
				callSeq++
				p.send(ctx, out, StreamEvent{Type: StreamEventToolCallStart, ToolCall: delta})
				p.send(ctx, out, StreamEvent{Type: StreamEventToolCallEnd, ToolCall: delta})
			}
		}

		if chunk.Done {
			if started {
				p.send(ctx, out, StreamEvent{Type: StreamEventMessageEnd})
			}
			p.send(ctx, out, StreamEvent{Type: StreamEventStop})
			return
		}
	}

	// Stream ended without a done chunk: emit close events if we started.
	if started {
		p.send(ctx, out, StreamEvent{Type: StreamEventMessageEnd})
	}
	p.send(ctx, out, StreamEvent{Type: StreamEventStop})
}

// send delivers an event, aborting silently if ctx is cancelled.
func (p *OllamaProvider) send(ctx context.Context, out chan StreamEvent, ev StreamEvent) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// ── Complete ─────────────────────────────────────────────────────────────

func (p *OllamaProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
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
		Stream:   false,
		Think:    p.Think,
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama provider: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var final ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&final); err != nil {
		return nil, fmt.Errorf("ollama provider: decode response: %w", err)
	}
	if final.Error != "" {
		return nil, fmt.Errorf("ollama provider: %s", final.Error)
	}
	if final.Message == nil {
		return nil, fmt.Errorf("ollama provider: empty response")
	}

	// Ollama reports prompt/eval token counts on the final chunk;
	// surfaced as UsageInfo (free , useful for #189).
	var usage *UsageInfo
	if final.PromptEvalCount > 0 || final.EvalCount > 0 {
		usage = &UsageInfo{InputTokens: final.PromptEvalCount, OutputTokens: final.EvalCount}
	}

	msg := Message{Role: RoleAssistant, Content: final.Message.Content}
	for _, tc := range final.Message.ToolCalls {
		args, err := json.Marshal(tc.Function.Arguments)
		if err != nil {
			args = []byte(`{}`)
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:        fmt.Sprintf("call_%d", len(msg.ToolCalls)),
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return &Response{Message: msg, Usage: usage}, nil
}

// ── Ping / token counting / context window ───────────────────────────────

// Ping queries /api/tags.
func (p *OllamaProvider) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama provider: build ping: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama not reachable at %s: %w", p.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama provider: ping status %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("ollama provider: failed to parse ollama tags: %w", err)
	}
	return nil
}

// ListModels returns the models available on the instance (via the
// /api/tags endpoint Ping uses).
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama provider: build tags request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama provider: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama provider: tags status %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("ollama provider: decode tags: %w", err)
	}
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

func (p *OllamaProvider) TokenCount(messages []Message) int {
	return HeuristicTokenCounter{}.CountTokens(messages)
}

// MaxContextWindow returns known context windows for supported model
// families, conservative 4096 otherwise.
func (p *OllamaProvider) MaxContextWindow(config Config) int {
	model := strings.ToLower(config.Model)
	switch {
	case strings.Contains(model, "qwen2.5"), strings.Contains(model, "qwen3"):
		return 32768
	case strings.Contains(model, "llama3"):
		return 8192
	case strings.Contains(model, "codellama"):
		return 16384
	default:
		return 4096
	}
}

// ── conversion helpers ───────────────────────────────────────────────────

// toOllamaMessages converts llm messages to Ollama's flat-string
// content format. A tool-role message carrying only ToolResults
// expands into result messages; the original is skipped when it has no
// content or tool calls of its own (otherwise the wire output gets a
// spurious empty tool message).
func toOllamaMessages(messages []Message) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(messages))
	for _, m := range messages {
		for _, tr := range m.ToolResults {
			// Ollama represents tool results as tool-role messages; the
			// call ID rides along in the content prefix when present.
			// Note: this branch never fires from the agent path — the
			// agent adapter sends results as plain tool-role content —
			// but direct llm-layer consumers hit it. Ollama pairs
			// results by order; #219 will add agent-side call IDs.
			content := tr.Content
			if tr.ToolCallID != "" {
				content = fmt.Sprintf("[%s] %s", tr.ToolCallID, content)
			}
			out = append(out, ollamaMessage{Role: "tool", Content: content})
		}
		if m.Content == "" && len(m.ToolCalls) == 0 {
			continue // results-only message: nothing left to append
		}
		om := ollamaMessage{
			Role:    string(m.Role),
			Content: m.Content,
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

// toOllamaTools converts tool definitions to Ollama's function format.
func toOllamaTools(tools []Tool) []ollamaTool {
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

// compile-time Provider check.
var _ Provider = (*OllamaProvider)(nil)

// Close releases provider resources (idle HTTP connections). Note:
// Complete() reports prompt_eval_count/eval_count as UsageInfo; the
// streaming path does not (Ollama only includes counts on done chunks
// with stream:false).
func (p *OllamaProvider) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

// CloseIdler is implemented by providers that hold idle connections.
// Optional: the agent adapter calls Close via this when present.
type CloseIdler interface {
	CloseIdleConnections()
}

// countPriorToolCalls counts tool calls across all messages in the
// conversation so synthesized IDs (call_<base+seq>) never collide with
// earlier turns when the conversation is re-sent.
func countPriorToolCalls(messages []Message) int {
	n := 0
	for _, m := range messages {
		n += len(m.ToolCalls)
	}
	return n
}
