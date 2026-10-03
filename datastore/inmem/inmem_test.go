package inmem

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// The in-memory backend is the reference implementation the datastore
// contract is tested against: every aggregate must round-trip through the
// views, and nothing in these tests imports navi or internal.

func TestCandidateAndViews(t *testing.T) {
	c := Candidate()
	if c.Name != "inmem" || !c.Default || c.Open == nil {
		t.Fatalf("candidate = %+v", c)
	}
	backend, err := c.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	var _ datastore.Backend = backend
	if backend.Thoughts() == nil || backend.ToolState() == nil || backend.Memory() == nil {
		t.Fatal("every aggregate must expose a view")
	}
}

func TestHandleBindsAndClaims(t *testing.T) {
	h := datastore.NewDatastore()
	if h.Bound() || h.Claimed() {
		t.Fatal("a fresh handle is unbound and unclaimed")
	}
	if _, err := h.Thoughts().LoadTranscript("t1"); !errors.Is(err, datastore.ErrUnbound) {
		t.Fatalf("an unbound view must fail with ErrUnbound, got %v", err)
	}
	if !h.Claimed() {
		t.Fatal("requesting a view claims the handle")
	}
	h.Bind(New())
	if !h.Bound() {
		t.Fatal("Bind must mark the handle bound")
	}
	if _, err := h.Thoughts().LoadTranscript("t1"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
		t.Fatalf("a bound view must serve the backend, got %v", err)
	}
}

func TestThoughtAggregateRoundTrips(t *testing.T) {
	v := New().Thoughts()

	th := &thought.Thought{ID: "t1", Prompt: "hello", Status: thought.ThoughtPending}
	if err := v.SaveThought(th); err != nil {
		t.Fatal(err)
	}
	loaded, err := v.LoadThought("t1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Prompt != "hello" {
		t.Fatalf("thought round-trip: %+v", loaded)
	}
	all, err := v.LoadThoughts()
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadThoughts = %v, %v", all, err)
	}
	if err := v.DeleteThought("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadThought("t1"); !errors.Is(err, thought.ErrThoughtNotFound) {
		t.Fatalf("deleted thought: %v", err)
	}
}

func TestTranscriptAggregateRoundTrips(t *testing.T) {
	v := New().Thoughts()

	entries := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "say pong"),
		datastore.NewEntry(datastore.RoleAssistant, "pong"),
	}
	entries[0].ToolCallID = "call-1"
	entries[0].ToolName = "echo"
	entries[0].ToolInput = json.RawMessage(`{"x":"hi"}`)

	if err := v.SaveTranscript("t1", entries); err != nil {
		t.Fatal(err)
	}

	// A caller cannot mutate stored state through the slice it wrote.
	entries[0].Message = "mutated"

	got, err := v.LoadTranscript("t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Message != "say pong" || got[1].Message != "pong" {
		t.Fatalf("transcript round-trip: %+v", got)
	}
	if got[0].ToolCallID != "call-1" || got[0].ToolName != "echo" || string(got[0].ToolInput) != `{"x":"hi"}` {
		t.Fatalf("tool metadata lost: %+v", got[0])
	}

	info, err := v.TranscriptInfo("t1")
	if err != nil {
		t.Fatal(err)
	}
	if info.ThoughtID != "t1" || info.MessageCount != 2 {
		t.Fatalf("TranscriptInfo = %+v", info)
	}

	if err := v.DeleteTranscript("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadTranscript("t1"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
		t.Fatalf("deleted transcript: %v", err)
	}
	if _, err := v.TranscriptInfo("missing"); !errors.Is(err, datastore.ErrTranscriptNotFound) {
		t.Fatalf("missing transcript info: %v", err)
	}
}

// TestConversationReadableThroughTheContractAlone is the acceptance test:
// a completed thought's conversation is read back using only the public
// datastore types — no navi, no internal package.
func TestConversationReadableThroughTheContractAlone(t *testing.T) {
	var backend datastore.Backend = New() // as a harness would receive it

	conversation := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "what is 2+2?"),
		datastore.NewEntry(datastore.RoleAssistant, "4"),
	}
	if err := backend.Thoughts().SaveTranscript("thought-1", conversation); err != nil {
		t.Fatal(err)
	}

	read, err := backend.Thoughts().LoadTranscript("thought-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != len(conversation) {
		t.Fatalf("read %d entries, want %d", len(read), len(conversation))
	}
	for i := range conversation {
		if read[i].Role != conversation[i].Role || read[i].Message != conversation[i].Message {
			t.Fatalf("entry %d = %+v, want %+v", i, read[i], conversation[i])
		}
	}
}

func TestToolStateAggregateRoundTrips(t *testing.T) {
	v := New().ToolState()

	// Todos: replace, list, partial update, remove.
	stored, err := v.ReplaceTodos("navi-a", []datastore.Todo{
		{Text: "one", Priority: 2},
		{Text: "two", Priority: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].ID == "" || stored[0].Status != datastore.TodoPending {
		t.Fatalf("ReplaceTodos = %+v", stored)
	}
	listed, err := v.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Text != "two" {
		t.Fatalf("ListTodos order = %+v", listed)
	}

	text := "renamed"
	done := datastore.TodoDone
	updated, err := v.UpdateTodos("navi-a", []datastore.TodoUpdate{
		{ID: stored[0].ID, Text: &text, Status: &done},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range updated {
		if u.ID == stored[0].ID {
			found = true
			if u.Text != "renamed" || u.Status != datastore.TodoDone {
				t.Fatalf("update = %+v", u)
			}
		}
	}
	if !found {
		t.Fatalf("updated todo missing from %+v", updated)
	}

	if _, err := v.UpdateTodos("navi-a", []datastore.TodoUpdate{{ID: "nope"}}); !errors.Is(err, datastore.ErrTodoNotFound) {
		t.Fatalf("unknown todo: %v", err)
	}
	if _, err := v.ReplaceTodos("navi-a", []datastore.Todo{{Text: "bad", Status: "bogus"}}); err == nil {
		t.Fatal("an invalid status must be rejected")
	}

	// Notes: upsert, read by id/tag/query, remove.
	note, err := v.UpsertNote("navi-a", datastore.Note{Title: "deploy", Body: "how to deploy", Tags: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	if note.ID == "" || note.CreatedAt.IsZero() {
		t.Fatalf("UpsertNote = %+v", note)
	}
	byID, err := v.ReadNotes("navi-a", datastore.NoteQuery{ID: note.ID})
	if err != nil || len(byID) != 1 {
		t.Fatalf("ReadNotes by id = %+v, %v", byID, err)
	}
	byTag, err := v.ReadNotes("navi-a", datastore.NoteQuery{Tag: "ops"})
	if err != nil || len(byTag) != 1 {
		t.Fatalf("ReadNotes by tag = %+v, %v", byTag, err)
	}
	byQuery, err := v.ReadNotes("navi-a", datastore.NoteQuery{Query: "deploy"})
	if err != nil || len(byQuery) != 1 {
		t.Fatalf("ReadNotes by query = %+v, %v", byQuery, err)
	}
	if err := v.RemoveNote("navi-a", note.ID); err != nil {
		t.Fatal(err)
	}
	if err := v.RemoveNote("navi-a", note.ID); !errors.Is(err, datastore.ErrNoteNotFound) {
		t.Fatalf("removed note: %v", err)
	}

	// Per-navi scoping: another navi sees nothing.
	other, err := v.ListTodos("navi-b", "", 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("navi-b must not see navi-a state: %+v, %v", other, err)
	}
}

func TestMemoryAggregateRoundTrips(t *testing.T) {
	v := New().Memory()

	rec, err := v.Remember(datastore.MemoryRecord{
		Scope:      datastore.ScopeMemories,
		Subject:    "navi",
		Content:    "deploys happen on fridays",
		Tags:       []string{"ops"},
		Importance: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" || rec.CreatedAt.IsZero() {
		t.Fatalf("Remember = %+v", rec)
	}

	got, err := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeMemories, Query: "fridays", Tags: []string{"ops"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("Recall = %+v, %v", got, err)
	}
	if none, _ := v.Recall(datastore.RecallQuery{Scope: datastore.ScopeUser}); len(none) != 0 {
		t.Fatalf("scope filter leaked: %+v", none)
	}

	if err := v.Forget(rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := v.Forget(rec.ID); !errors.Is(err, datastore.ErrMemoryNotFound) {
		t.Fatalf("forgotten memory: %v", err)
	}
}

func TestExpiryIsCarriedNotInterpreted(t *testing.T) {
	v := New().Memory()
	expires := time.Now().Add(time.Hour)
	rec, err := v.Remember(datastore.MemoryRecord{Content: "x", ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Recall(datastore.RecallQuery{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Recall = %+v, %v", got, err)
	}
	if !got[0].ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt = %v, want %v", got[0].ExpiresAt, expires)
	}
	_ = rec
}
