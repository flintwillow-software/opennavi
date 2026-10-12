package navi

import (
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/memory"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// liveNavi builds a navi whose own object surface is wired to its queue and
// agent fanout, so Watch sees the translated deltas. The surface acts as a
// permissive test operator.
func liveNavi(t *testing.T) *Navi {
	t.Helper()
	return New("deltas", WithPlanCloseCheck(false), WithObjectAuthorizer(portal.AllowAll(), principal.MustParse("service:test")))
}

func TestDeltaWindowCursorAndGap(t *testing.T) {
	m := object.NewDeltaWindow(2)
	for _, id := range []string{"a", "b", "c"} {
		m.Push(object.Delta{Type: TypeThought, ID: id, Op: object.OpUpdate})
	}

	// The window holds the two newest; a cursor at 0 is behind it.
	deltas, next, lost := m.ReadAfter(0)
	if !lost {
		t.Fatal("ReadAfter(0) after a drop must report lost")
	}
	if len(deltas) != 2 || deltas[0].ID != "b" || deltas[1].ID != "c" {
		t.Fatalf("ReadAfter(0) = %+v, want the two newest (b, c)", deltas)
	}
	if next != 4 {
		t.Fatalf("next cursor = %d, want 4", next)
	}

	// Reading from the live cursor yields nothing new and no gap.
	deltas, next, lost = m.ReadAfter(next)
	if len(deltas) != 0 || lost || next != 4 {
		t.Fatalf("ReadAfter(4) = %+v next=%d lost=%v, want empty/4/false", deltas, next, lost)
	}

	// A cursor past everything is not a gap: nothing was dropped.
	if _, next, lost := m.ReadAfter(99); lost || next != 99 {
		t.Fatalf("ReadAfter(99) = next=%d lost=%v, want 99/false", next, lost)
	}

	// An in-window cursor resumes without a gap.
	m.Push(object.Delta{Type: TypeThought, ID: "d", Op: object.OpUpdate})
	deltas, next, lost = m.ReadAfter(4)
	if lost || len(deltas) != 1 || deltas[0].ID != "d" || next != 5 {
		t.Fatalf("ReadAfter(4) = %+v next=%d lost=%v, want [d]/5/false", deltas, next, lost)
	}

	// Notify is a push signal: a reader that captures the channel before
	// reading is woken by the next push, and no wakeup is lost.
	wake := m.Notify()
	select {
	case <-wake:
		t.Fatal("Notify fired before a push")
	default:
	}
	m.Push(object.Delta{Type: TypeThought, ID: "e", Op: object.OpUpdate})
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("Push did not wake a captured Notify channel")
	}
}

func TestWatchTranslatesQueueLifecycle(t *testing.T) {
	n := liveNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())

	deltas, cursor, _, _ := n.objects.Watch(0)
	if len(deltas) != 1 {
		t.Fatalf("enqueue produced %d deltas, want 1: %+v", len(deltas), deltas)
	}
	if deltas[0].Type != TypeThought || deltas[0].ID != "t1" || deltas[0].Op != object.OpUpdate {
		t.Fatalf("enqueue delta = %+v, want thought t1 update", deltas[0])
	}
	if deltas[0].Fields["status"] != string(thought.ThoughtPending) {
		t.Fatalf("enqueue delta fields = %v, want status pending", deltas[0].Fields)
	}
	if _, hidden := deltas[0].Fields["checkpoint"]; hidden {
		t.Fatalf("delta leaked a sensitive field: %v", deltas[0].Fields)
	}

	if err := n.Thoughts.Cancel("t1"); err != nil {
		t.Fatal(err)
	}
	deltas, cursor, _, _ = n.objects.Watch(cursor)
	if len(deltas) != 1 || deltas[0].Op != object.OpUpdate {
		t.Fatalf("cancel deltas = %+v, want one update", deltas)
	}
	if deltas[0].Fields["status"] != string(thought.ThoughtCancelled) {
		t.Fatalf("cancel delta fields = %v, want status cancelled", deltas[0].Fields)
	}

	// Deletion is its own op. A terminal record is protected from deletion, so
	// delete a fresh pending thought and check the last delta.
	enqueueThought(t, n, "t2", time.Now().UTC())
	if err := n.Thoughts.Delete("t2"); err != nil {
		t.Fatal(err)
	}
	deltas, _, _, _ = n.objects.Watch(cursor)
	last := deltas[len(deltas)-1]
	if last.Op != object.OpDelete || last.ID != "t2" {
		t.Fatalf("delete deltas = %+v, want a delete for t2", deltas)
	}
}

func TestWatchTranslatesAgentTranscript(t *testing.T) {
	n := liveNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())
	_, cursor, _, _ := n.objects.Watch(0)

	n.objects.observeAgent("t1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Content: "hello "},
	})
	n.objects.observeAgent("t1", agent.AgentEvent{
		Type: agent.AgentEventToolCall,
		Tool: &agent.ToolUpdate{ToolCallID: "c1", Name: "todo_write", Input: []byte(`{"text":"x"}`)},
	})
	n.objects.observeAgent("t1", agent.AgentEvent{
		Type: agent.AgentEventToolResult,
		Tool: &agent.ToolUpdate{ToolCallID: "c1", Name: "todo_write", Result: "added"},
	})
	// Lifecycle events are not transcript content.
	n.objects.observeAgent("t1", agent.AgentEvent{Type: agent.AgentEventTurnStart})

	deltas, _, _, _ := n.objects.Watch(cursor)
	if len(deltas) != 3 {
		t.Fatalf("agent deltas = %d, want 3 (delta, call, result): %+v", len(deltas), deltas)
	}
	for i, d := range deltas {
		if d.Type != TypeThought || d.ID != "t1" || d.Op != object.OpAppend || d.Detail != DetailTranscript {
			t.Fatalf("delta %d = %+v, want thought t1 append on transcript", i, d)
		}
	}
	if deltas[0].Fields["role"] != "assistant" || deltas[0].Fields["message"] != "hello " {
		t.Fatalf("content delta = %v, want an assistant fragment", deltas[0].Fields)
	}
	if deltas[1].Fields["tool_name"] != "todo_write" || deltas[1].Fields["tool_input"] != `{"text":"x"}` {
		t.Fatalf("tool call delta = %v, want the tool input", deltas[1].Fields)
	}
	if deltas[2].Fields["role"] != "tool" || deltas[2].Fields["message"] != "added" {
		t.Fatalf("tool result delta = %v, want the tool output", deltas[2].Fields)
	}
}

func TestWatchPostActionEmitsTheNewState(t *testing.T) {
	n := liveNavi(t)
	enqueueThought(t, n, "t1", time.Now().UTC())
	if _, ok := n.Thoughts.Dequeue(); !ok {
		t.Fatal("could not claim t1")
	}
	_, cursor, _, _ := n.objects.Watch(0)

	if err := n.objects.Act(TypeThought, "t1", "pause", nil); err != nil {
		t.Fatalf("pause: %v", err)
	}
	deltas, _, _, _ := n.objects.Watch(cursor)
	if len(deltas) == 0 {
		t.Fatal("pause produced no delta")
	}
	last := deltas[len(deltas)-1]
	if last.Op != object.OpUpdate || last.ID != "t1" || last.Fields["status"] != string(thought.ThoughtPaused) {
		t.Fatalf("last delta = %+v, want a paused update", last)
	}
}

func TestWatchPostRemoveEmitsDelete(t *testing.T) {
	_, s, state := toolStateNavi(t)
	todos, err := state.ReplaceTodos("navi-a", []datastore.Todo{{Text: "ship it"}})
	if err != nil {
		t.Fatal(err)
	}
	id := todos[0].ID

	if err := s.Act(TypeTodo, id, "remove", nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	deltas, _, _, _ := s.Watch(0)
	var found bool
	for _, d := range deltas {
		if d.Type == TypeTodo && d.ID == id && d.Op == object.OpDelete {
			found = true
		}
	}
	if !found {
		t.Fatalf("remove deltas = %+v, want a todo delete for %s", deltas, id)
	}
}

func TestMemoryChangeProjectsToDeltas(t *testing.T) {
	n := liveNavi(t)
	rec := datastore.MemoryRecord{
		ID: "m1", Scope: datastore.ScopeUser, Subject: principal.MustParse("user:deltas"),
		Content: "prefers terse answers", Importance: 3,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	n.memoryChanged(rec, memory.ChangeCreated)
	n.memoryChanged(rec, memory.ChangeUpdated)
	n.memoryChanged(rec, memory.ChangeRemoved)

	deltas, _, _, _ := n.objects.Watch(0)
	if len(deltas) != 3 {
		t.Fatalf("memory deltas = %d, want 3: %+v", len(deltas), deltas)
	}
	if deltas[0].Op != object.OpCreate || deltas[1].Op != object.OpUpdate || deltas[2].Op != object.OpDelete {
		t.Fatalf("memory ops = %s/%s/%s, want create/update/delete", deltas[0].Op, deltas[1].Op, deltas[2].Op)
	}
	if deltas[0].Fields["content"] != "prefers terse answers" {
		t.Fatalf("memory create fields = %v, want the record content", deltas[0].Fields)
	}
	if len(deltas[2].Fields) != 0 {
		t.Fatalf("memory delete fields = %v, want none", deltas[2].Fields)
	}
}
