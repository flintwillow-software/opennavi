package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterStreamContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers are part of the interface.
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Title") != "OpenNAVI" {
			t.Errorf("X-Title = %q", r.Header.Get("X-Title"))
		}
		if r.Header.Get("X-OpenRouter-Title") != "OpenNAVI" {
			t.Errorf("X-OpenRouter-Title = %q", r.Header.Get("X-OpenRouter-Title"))
		}
		// The referer is the app's identity on openrouter.ai — pin it.
		if r.Header.Get("HTTP-Referer") != "https://opennavi.io" {
			t.Errorf("HTTP-Referer = %q", r.Header.Get("HTTP-Referer"))
		}
		if r.Header.Get("X-OpenRouter-Categories") != "personal-agent" {
			t.Errorf("X-OpenRouter-Categories = %q", r.Header.Get("X-OpenRouter-Categories"))
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n"))
		w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"lo"}}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]" + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, APIKey: "test-key", client: srv.Client()}
	ch, err := p.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "hi"}}, nil,
		Config{Model: "z-ai/glm-5.3-flash", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}

	var content strings.Builder
	var sawStop bool
	for ev := range ch {
		switch ev.Type {
		case StreamEventContentDelta:
			content.WriteString(ev.Content)
		case StreamEventStop:
			sawStop = true
		case StreamEventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	if content.String() != "Hello" {
		t.Fatalf("content = %q", content.String())
	}
	if !sawStop {
		t.Fatal("no stop event")
	}
}

func TestOpenRouterStreamToolCallAccumulation(t *testing.T) {
	// OpenAI-style partial tool-call fragments accumulate across chunks;
	// finish_reason flushes them as complete calls with IDs (#219 shape).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"file_read","arguments":"{\"pa"}}]}}]}` + "\n\n"))
		w.Write([]byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x.txt\"}"}}]}}]}` + "\n\n"))
		w.Write([]byte("data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]" + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, client: srv.Client()}
	ch, err := p.Stream(context.Background(), nil, nil, Config{Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}

	var ends []ToolCallDelta
	for ev := range ch {
		if ev.Type == StreamEventToolCallEnd {
			ends = append(ends, *ev.ToolCall)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("tool_call_end events = %d, want 1", len(ends))
	}
	if ends[0].ID != "call_9" || ends[0].Name != "file_read" || ends[0].Arguments != `{"path":"x.txt"}` {
		t.Fatalf("accumulated call = %+v", ends[0])
	}
}

func TestOpenRouterNon200Classified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, client: srv.Client()}
	_, err := p.Stream(context.Background(), nil, nil, Config{Model: "m", APIKey: "k"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
	if pe.Code != 429 || !pe.IsRetryable {
		t.Fatalf("ProviderError = %+v", pe)
	}
}

func TestOpenRouterNoKeyRejected(t *testing.T) {
	p := &OpenRouterProvider{BaseURL: "https://example.com", client: http.DefaultClient}
	if _, err := p.Stream(context.Background(), nil, nil, Config{Model: "m"}); err == nil {
		t.Fatal("missing API key should error")
	}
}

func TestOpenRouterCompleteWithUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, client: srv.Client()}
	resp, err := p.Complete(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Config{Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "answer" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestOpenRouterPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, client: srv.Client()}
	// No key on the provider: request carries no auth → 401 → error.
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("ping without key should fail against auth-checking server")
	}
	p2 := &OpenRouterProvider{BaseURL: srv.URL, APIKey: "k", client: srv.Client()}
	if err := p2.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRouterMaxContextWindow(t *testing.T) {
	p := &OpenRouterProvider{BaseURL: DefaultOpenRouterURL, client: http.DefaultClient}
	if got := p.MaxContextWindow(Config{Model: "z-ai/glm-5.3-flash"}); got != 1310000 {
		t.Fatalf("glm-5.3-flash window = %d, want 1310000", got)
	}
	if got := p.MaxContextWindow(Config{Model: "anthropic/claude-sonnet-4"}); got != 200000 {
		t.Fatalf("claude window = %d", got)
	}
	// Unknown model, API unreachable: 4096 fallback.
	if got := p.MaxContextWindow(Config{Model: "unknown/model"}); got != 4096 {
		t.Fatalf("unknown model window = %d, want 4096", got)
	}
}

func TestOpenRouterMaxContextWindowLiveFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/new/model") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"data":{"id":"new/model","context_length":777777}}`))
	}))
	defer srv.Close()

	p := &OpenRouterProvider{BaseURL: srv.URL, client: srv.Client()}
	if got := p.MaxContextWindow(Config{Model: "new/model"}); got != 777777 {
		t.Fatalf("fetched window = %d, want 777777", got)
	}
}

func TestBuildOpenAIRequestShape(t *testing.T) {
	req := buildOpenAIRequest("m",
		[]Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "f", Arguments: []byte(`{}`)}}},
			{Role: RoleTool, ToolResults: []ToolResult{{ToolCallID: "call_0", Content: "res"}}},
		},
		[]Tool{{Name: "f", Description: "d", Parameters: []byte(`{}`)}},
		Config{Temperature: 0.5, MaxTokens: 100}, true)

	if !req.Stream || req.Model != "m" || req.Temperature != 0.5 || req.MaxTokens != 100 {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	if req.Messages[3].Role != "tool" || req.Messages[3].ToolCallID != "call_0" {
		t.Fatalf("tool result message = %+v", req.Messages[3])
	}
	if len(req.Messages[2].ToolCalls) != 1 || req.Messages[2].ToolCalls[0].ID != "call_0" {
		t.Fatalf("assistant tool calls = %+v", req.Messages[2].ToolCalls)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "f" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	blob, _ := json.Marshal(req)
	if !strings.Contains(string(blob), `"stream":true`) {
		t.Fatal("stream flag missing")
	}
}

// TestOpenRouterEmitsProviderRouting pins ADR 0034: a model declaring routing
// emits OpenRouter's `provider` object, and the JSON says exactly what the
// declaration asked for.
//
// This asserts on the ENCODED BODY rather than the struct, because the bug
// this guards is a field that exists in Go and never reaches the wire — which
// is precisely how `model.WithThinking` was silently dropped for OpenRouter.
func TestOpenRouterEmitsProviderRouting(t *testing.T) {
	req := buildOpenAIRequest("deepseek/deepseek-v4.1-flash", nil, nil, Config{
		Routing: &Routing{
			Only:           []string{"inference-net/fp8"},
			Order:          []string{"inference-net/fp8"},
			AllowFallbacks: boolPtr(false),
		},
	}, false)

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	prov, ok := got["provider"].(map[string]any)
	if !ok {
		t.Fatalf("no provider object on the wire: %s", body)
	}
	if only, _ := prov["only"].([]any); len(only) != 1 || only[0] != "inference-net/fp8" {
		t.Errorf("provider.only = %v, want [inference-net/fp8]", prov["only"])
	}
	if order, _ := prov["order"].([]any); len(order) != 1 || order[0] != "inference-net/fp8" {
		t.Errorf("provider.order = %v, want [inference-net/fp8]", prov["order"])
	}
	// allow_fallbacks: false must be PRESENT as false, not omitted — omitted
	// means "OpenRouter default (true)", which is the opposite of the ask.
	af, present := prov["allow_fallbacks"]
	if !present {
		t.Fatalf("allow_fallbacks absent; false must reach the wire: %s", body)
	}
	if af != false {
		t.Errorf("allow_fallbacks = %v, want false", af)
	}
}

// TestOpenRouterEmitsReasoning pins the second half of ADR 0034: a declared
// reasoning effort reaches OpenRouter as `reasoning.effort`, matching the pi
// `openrouter` thinking format.
func TestOpenRouterEmitsReasoning(t *testing.T) {
	req := buildOpenAIRequest("deepseek/deepseek-v4.1-flash", nil, nil, Config{
		Reasoning: &Reasoning{Effort: "high"},
	}, false)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	reasoning, ok := got["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("no reasoning object on the wire: %s", body)
	}
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", reasoning["effort"])
	}
}

// TestOpenRouterOmitsUnsetOptions pins the other direction: a model that
// declares nothing sends a body with neither object, so adding ADR 0034 does
// not change the request for a navi that says nothing.
func TestOpenRouterOmitsUnsetOptions(t *testing.T) {
	// Nil options.
	body, err := json.Marshal(buildOpenAIRequest("m", nil, nil, Config{}, false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"provider", "reasoning"} {
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := got[key]; present {
			t.Errorf("nil %s reached the wire: %s", key, body)
		}
	}

	// A non-nil but empty Routing is also nothing: `"provider": {}` would be a
	// request the caller did not ask for.
	body, err = json.Marshal(buildOpenAIRequest("m", nil, nil, Config{
		Routing:   &Routing{},
		Reasoning: &Reasoning{Effort: "  "},
	}, false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["provider"]; present {
		t.Errorf("empty routing reached the wire: %s", body)
	}
	if _, present := got["reasoning"]; present {
		t.Errorf("blank reasoning effort reached the wire: %s", body)
	}
}

// TestOpenRouterAllowFallbacksUnsetVsFalse pins the tri-state distinction:
// unset means "no opinion" (OpenRouter's default), false means "do not
// degrade". Collapsing them would make it impossible to request the safe
// behaviour explicitly.
func TestOpenRouterAllowFallbacksUnsetVsFalse(t *testing.T) {
	unset := buildOpenAIRequest("m", nil, nil, Config{
		Routing: &Routing{Only: []string{"a"}},
	}, false)
	if unset.Provider.AllowFallbacks != nil {
		t.Error("unset allow_fallbacks should stay nil (no opinion)")
	}
	explicitFalse := buildOpenAIRequest("m", nil, nil, Config{
		Routing: &Routing{Only: []string{"a"}, AllowFallbacks: boolPtr(false)},
	}, false)
	if explicitFalse.Provider.AllowFallbacks == nil || *explicitFalse.Provider.AllowFallbacks {
		t.Error("explicit false allow_fallbacks must be carried as false")
	}
	explicitTrue := buildOpenAIRequest("m", nil, nil, Config{
		Routing: &Routing{Only: []string{"a"}, AllowFallbacks: boolPtr(true)},
	}, false)
	if explicitTrue.Provider.AllowFallbacks == nil || !*explicitTrue.Provider.AllowFallbacks {
		t.Error("explicit true allow_fallbacks must be carried as true")
	}
}

func boolPtr(v bool) *bool { return &v }
