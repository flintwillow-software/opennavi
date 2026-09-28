package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/agent"
)

// runToolConversation drives a real agent through a tool-call turn and
// returns the agent after WaitForIdle.
func runToolConversation(t *testing.T) *agent.Agent {
	t.Helper()
	a := agent.NewAgent(agent.AgentConfig{Provider: "mock", Model: "m"})
	a.Provider = newMockAgentProvider()
	a.Executor.RegisterTool(
		agent.AgentTool{Name: "test_tool", Description: "test"},
		func(args map[string]any) (string, error) { return "tool output 42", nil },
	)
	if err := a.Prompt("do the thing", nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()
	return a
}

func TestSyncFromAgentCapturesConversation(t *testing.T) {
	a := runToolConversation(t)

	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("agent-1", "")
	if err := m.SyncFromAgent(s, a); err != nil {
		t.Fatal(err)
	}

	if s.Status != StatusPaused {
		t.Fatalf("status after sync = %q, want paused", s.Status)
	}

	// Expected entries: user, assistant(tool call), tool(result),
	// assistant(final).
	if len(s.Entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(s.Entries), s.Entries)
	}
	if s.Entries[0].Role != RoleUser || s.Entries[0].Message != "do the thing" {
		t.Fatalf("entry 0 = %+v", s.Entries[0])
	}
	ea := s.Entries[1]
	if ea.Role != RoleAssistant || ea.ToolName != "test_tool" || ea.ToolCallID == "" {
		t.Fatalf("assistant entry = %+v", ea)
	}
	if !strings.Contains(string(ea.ToolInput), "key") {
		t.Fatalf("tool input = %s", ea.ToolInput)
	}
	et := s.Entries[2]
	if et.Role != RoleTool || et.Message != "tool output 42" || et.ToolCallID != ea.ToolCallID {
		t.Fatalf("tool entry = %+v", et)
	}
	if s.Entries[3].Role != RoleAssistant || s.Entries[3].Message != "all done" {
		t.Fatalf("final entry = %+v", s.Entries[3])
	}

	// Persisted.
	stored, err := m.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 4 || stored.Status != StatusPaused {
		t.Fatalf("stored = %d entries, status %q", len(stored.Entries), stored.Status)
	}
}

func TestSyncFromAgentReplacesPreviousCapture(t *testing.T) {
	a := runToolConversation(t)
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")

	if err := m.SyncFromAgent(s, a); err != nil {
		t.Fatal(err)
	}
	// The agent state didn't change between syncs; a re-sync must not
	// double the entries (user, assistant(call), tool, assistant final).
	if err := m.SyncFromAgent(s, a); err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 4 {
		t.Fatalf("entries after re-sync = %d, want 4", len(s.Entries))
	}
}

func TestSnapshotRoundTripWithToolCalls(t *testing.T) {
	a1 := runToolConversation(t)
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("agent-1", "")
	if err := m.SyncFromAgent(s, a1); err != nil {
		t.Fatal(err)
	}

	// Resume into a fresh agent (as a restarted process would).
	a2 := agent.NewAgent(agent.AgentConfig{Provider: "mock", Model: "m"})
	a2.Provider = newMockAgentProvider()
	if err := m.RestoreToAgent(s, a2); err != nil {
		t.Fatal(err)
	}

	orig := a1.State().Clone()
	restored := a2.State().Clone()

	if len(restored.Messages) != len(orig.Messages) {
		t.Fatalf("message count: orig %d, restored %d", len(orig.Messages), len(restored.Messages))
	}
	for i := range orig.Messages {
		om, rm := orig.Messages[i], restored.Messages[i]
		if om.Role != rm.Role {
			t.Fatalf("msg %d role: %v vs %v", i, om.Role, rm.Role)
		}
		if om.Content != rm.Content {
			t.Fatalf("msg %d content: %q vs %q", i, om.Content, rm.Content)
		}
		if om.ToolResult != rm.ToolResult {
			t.Fatalf("msg %d tool result: %q vs %q", i, om.ToolResult, rm.ToolResult)
		}
		if om.ToolCallID != rm.ToolCallID {
			t.Fatalf("msg %d tool call id: %q vs %q", i, om.ToolCallID, rm.ToolCallID)
		}
		if len(om.ToolCalls) != len(rm.ToolCalls) {
			t.Fatalf("msg %d tool call count: %d vs %d", i, len(om.ToolCalls), len(rm.ToolCalls))
		}
		for j := range om.ToolCalls {
			if om.ToolCalls[j].Name != rm.ToolCalls[j].Name {
				t.Fatalf("msg %d call %d name: %q vs %q", i, j, om.ToolCalls[j].Name, rm.ToolCalls[j].Name)
			}
			if string(om.ToolCalls[j].Arguments) != string(rm.ToolCalls[j].Arguments) {
				t.Fatalf("msg %d call %d args: %s vs %s", i, j, om.ToolCalls[j].Arguments, rm.ToolCalls[j].Arguments)
			}
			if om.ToolCalls[j].ToolCallID != rm.ToolCalls[j].ToolCallID {
				t.Fatalf("msg %d call %d id: %q vs %q", i, j, om.ToolCalls[j].ToolCallID, rm.ToolCalls[j].ToolCallID)
			}
		}
	}
	if restored.Status != agent.StatusIdle {
		t.Fatalf("restored status = %q, want idle", restored.Status)
	}

	// Session back to active after resume.
	loaded, _ := m.LoadSession(s.ID)
	if loaded.Status != StatusActive {
		t.Fatalf("session status after restore = %q, want active", loaded.Status)
	}
}

func TestRestoreToAgentThenContinueConversation(t *testing.T) {
	a1 := runToolConversation(t)
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")
	if err := m.SyncFromAgent(s, a1); err != nil {
		t.Fatal(err)
	}

	a2 := agent.NewAgent(agent.AgentConfig{Provider: "mock", Model: "m"})
	a2.Provider = newMockAgentProvider()
	a2.Executor.RegisterTool(
		agent.AgentTool{Name: "test_tool", Description: "test"},
		func(args map[string]any) (string, error) { return "tool output 42", nil },
	)
	if err := m.RestoreToAgent(s, a2); err != nil {
		t.Fatal(err)
	}

	// The restored agent keeps working: append another turn and re-sync.
	if err := a2.Prompt("and now this", nil); err != nil {
		t.Fatal(err)
	}
	a2.WaitForIdle()
	if err := m.SyncFromAgent(s, a2); err != nil {
		t.Fatal(err)
	}

	// Restored: user, assistant(call), tool, assistant(final). New turn:
	// user, assistant(call), tool, assistant(final).
	if len(s.Entries) != 8 {
		t.Fatalf("entries = %d, want 8: %+v", len(s.Entries), s.Entries)
	}
	if s.Entries[0].Message != "do the thing" {
		t.Fatalf("entry 0 = %+v (restored history lost?)", s.Entries[0])
	}
	if s.Entries[3].Role != RoleAssistant || s.Entries[3].Message != "all done" {
		t.Fatalf("entry 3 = %+v", s.Entries[3])
	}
	if s.Entries[4].Role != RoleUser || s.Entries[4].Message != "and now this" {
		t.Fatalf("entry 4 = %+v", s.Entries[4])
	}
	if s.Entries[5].ToolCallID == "" || s.Entries[5].ToolName != "test_tool" {
		t.Fatalf("entry 5 = %+v", s.Entries[5])
	}
	if s.Entries[6].Role != RoleTool || s.Entries[6].Message != "tool output 42" {
		t.Fatalf("entry 6 = %+v", s.Entries[6])
	}
	if s.Entries[7].Role != RoleAssistant || s.Entries[7].Message != "all done" {
		t.Fatalf("entry 7 = %+v", s.Entries[7])
	}
}

func TestRestoreToAgentRejectsRunningAgent(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")
	s.AppendEntry(NewEntry(RoleUser, "hi"))

	a := agent.NewAgent(agent.AgentConfig{Provider: "mock", Model: "m"})
	bp := newBlockingAgentProvider()
	a.Provider = bp
	if err := a.Prompt("running", nil); err != nil {
		t.Fatal(err)
	}
	defer func() { a.Abort(); a.WaitForIdle() }()

	<-bp.Entered
	if err := m.RestoreToAgent(s, a); err == nil {
		t.Fatal("restore into a running agent should fail")
	}
}

func TestRestoreEmptySession(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")
	a := agent.NewAgent(agent.AgentConfig{Provider: "mock", Model: "m"})
	if err := m.RestoreToAgent(s, a); err != nil {
		t.Fatal(err)
	}
	if len(a.State().Messages) != 0 {
		t.Fatalf("messages = %d, want 0", len(a.State().Messages))
	}
}

// ── mock providers (local copies so session tests don't reach into the
// agent package's test files) ────────────────────────────────────────────

type mockAgentProvider struct {
	calls int
}

func newMockAgentProvider() *mockAgentProvider { return &mockAgentProvider{} }

func (m *mockAgentProvider) Stream(ctx context.Context, messages []agent.Message, tools []agent.AgentTool, config agent.AgentConfig) (<-chan agent.Event, error) {
	m.calls++
	out := make(chan agent.Event, 8)
	go func() {
		defer close(out)
		if m.calls == 1 {
			out <- agent.Event{Type: agent.EventToolCall, Tool: &agent.ToolUpdate{
				Name:  "test_tool",
				Input: json.RawMessage(`{"key":"value"}`),
			}}
			out <- agent.Event{Type: agent.EventStop}
			return
		}
		out <- agent.Event{Type: agent.EventContentDelta, Content: "all done"}
		out <- agent.Event{Type: agent.EventStop}
	}()
	return out, nil
}

func (m *mockAgentProvider) Close() error { return nil }

type blockingAgentProvider struct {
	Entered chan struct{}
}

func newBlockingAgentProvider() *blockingAgentProvider {
	return &blockingAgentProvider{Entered: make(chan struct{})}
}

func (b *blockingAgentProvider) Stream(ctx context.Context, _ []agent.Message, _ []agent.AgentTool, _ agent.AgentConfig) (<-chan agent.Event, error) {
	out := make(chan agent.Event)
	close(b.Entered)
	<-ctx.Done()
	close(out)
	return out, nil
}

func (b *blockingAgentProvider) Close() error { return nil }
