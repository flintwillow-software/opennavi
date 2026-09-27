package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/llm"
)

func TestToLLMMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: []Content{TextContent("be brief")}},
		{Role: "user", Content: []Content{TextContent("hello")}},
		{Role: "assistant", Content: []Content{TextContent("calling")}, ToolCalls: []ToolCall{{
			ToolCallID: "call_0", Name: "file_read", Arguments: json.RawMessage(`{"path":"x"}`),
		}}},
		{Role: "tool", Content: []Content{TextContent("file contents")}},
	}
	got := toLLMMessages(msgs)

	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if got[0].Role != llm.RoleSystem || got[0].Content != "be brief" {
		t.Fatalf("system = %+v", got[0])
	}
	if got[1].Role != llm.RoleUser || got[1].Content != "hello" {
		t.Fatalf("user = %+v", got[1])
	}
	if got[2].Role != llm.RoleAssistant || got[2].Content != "calling" {
		t.Fatalf("assistant = %+v", got[2])
	}
	if len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "call_0" || got[2].ToolCalls[0].Name != "file_read" {
		t.Fatalf("tool call = %+v", got[2].ToolCalls[0])
	}
	if !strings.Contains(string(got[2].ToolCalls[0].Arguments), "path") {
		t.Fatalf("arguments = %s", got[2].ToolCalls[0].Arguments)
	}
	if got[3].Role != llm.RoleTool || got[3].Content != "file contents" {
		t.Fatalf("tool = %+v", got[3])
	}
}

func TestAgentConfigToLLM(t *testing.T) {
	cfg := agentConfigToLLM("ollama", AgentConfig{Model: "qwen3.8:27b", Temperature: 0.7, MaxTokens: 512})
	if cfg.Provider != "ollama" || cfg.Model != "qwen3.8:27b" || cfg.Temperature != 0.7 || cfg.MaxTokens != 512 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestProviderStreamRemapsLLMEvents(t *testing.T) {
	// The adapter wraps llm.OllamaProvider; inject the scripted llm
	// mock underneath and verify the agent-side event mapping,
	// including the ToolCallID passthrough.
	inner := llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventMessageStart},
		{Type: llm.StreamEventContentDelta, Content: "Hello"},
		{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallDelta{
			ID: "call_7", Name: "file_read", Arguments: `{"path":"x"}`,
		}},
		{Type: llm.StreamEventMessageEnd},
		{Type: llm.StreamEventStop},
	})
	p := &OllamaProvider{inner: inner}

	ch, err := p.Stream(context.Background(),
		[]Message{{Role: "user", Content: []Content{TextContent("hi")}}},
		nil, AgentConfig{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var types []string
	var toolUpdate *ToolUpdate
	for ev := range ch {
		types = append(types, string(ev.Type))
		if ev.Type == EventToolCall {
			toolUpdate = ev.Tool
		}
	}
	want := []string{"message_start", "content_delta", "tool_call", "message_end", "stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	if toolUpdate == nil || toolUpdate.ToolCallID != "call_7" || toolUpdate.Name != "file_read" {
		t.Fatalf("tool update = %+v", toolUpdate)
	}
	if !strings.Contains(string(toolUpdate.Input), "path") {
		t.Fatalf("tool input = %s", toolUpdate.Input)
	}
}

func TestProviderStreamDelegatesToOllamaWire(t *testing.T) {
	// End-to-end through the adapter: an Ollama-shaped NDJSON server
	// comes back as agent events.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"role":"assistant","content":"hi there"}}` + "\n"))
		w.Write([]byte(`{"done":true}` + "\n"))
	}))
	defer srv.Close()

	p := NewOllamaProviderAt(srv.URL)
	ch, err := p.Stream(context.Background(),
		[]Message{{Role: "user", Content: []Content{TextContent("hello")}}},
		nil, AgentConfig{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var content strings.Builder
	for ev := range ch {
		if ev.Type == EventContentDelta {
			content.WriteString(ev.Content)
		}
	}
	if content.String() != "hi there" {
		t.Fatalf("content = %q", content.String())
	}
}

func TestProviderStreamErrorDelegation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model missing", http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewOllamaProviderAt(srv.URL)
	if _, err := p.Stream(context.Background(), nil, nil, AgentConfig{Model: "m"}); err == nil {
		t.Fatal("expected delegated stream error")
	}
}

func TestProviderPingAndListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"models":[{"name":"qwen3.8:27b"},{"name":"gemma4:12b"}]}`))
	}))
	defer srv.Close()

	p := NewOllamaProviderAt(srv.URL)
	if err := p.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "qwen3.8:27b" {
		t.Fatalf("models = %v", models)
	}
}

func TestProviderCloseDelegates(t *testing.T) {
	inner := llm.NewMockProvider()
	p := &OllamaProvider{inner: inner}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	// The mock's Close is a no-op; this just proves delegation compiles
	// and runs without error.
}
