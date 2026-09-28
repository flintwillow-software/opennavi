package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenRouterProvider implements Provider against the OpenRouter API
// OpenAI-compatible chat completions with OpenRouter-
// specific headers, proxying to many underlying models behind one key.
type OpenRouterProvider struct {
	BaseURL string // default https://openrouter.ai/api/v1
	APIKey  string
	client  *http.Client
}

// NewOpenRouterProviderWithClient builds a provider with a custom HTTP
// client (daemon wiring uses the shared client; tests inject httptest
// clients).
func NewOpenRouterProviderWithClient(baseURL, apiKey string, client *http.Client) *OpenRouterProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenRouterProvider{BaseURL: baseURL, APIKey: apiKey, client: client}
}

// DefaultOpenRouterURL is the OpenRouter API base URL.
const DefaultOpenRouterURL = "https://openrouter.ai/api/v1"

// NewOpenRouterProvider builds a provider. The API key comes from
// config.APIKey per request; empty here is fine (Stream errors when the
// request config has no key).
func NewOpenRouterProvider() *OpenRouterProvider {
	return &OpenRouterProvider{
		BaseURL: DefaultOpenRouterURL,
		client:  &http.Client{},
	}
}

// Name implements Provider.
func (p *OpenRouterProvider) Name() string { return "openrouter" }

// knownContextWindows maps model IDs to context windows
// scenario). The live /models/{id} fetch covers everything else.
var knownContextWindows = map[string]int{
	"z-ai/glm-5.3-flash":        1310000, // 1.31M
	"anthropic/claude-sonnet-4": 200000,
	"openai/gpt-4o":             128000,
	"google/gemini-pro-1.5":     1000000,
}

// MaxContextWindow resolves the model's context window: known-model
// table first, then the live /models/{id} endpoint, then the spec's
// 4096 fallback.
func (p *OpenRouterProvider) MaxContextWindow(config Config) int {
	if w, ok := knownContextWindows[config.Model]; ok {
		return w
	}
	// Best-effort live fetch. This method has no ctx parameter, so the
	// 10s client timeout is what prevents stalls on dead connections.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(p.BaseURL + "/models/" + config.Model)
	if err != nil {
		return 4096
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 4096
	}
	var doc struct {
		Data struct {
			ContextLength int `json:"context_length"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&doc) != nil || doc.Data.ContextLength <= 0 {
		return 4096
	}
	return doc.Data.ContextLength
}

// TokenCount estimates via the heuristic (OpenRouter counts depend on
// the underlying provider, so estimates are the honest default).
func (p *OpenRouterProvider) TokenCount(messages []Message) int {
	return HeuristicTokenCounter{}.CountTokens(messages)
}

// Ping checks connectivity and auth via /models (requires a valid key).
func (p *OpenRouterProvider) Ping(ctx context.Context) error {
	req, err := p.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter: unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return &ProviderError{Provider: "openrouter", Code: 401, Message: "invalid API key"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openrouter: ping status %d", resp.StatusCode)
	}
	return nil
}

// newRequest builds an authenticated request with the OpenRouter
// identity headers (the intent behind the spec's context.WithValue
// sketch, which is not idiomatic Go).
func (p *OpenRouterProvider) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	req.Header.Set("HTTP-Referer", "https://opennavi.dev")
	req.Header.Set("X-Title", "OpenNavi")
	return req, nil
}

// ── Stream ───────────────────────────────────────────────────────────────

func (p *OpenRouterProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	if config.APIKey == "" && p.APIKey == "" {
		return nil, &ProviderError{Provider: "openrouter", Code: 401, Message: "no API key configured"}
	}
	reqBody := buildOpenAIRequest(config.Model, messages, tools, config, true)
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openrouter: encode request: %w", err)
	}

	httpReq, err := p.newRequest(ctx, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	if config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, NewProviderError("openrouter", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	out := make(chan StreamEvent, 32)
	go p.consume(ctx, resp.Body, out)
	return out, nil
}

func (p *OpenRouterProvider) consume(ctx context.Context, body io.ReadCloser, out chan StreamEvent) {
	defer close(out)
	defer body.Close()

	parser := newOpenAISSEParser("openrouter")
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		evts, done := parser.feed(scanner.Text())
		for _, ev := range evts {
			p.send(ctx, out, ev)
		}
		if done {
			return
		}
	}
	// Body ended without [DONE]: still emit the terminal pair so
	// consumers see a well-formed stream.
	if parser.started {
		for _, acc := range parser.pendingToolUses() {
			p.send(ctx, out, StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallDelta{
				ID:        acc.ID,
				Name:      acc.Function.Name,
				Arguments: acc.Function.Arguments,
			}})
		}
		p.send(ctx, out, StreamEvent{Type: StreamEventMessageEnd})
	}
	p.send(ctx, out, StreamEvent{Type: StreamEventStop})
}

func (p *OpenRouterProvider) send(ctx context.Context, out chan StreamEvent, ev StreamEvent) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// ── Complete ─────────────────────────────────────────────────────────────

func (p *OpenRouterProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	if config.APIKey == "" && p.APIKey == "" {
		return nil, &ProviderError{Provider: "openrouter", Code: 401, Message: "no API key configured"}
	}
	reqBody := buildOpenAIRequest(config.Model, messages, tools, config, false)
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openrouter: encode request: %w", err)
	}
	httpReq, err := p.newRequest(ctx, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	if config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, NewProviderError("openrouter", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var doc struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if doc.Error != nil {
		return nil, &ProviderError{Provider: "openrouter", Message: doc.Error.Message}
	}
	if len(doc.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: empty response")
	}

	msg := Message{Role: RoleAssistant, Content: doc.Choices[0].Message.Content}
	for _, tc := range doc.Choices[0].Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: []byte(tc.Function.Arguments),
		})
	}
	response := &Response{Message: msg}
	if doc.Usage != nil {
		response.Usage = &UsageInfo{
			InputTokens:  doc.Usage.PromptTokens,
			OutputTokens: doc.Usage.CompletionTokens,
		}
	}
	return response, nil
}

// compile-time check.
var _ Provider = (*OpenRouterProvider)(nil)
