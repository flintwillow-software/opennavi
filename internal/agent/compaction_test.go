package agent

import (
	"context"
	"strings"
	"testing"
)

func TestSafeCutIndexRespectsToolIntegrity(t *testing.T) {
	// messages: user, assistant(tc), tool, user, assistant, tool, user
	messages := []AgentMessage{
		{Role: RoleUser, Content: "t1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "f"}}},
		{Role: RoleTool, ToolResult: "r1"},
		{Role: RoleUser, Content: "t2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "g"}}},
		{Role: RoleTool, ToolResult: "r2"},
		{Role: RoleUser, Content: "t3"},
	}

	cases := []struct{ boundary, want int }{
		{7, 7}, // clean end
		{6, 6}, // between result and user: safe
		{5, 4}, // 5 starts with a tool result: unsafe, fall to 4
		{4, 4}, // suffix starts at assistant-with-calls, results follow: safe
		{3, 3}, // between result and user: safe
		{2, 1}, // 2 starts with a tool result: unsafe, fall to 1
		{1, 1},
		{0, 0},
	}
	for _, c := range cases {
		if got := safeCutIndex(messages, c.boundary); got != c.want {
			t.Errorf("safeCutIndex(boundary=%d) = %d, want %d", c.boundary, got, c.want)
		}
	}
}

func TestSafeCutIndexNeverSplitsToolPair(t *testing.T) {
	// A boundary that lands between assistant(tool call) and its tool
	// result must walk back to a safe point, never splitting the pair.
	messages := []AgentMessage{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "f"}}},
		{Role: RoleTool, ToolResult: "r1"},
		{Role: RoleUser, Content: "u2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "g"}}},
		{Role: RoleTool, ToolResult: "r2"},
	}
	// Boundary 5 = between the g-call (idx 4) and its result (idx 5):
	// unsafe → walk back to 4, where the suffix starts with the
	// assistant call and keeps its result with it.
	got := safeCutIndex(messages, 5)
	suffix := messages[got:]
	if suffix[0].Role == RoleTool {
		t.Fatalf("cut %d orphans a tool result", got)
	}
	if got != 4 {
		t.Fatalf("safeCutIndex(5) = %d, want 4", got)
	}
	// Integrity invariant over every safe cut: the suffix must never
	// start with an orphaned tool result.
	for boundary := 1; boundary <= len(messages); boundary++ {
		cut := safeCutIndex(messages, boundary)
		if cut < len(messages) && messages[cut].Role == RoleTool {
			t.Fatalf("boundary %d: cut %d starts suffix with tool result", boundary, cut)
		}
	}
}

// mockProviderWithSummary: call 1 issues a tool call, call 2 is the
// compaction summary, call 3 the final answer.
type compactionMockProvider struct {
	calls int
}

func (m *compactionMockProvider) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	m.calls++
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		switch m.calls {
		case 1:
			out <- Event{Type: EventToolCall, Tool: &ToolUpdate{Name: "test_tool", Input: []byte(`{}`)}}
			out <- Event{Type: EventStop}
		case 2: // compaction summarizer
			out <- Event{Type: EventContentDelta, Content: "summary of earlier work"}
			out <- Event{Type: EventStop}
		default:
			out <- Event{Type: EventContentDelta, Content: "final answer"}
			out <- Event{Type: EventStop}
		}
	}()
	return out, nil
}

func (m *compactionMockProvider) Close() error { return nil }

func TestCompactionRunsBetweenTurns(t *testing.T) {
	// Tiny budget: the first turn's content crosses the trigger, so
	// compaction fires before turn 2 and replaces the older history
	// with a system summary — without splitting the tool pair.
	a := NewAgent(AgentConfig{
		Provider: "mock", Model: "m",
		Compaction: &CompactionConfig{
			Enabled:   true,
			MaxTokens: 2, // trigger at ~2 tokens: any real content
			KeepTurns: 1,
		},
	})
	mp := &compactionMockProvider{}
	a.Provider = mp
	a.Executor.RegisterTool(AgentTool{Name: "test_tool"}, func(map[string]any) (string, error) {
		return strings.Repeat("tool output padding ", 10), nil
	})

	if err := a.Prompt("do the thing", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	st := a.State()
	// Expected after compaction: [system summary, assistant(tc), tool,
	// assistant(final)] — the user message was compacted away.
	if st.Messages[0].Role != RoleSystem || !strings.Contains(st.Messages[0].Content, "summary of earlier work") {
		t.Fatalf("first message = %+v, want system summary", st.Messages[0])
	}
	// Tool pair intact: assistant with calls followed by its result.
	for i, m := range st.Messages {
		if len(m.ToolCalls) > 0 {
			if i+1 >= len(st.Messages) || st.Messages[i+1].Role != RoleTool {
				t.Fatalf("dangling tool call at %d: %+v", i, st.Messages)
			}
		}
		if m.Role == RoleTool && (i == 0 || st.Messages[i-1].Role != RoleAssistant) {
			t.Fatalf("orphaned tool result at %d: %+v", i, st.Messages)
		}
	}
	if st.Messages[len(st.Messages)-1].Content != "final answer" {
		t.Fatalf("final message = %+v", st.Messages[len(st.Messages)-1])
	}
}

func TestCompactionDisabledByDefault(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "m"})
	if a.compaction != nil {
		t.Fatal("compaction should be nil without config")
	}
	a.Provider = &compactionMockProvider{}
	if err := a.Prompt("hi", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()
}

func TestMaybeCompactRespectsBoundary(t *testing.T) {
	// Plenty of budget: compaction must not fire even with many messages.
	a := NewAgent(AgentConfig{
		Provider: "mock", Model: "m",
		Compaction: &CompactionConfig{Enabled: true, MaxTokens: 1 << 20},
	})
	a.Provider = &compactionMockProvider{}
	for i := 0; i < 30; i++ {
		a.setState(a.State().AppendMessage(AgentMessage{Role: RoleUser, Content: strings.Repeat("m", 100)}))
	}
	if err := a.maybeCompact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.State().Messages) != 30 {
		t.Fatalf("messages = %d, want 30 (compaction should not fire)", len(a.State().Messages))
	}
}
