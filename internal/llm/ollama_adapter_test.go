package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// drain collects every event the provider emits until the channel closes.
func drain(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var evts []StreamEvent
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evts
			}
			evts = append(evts, ev)
		case <-timeout:
			t.Fatalf("provider stream did not close; got %d events", len(evts))
		}
	}
}

func TestOllamaProviderStreamContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !req.Stream {
			t.Errorf("request.stream = false, want true")
		}
		if req.Model != "test-model" {
			t.Errorf("request.model = %q", req.Model)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"role":"assistant","content":"Hel"}}` + "\n"))
		w.Write([]byte(`{"message":{"role":"assistant","content":"lo"}}` + "\n"))
		w.Write([]byte(`{"done":true}` + "\n"))
	}))
	defer srv.Close()

	p := &OllamaProvider{BaseURL: srv.URL, client: srv.Client()}
	ch, err := p.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, Config{Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	evts := drain(t, ch)

	want := []string{string(StreamEventMessageStart), string(StreamEventContentDelta), string(StreamEventContentDelta), string(StreamEventMessageEnd), string(StreamEventStop)}
	if len(evts) != len(want) {
		t.Fatalf("got %d events %v, want %v", len(evts), evts, want)
	}
	for i, ty := range want {
		if string(evts[i].Type) != ty {
			t.Fatalf("event[%d] = %q, want %q", i, evts[i].Type, ty)
		}
	}
	if evts[1].Content != "Hel" || evts[2].Content != "lo" {
		t.Fatalf("deltas = %q, %q", evts[1].Content, evts[2].Content)
	}
}

func TestOllamaProviderStreamToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "my_tool" {
			t.Errorf("tools in request = %+v", req.Tools)
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"my_tool","arguments":{"x":1}}}]}}` + "\n"))
		w.Write([]byte(`{"done":true}` + "\n"))
	}))
	defer srv.Close()

	p := &OllamaProvider{BaseURL: srv.URL, client: srv.Client()}
	tools := []Tool{{Name: "my_tool", Description: "d"}}
	ch, err := p.Stream(context.Background(), nil, tools, Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evts := drain(t, ch)

	var sawTool bool
	for _, ev := range evts {
		if ev.Type == StreamEventToolCallStart && ev.ToolCall != nil && ev.ToolCall.Name == "my_tool" {
			sawTool = true
			if !strings.Contains(ev.ToolCall.Arguments, `"x"`) {
				t.Fatalf("tool arguments = %s, want marshalled map", ev.ToolCall.Arguments)
			}
		}
	}
	if !sawTool {
		t.Fatalf("no tool_call event in %v", evts)
	}
	if last := evts[len(evts)-1]; last.Type != StreamEventStop {
		t.Fatalf("last event = %q, want stop", last.Type)
	}
}

func TestOllamaProviderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	p := &OllamaProvider{BaseURL: srv.URL, client: srv.Client()}
	_, err := p.Stream(context.Background(), nil, nil, Config{Model: "missing"})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want status code mentioned", err)
	}
}

func TestOllamaProviderNoModel(t *testing.T) {
	p := &OllamaProvider{BaseURL: "http://localhost:1"}
	if _, err := p.Stream(context.Background(), nil, nil, Config{}); err == nil {
		t.Fatal("expected error when no model configured")
	}
}

func TestOllamaProviderContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"content":"partial"}}` + "\n"))
		w.(http.Flusher).Flush()
		<-release // hold the stream open until the test finishes
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	p := &OllamaProvider{BaseURL: srv.URL, client: srv.Client()}
	ch, err := p.Stream(ctx, nil, nil, Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	// First event arrives; then cancel and expect the channel to close.
	<-ch
	cancel()
	drain(t, ch)
}

func TestOllamaHostFromEnv(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "http://localhost:11434"},
		{"10.0.0.50:11434", "http://10.0.0.50:11434"},
		{"http://host:1234", "http://host:1234"},
		{"https://host:1234/", "https://host:1234"},
	}
	for _, c := range cases {
		t.Setenv("OLLAMA_HOST", c.in)
		if got := OllamaHostFromEnv(); got != c.want {
			t.Errorf("OLLAMA_HOST=%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToOllamaMessages(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello world"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "f", Arguments: []byte(`{"a":1}`)}}},
	}
	out := toOllamaMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("got %d messages", len(out))
	}
	if out[0].Content != "hello world" {
		t.Fatalf("content = %q", out[0].Content)
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].Function.Name != "f" {
		t.Fatalf("tool calls = %+v", out[1].ToolCalls)
	}
	if out[1].ToolCalls[0].Function.Arguments["a"] != float64(1) {
		t.Fatalf("arguments = %+v", out[1].ToolCalls[0].Function.Arguments)
	}
}

// TestOllamaProviderThinkParameter pins the reasoning knob: unset sends no
// `think` at all (the model's own default — a reasoning model reasons), and an
// explicit value is sent verbatim. `false` is the difference between an answer
// and a budget spent on hidden reasoning before any visible token.
func TestOllamaProviderThinkParameter(t *testing.T) {
	capture := func(t *testing.T, think *bool) string {
		t.Helper()
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"}}` + "\n"))
			_, _ = w.Write([]byte(`{"done":true}` + "\n"))
		}))
		defer srv.Close()

		p := &OllamaProvider{BaseURL: srv.URL, client: srv.Client(), Think: think}
		ch, err := p.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, Config{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, ch)
		return string(body)
	}

	if got := capture(t, nil); strings.Contains(got, "think") {
		t.Fatalf("an unset reasoning setting must send no `think` parameter: %s", got)
	}
	off := false
	if got := capture(t, &off); !strings.Contains(got, `"think":false`) {
		t.Fatalf("think=false missing from the request: %s", got)
	}
	on := true
	if got := capture(t, &on); !strings.Contains(got, `"think":true`) {
		t.Fatalf("think=true missing from the request: %s", got)
	}
}
