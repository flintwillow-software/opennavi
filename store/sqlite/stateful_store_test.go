package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/store"
)

func newTestStatefulStore(t *testing.T) (*StatefulStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stateful.db")
	st, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss, err := NewStatefulStore(st.DB())
	if err != nil {
		t.Fatalf("NewStatefulStore: %v", err)
	}
	return ss, path
}

// reopenStatefulStore reopens the same database file — the
// "state survives restart" scenario (S130500 §07).
func reopenStatefulStore(t *testing.T, path string) *StatefulStore {
	t.Helper()
	st, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("reopen Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss, err := NewStatefulStore(st.DB())
	if err != nil {
		t.Fatalf("reopen NewStatefulStore: %v", err)
	}
	return ss
}

func TestStatefulStoreRestartPersistence(t *testing.T) {
	ss, path := newTestStatefulStore(t)

	written, err := ss.ReplaceTodos("navi-a", []store.Todo{
		{Text: "write the report", Status: store.TodoInProgress, Priority: 1},
		{Text: "file the PR", Status: store.TodoPending, Priority: 2},
	})
	if err != nil {
		t.Fatalf("ReplaceTodos: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("expected 2 persisted todos, got %d", len(written))
	}
	for _, td := range written {
		if td.ID == "" {
			t.Fatal("ReplaceTodos returned item without id")
		}
	}
	note, err := ss.UpsertNote("navi-a", store.Note{Title: "scratch", Body: "verbatim body", Tags: []string{"ideas"}})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}

	reopened := reopenStatefulStore(t, path)

	// Todos survive with ids, status, priority, timestamps.
	todos, err := reopened.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos after restart: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("expected 2 todos after restart, got %d", len(todos))
	}
	if todos[0].Text != "write the report" || todos[0].Status != store.TodoInProgress || todos[0].Priority != 1 {
		t.Fatalf("todo round-trip mismatch: %+v", todos[0])
	}
	if todos[0].ID != written[0].ID {
		t.Fatalf("todo id changed across restart: %s -> %s", written[0].ID, todos[0].ID)
	}
	if todos[0].CreatedAt.IsZero() || todos[0].UpdatedAt.IsZero() {
		t.Fatal("todo timestamps lost across restart")
	}
	// Notes survive verbatim with tags.
	notes, err := reopened.ReadNotes("navi-a", store.NoteQuery{ID: note.ID})
	if err != nil {
		t.Fatalf("ReadNotes after restart: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note after restart, got %d", len(notes))
	}
	if notes[0].Title != "scratch" || notes[0].Body != "verbatim body" || len(notes[0].Tags) != 1 || notes[0].Tags[0] != "ideas" {
		t.Fatalf("note round-trip mismatch: %+v", notes[0])
	}
}

func TestStatefulStorePerNaviScoping(t *testing.T) {
	ss, _ := newTestStatefulStore(t)

	// navi-a writes todos and notes; navi-b must not see them.
	if _, err := ss.ReplaceTodos("navi-a", []store.Todo{{Text: "a's plan", Status: store.TodoPending}}); err != nil {
		t.Fatalf("ReplaceTodos(navi-a): %v", err)
	}
	if _, err := ss.UpsertNote("navi-a", store.Note{Title: "a's note", Body: "secret"}); err != nil {
		t.Fatalf("UpsertNote(navi-a): %v", err)
	}

	todosB, err := ss.ListTodos("navi-b", "", 0)
	if err != nil {
		t.Fatalf("ListTodos(navi-b): %v", err)
	}
	if len(todosB) != 0 {
		t.Fatalf("navi-b sees navi-a todos: %+v", todosB)
	}
	notesB, err := ss.ReadNotes("navi-b", store.NoteQuery{Query: "secret"})
	if err != nil {
		t.Fatalf("ReadNotes(navi-b): %v", err)
	}
	if len(notesB) != 0 {
		t.Fatalf("navi-b sees navi-a notes: %+v", notesB)
	}

	// Same id under a different navi is a different row.
	if _, err := ss.UpsertNote("navi-b", store.Note{ID: "shared-id", Title: "b's note", Body: "mine"}); err != nil {
		t.Fatalf("UpsertNote(navi-b): %v", err)
	}
	notesA, err := ss.ReadNotes("navi-a", store.NoteQuery{ID: "shared-id"})
	if err != nil {
		t.Fatalf("ReadNotes(navi-a, shared-id): %v", err)
	}
	if len(notesA) != 0 {
		t.Fatalf("navi-a saw navi-b's note via same id: %+v", notesA)
	}

	// navi-b removing its own note does not touch navi-a's state.
	if _, err := ss.UpsertNote("navi-b", store.Note{ID: "b-only", Title: "tmp", Body: "x"}); err != nil {
		t.Fatalf("UpsertNote(navi-b, b-only): %v", err)
	}
	if err := ss.RemoveNote("navi-b", "b-only"); err != nil {
		t.Fatalf("RemoveNote(navi-b): %v", err)
	}
	// navi-a's full-replace does not clobber navi-b's list.
	if _, err := ss.ReplaceTodos("navi-b", []store.Todo{{Text: "b's plan", Status: store.TodoPending}}); err != nil {
		t.Fatalf("ReplaceTodos(navi-b): %v", err)
	}
	todosA, err := ss.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos(navi-a): %v", err)
	}
	if len(todosA) != 1 || todosA[0].Text != "a's plan" {
		t.Fatalf("navi-a todos clobbered by navi-b write: %+v", todosA)
	}
}

func TestStatefulStoreReplaceAndListOrder(t *testing.T) {
	ss, _ := newTestStatefulStore(t)

	written, err := ss.ReplaceTodos("navi-a", []store.Todo{
		{Text: "second", Status: store.TodoPending, Priority: 5},
		{Text: "first", Status: store.TodoInProgress, Priority: 0},
	})
	if err != nil {
		t.Fatalf("ReplaceTodos: %v", err)
	}
	if written[0].Status != store.TodoPending {
		t.Fatalf("status default lost: %+v", written[0])
	}

	// Ordered by priority, then most recent activity.
	todos, err := ss.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 2 || todos[0].Text != "first" || todos[1].Text != "second" {
		t.Fatalf("ordering wrong: %+v", todos)
	}
	// Filtered by status.
	inProgress, err := ss.ListTodos("navi-a", store.TodoInProgress, 0)
	if err != nil {
		t.Fatalf("ListTodos(in_progress): %v", err)
	}
	if len(inProgress) != 1 || inProgress[0].Text != "first" {
		t.Fatalf("status filter wrong: %+v", inProgress)
	}
	// Full replace clears the previous list.
	if _, err := ss.ReplaceTodos("navi-a", []store.Todo{{Text: "new plan", Status: store.TodoDone}}); err != nil {
		t.Fatalf("ReplaceTodos #2: %v", err)
	}
	todos, err = ss.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos after replace: %v", err)
	}
	if len(todos) != 1 || todos[0].Text != "new plan" {
		t.Fatalf("replace did not clear old list: %+v", todos)
	}
}

func TestStatefulStoreUpdateTodosAtomicity(t *testing.T) {
	ss, _ := newTestStatefulStore(t)
	written, err := ss.ReplaceTodos("navi-a", []store.Todo{
		{Text: "one", Status: store.TodoPending},
		{Text: "two", Status: store.TodoPending},
	})
	if err != nil {
		t.Fatalf("ReplaceTodos: %v", err)
	}
	done := store.TodoDone
	newText := "renamed"
	prio := 7
	updated, err := ss.UpdateTodos("navi-a", []store.TodoUpdate{
		{ID: written[0].ID, Status: &done, Text: &newText, Priority: &prio},
	})
	if err != nil {
		t.Fatalf("UpdateTodos: %v", err)
	}
	found := false
	for _, td := range updated {
		if td.ID == written[0].ID {
			found = true
			if td.Status != store.TodoDone || td.Text != "renamed" || td.Priority != 7 {
				t.Fatalf("partial update mismatch: %+v", td)
			}
		}
	}
	if !found {
		t.Fatal("updated item missing from result")
	}

	// Unknown id rejects the whole batch; prior state intact.
	_, err = ss.UpdateTodos("navi-a", []store.TodoUpdate{
		{ID: written[1].ID, Text: strPtr("should not land")},
		{ID: "no-such-id", Status: &done},
	})
	if err == nil || !errors.Is(err, store.ErrTodoNotFound) {
		t.Fatalf("unknown id err = %v, want ErrTodoNotFound", err)
	}
	todos, err := ss.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos after failed batch: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("failed batch changed list length: %d", len(todos))
	}
	if todos[0].Text != "two" {
		t.Fatalf("failed batch mutated item: %+v", todos[0])
	}

	// Remove path.
	if _, err := ss.UpdateTodos("navi-a", []store.TodoUpdate{{ID: written[1].ID, Remove: true}}); err != nil {
		t.Fatalf("remove update: %v", err)
	}
	todos, _ = ss.ListTodos("navi-a", "", 0)
	if len(todos) != 1 {
		t.Fatalf("remove did not delete: %d items", len(todos))
	}
}

func TestStatefulStoreNotesSearchAndRemove(t *testing.T) {
	ss, _ := newTestStatefulStore(t)

	n1, err := ss.UpsertNote("navi-a", store.Note{Title: "deploy checklist", Body: "run the tests", Tags: []string{"ops"}})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}
	if _, err := ss.UpsertNote("navi-a", store.Note{Title: "grocery list", Body: "milk, eggs", Tags: []string{"home"}}); err != nil {
		t.Fatalf("UpsertNote #2: %v", err)
	}

	// By id.
	got, err := ss.ReadNotes("navi-a", store.NoteQuery{ID: n1.ID})
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadNotes(id): %v notes=%d", err, len(got))
	}
	// By tag.
	byTag, err := ss.ReadNotes("navi-a", store.NoteQuery{Tag: "ops"})
	if err != nil || len(byTag) != 1 || byTag[0].ID != n1.ID {
		t.Fatalf("ReadNotes(tag): %v notes=%+v", err, byTag)
	}
	// Literal substring on title/body — no semantic matching.
	byQuery, err := ss.ReadNotes("navi-a", store.NoteQuery{Query: "milk"})
	if err != nil || len(byQuery) != 1 {
		t.Fatalf("ReadNotes(query): %v notes=%d", err, len(byQuery))
	}
	// Literal match does not include the query as substring of a
	// different word.
	noMatch, err := ss.ReadNotes("navi-a", store.NoteQuery{Query: "mic"})
	if err != nil || len(noMatch) != 0 {
		t.Fatalf("ReadNotes(nomatch): %v notes=%d", err, len(noMatch))
	}
	// Update preserves created_at, bumps updated_at.
	updated, err := ss.UpsertNote("navi-a", store.Note{ID: n1.ID, Title: "deploy checklist v2", Body: "run the tests twice"})
	if err != nil {
		t.Fatalf("UpsertNote(update): %v", err)
	}
	if !updated.CreatedAt.Equal(n1.CreatedAt) {
		t.Fatalf("created_at changed on update: %v -> %v", n1.CreatedAt, updated.CreatedAt)
	}
	// Remove, then not-found.
	if err := ss.RemoveNote("navi-a", n1.ID); err != nil {
		t.Fatalf("RemoveNote: %v", err)
	}
	if err := ss.RemoveNote("navi-a", n1.ID); err == nil || !errors.Is(err, store.ErrNoteNotFound) {
		t.Fatalf("RemoveNote(again) err = %v, want ErrNoteNotFound", err)
	}
}

func TestStatefulStoreUpsertUnknownIDBehavesAsCreate(t *testing.T) {
	ss, _ := newTestStatefulStore(t)
	// Upsert with an explicit id that does not exist yet creates the
	// note (upsert semantics), preserving the caller-provided id.
	n, err := ss.UpsertNote("navi-a", store.Note{ID: "explicit", Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("UpsertNote(explicit): %v", err)
	}
	if n.ID != "explicit" {
		t.Fatalf("explicit id lost: %s", n.ID)
	}
	notes, _ := ss.ReadNotes("navi-a", store.NoteQuery{ID: "explicit"})
	if len(notes) != 1 {
		t.Fatalf("explicit-id note not found: %d", len(notes))
	}
}

func strPtr(s string) *string { return &s }

// ensure strPtr is used (kept for future update-shape tests).
var _ = strPtr

func TestStatefulStoreTimestampsProgress(t *testing.T) {
	ss, _ := newTestStatefulStore(t)
	n1, err := ss.UpsertNote("navi-a", store.Note{Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	n2, err := ss.UpsertNote("navi-a", store.Note{ID: n1.ID, Title: "t2", Body: "b2"})
	if err != nil {
		t.Fatalf("UpsertNote #2: %v", err)
	}
	if !n2.UpdatedAt.After(n1.UpdatedAt) {
		t.Fatalf("updated_at did not advance: %v -> %v", n1.UpdatedAt, n2.UpdatedAt)
	}
	if !n2.CreatedAt.Equal(n1.CreatedAt) {
		t.Fatalf("created_at changed: %v -> %v", n1.CreatedAt, n2.CreatedAt)
	}
}
