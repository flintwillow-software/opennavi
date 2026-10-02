package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
)

// statefulDef builds a complete, valid descriptor for a stateful tool.
func statefulDef(name string) ToolDefinition {
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object"}`))
	return ToolDefinition{
		Name:          name,
		Description:   "Test stateful tool " + name + ".",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "retry_once",
		Mode:          ToolModeSequential,
		Tags:          []string{"test"},
		PromptSnippet: "Test stateful tool (" + name + ")",
		SourceInfo:    SourceInfo{Origin: OriginSDK},
	}
}

// memStatefulStore is a minimal in-memory StatefulToolStore for
// registry-level tests; the real SQLite implementation and its
// restart/scoping tests live in store/sqlite.
type memStatefulStore struct {
	todos map[string][]datastore.Todo
	notes map[string][]datastore.Note
	seq   int
}

func newMemStatefulStore() *memStatefulStore {
	return &memStatefulStore{
		todos: map[string][]datastore.Todo{},
		notes: map[string][]datastore.Note{},
	}
}

func (m *memStatefulStore) ListTodos(naviID string, status datastore.TodoStatus, limit int) ([]datastore.Todo, error) {
	var out []datastore.Todo
	for _, t := range m.todos[naviID] {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStatefulStore) ReplaceTodos(naviID string, items []datastore.Todo) ([]datastore.Todo, error) {
	out := make([]datastore.Todo, 0, len(items))
	for _, item := range items {
		m.seq++
		item.ID = "t-" + strconv.Itoa(m.seq)
		item.NaviID = naviID
		out = append(out, item)
	}
	m.todos[naviID] = out
	return out, nil
}

func (m *memStatefulStore) UpdateTodos(naviID string, updates []datastore.TodoUpdate) ([]datastore.Todo, error) {
	list := m.todos[naviID]
	for _, u := range updates {
		idx := -1
		for i, t := range list {
			if t.ID == u.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("%w: %s", datastore.ErrTodoNotFound, u.ID)
		}
		if u.Remove {
			list = append(list[:idx], list[idx+1:]...)
			continue
		}
		if u.Text != nil {
			list[idx].Text = *u.Text
		}
		if u.Status != nil {
			list[idx].Status = *u.Status
		}
		if u.Priority != nil {
			list[idx].Priority = *u.Priority
		}
	}
	m.todos[naviID] = list
	return append([]datastore.Todo(nil), list...), nil
}

func (m *memStatefulStore) ReadNotes(naviID string, q datastore.NoteQuery) ([]datastore.Note, error) {
	var out []datastore.Note
	for _, n := range m.notes[naviID] {
		if q.ID != "" && n.ID != q.ID {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

func (m *memStatefulStore) UpsertNote(naviID string, n datastore.Note) (datastore.Note, error) {
	m.seq++
	if n.ID == "" {
		n.ID = "n-" + strconv.Itoa(m.seq)
	}
	n.NaviID = naviID
	m.notes[naviID] = append(m.notes[naviID], n)
	return n, nil
}

func (m *memStatefulStore) RemoveNote(naviID, id string) error {
	list := m.notes[naviID]
	for i, n := range list {
		if n.ID == id {
			m.notes[naviID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", datastore.ErrNoteNotFound, id)
}

func TestRegisterStatefulBindsStoreHandle(t *testing.T) {
	r := New()
	st := newMemStatefulStore()

	var seen StateContext
	handler := func(ctx context.Context, args map[string]any, state StateContext) (string, error) {
		seen = state
		return `{"ok":true}`, nil
	}
	if err := r.RegisterStateful(statefulDef("todo_read"), "navi-a", st, handler); err != nil {
		t.Fatalf("RegisterStateful: %v", err)
	}
	if err := r.SetActive([]string{"todo_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	out, err := r.Dispatch(context.Background(), "todo_read", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("dispatch output = %q", out)
	}
	if seen.NaviID != "navi-a" {
		t.Fatalf("StateContext.NaviID = %q, want navi-a", seen.NaviID)
	}
	if seen.Store == nil {
		t.Fatal("StateContext.Store is nil")
	}
}

func TestRegisterStatefulValidation(t *testing.T) {
	r := New()
	handler := func(ctx context.Context, args map[string]any, state StateContext) (string, error) { return "", nil }
	st := newMemStatefulStore()

	if err := r.RegisterStateful(statefulDef("todo_write"), "", st, handler); err == nil || !strings.Contains(err.Error(), "navi identity is required") {
		t.Fatalf("empty naviID err = %v, want navi identity required", err)
	}
	if err := r.RegisterStateful(statefulDef("todo_write"), "navi-a", nil, handler); err == nil || !strings.Contains(err.Error(), "store handle is required") {
		t.Fatalf("nil store err = %v, want store handle required", err)
	}
	if err := r.RegisterStateful(statefulDef("todo_write"), "navi-a", st, nil); err == nil || !strings.Contains(err.Error(), "handler is required") {
		t.Fatalf("nil handler err = %v, want handler required", err)
	}
	// Incomplete descriptor still rejected.
	bad := statefulDef("todo_write")
	bad.Access = AccessAny
	if err := r.RegisterStateful(bad, "navi-a", st, handler); err == nil || !strings.Contains(err.Error(), "incomplete metadata") {
		t.Fatalf("incomplete def err = %v, want incomplete metadata", err)
	}
	// Duplicate names rejected, original intact.
	if err := r.RegisterStateful(statefulDef("todo_write"), "navi-a", st, handler); err != nil {
		t.Fatalf("first RegisterStateful: %v", err)
	}
	if err := r.RegisterStateful(statefulDef("todo_write"), "navi-b", st, handler); err == nil || !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate err = %v, want ErrDuplicateTool", err)
	}
}

func TestStatefulHandlerPersistsThroughSeam(t *testing.T) {
	// The handler writes through the store handle it was handed; the
	// write lands scoped to the registration's navi identity.
	r := New()
	st := newMemStatefulStore()
	if err := r.RegisterStateful(statefulDef("todo_write"), "navi-a", st, func(ctx context.Context, args map[string]any, state StateContext) (string, error) {
		persisted, err := state.Store.ReplaceTodos(state.NaviID, []datastore.Todo{{Text: "from navi-a", Status: datastore.TodoPending}})
		if err != nil {
			return "", err
		}
		return `{"written":` + strconv.Itoa(len(persisted)) + `}`, nil
	}); err != nil {
		t.Fatalf("RegisterStateful: %v", err)
	}
	if err := r.SetActive([]string{"todo_write"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), "todo_write", nil); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	todos, err := st.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 1 || todos[0].Text != "from navi-a" || todos[0].NaviID != "navi-a" {
		t.Fatalf("persisted todos = %+v", todos)
	}
	// Another navi sees nothing.
	todosB, err := st.ListTodos("navi-b", "", 0)
	if err != nil {
		t.Fatalf("ListTodos(navi-b): %v", err)
	}
	if len(todosB) != 0 {
		t.Fatalf("navi-b saw navi-a state: %+v", todosB)
	}
}
