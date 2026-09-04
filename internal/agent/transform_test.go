package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransformSystemPrompt(t *testing.T) {
	cfg := AgentConfig{SystemPrompt: "You are navi."}
	msgs := []AgentMessage{{Role: RoleUser, Content: "hi"}}
	out := TransformContext(cfg, msgs)
	if len(out) != 2 {
		t.Fatalf("got %d messages, want 2", len(out))
	}
	if out[0].Role != "system" || FlattenContent(out[0].Content) != "You are navi." {
		t.Fatalf("first message = %+v, want system prompt", out[0])
	}
	if out[1].Role != "user" || FlattenContent(out[1].Content) != "hi" {
		t.Fatalf("second message = %+v, want user hi", out[1])
	}
}

func TestTransformNoSystemPrompt(t *testing.T) {
	out := TransformContext(AgentConfig{}, []AgentMessage{{Role: RoleUser, Content: "hi"}})
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1", len(out))
	}
}

func TestTransformRoleMapping(t *testing.T) {
	toolCall := ToolCall{Name: "file_read", Arguments: []byte(`{"path":"a.txt"}`)}
	msgs := []AgentMessage{
		{Role: RoleUser, Content: "run"},
		{Role: RoleAssistant, Content: "calling", ToolCalls: []ToolCall{toolCall}},
		{Role: RoleTool, Content: "ignored", ToolResult: "file body"},
	}
	out := TransformContext(AgentConfig{}, msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	if out[1].Role != "assistant" || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].Name != "file_read" {
		t.Fatalf("assistant tool calls not carried: %+v", out[1])
	}
	if out[2].Role != "tool" || FlattenContent(out[2].Content) != "file body" {
		t.Fatalf("tool result not carried: %+v", out[2])
	}
}

func TestTransformFiltersUIMessages(t *testing.T) {
	msgs := []AgentMessage{
		{Role: RoleUser, Content: "real"},
		{Role: RoleUser, Content: ""}, // UI-only placeholder
	}
	out := TransformContext(AgentConfig{}, msgs)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1 (empty filtered)", len(out))
	}
	// Assistant with tool calls but no content is preserved.
	msgs = []AgentMessage{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "t"}}},
	}
	out = TransformContext(AgentConfig{}, msgs)
	if len(out) != 1 {
		t.Fatalf("tool-call-only message should be kept, got %d", len(out))
	}
}

func TestTransformContextFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := AgentConfig{ContextFiles: []string{"notes.txt"}, WorkDir: dir}
	out := TransformContext(cfg, nil)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1 context file message", len(out))
	}
	text := FlattenContent(out[0].Content)
	if !strings.Contains(text, "File: notes.txt") || !strings.Contains(text, "hello notes") {
		t.Fatalf("context file not injected correctly: %q", text)
	}
	if out[0].Role != "user" {
		t.Fatalf("context file role = %q, want user", out[0].Role)
	}
}

func TestTransformContextFileUnreadable(t *testing.T) {
	cfg := AgentConfig{ContextFiles: []string{"/definitely/not/a/real/file.xyz"}}
	out := TransformContext(cfg, nil)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1", len(out))
	}
	if !strings.Contains(FlattenContent(out[0].Content), "unable to read") {
		t.Fatalf("unreadable file should be noted: %q", FlattenContent(out[0].Content))
	}
}

func TestTransformProviderTransform(t *testing.T) {
	msgs := []Message{{Role: "system", Content: []Content{TextContent("s")}}}
	for _, p := range []string{"ollama", "lmstudio", "Ollama", "unknown-future-provider"} {
		out := applyProviderTransform(p, msgs)
		if len(out) != len(msgs) {
			t.Fatalf("provider %q transform changed message count", p)
		}
	}
}
