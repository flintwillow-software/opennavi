package todo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// memStatefulStore is a minimal in-memory StatefulToolStore mirroring
// the registry tests' helper — enough for tool-layer semantics; the
// SQLite-backed durability/scoping tests live in store/sqlite.
type memStatefulStore struct {
	todos map[string][]store.Todo
	seq   int
	// failReplace makes ReplaceTodos fail mid-write (after clearing),
	// simulating a store error partway through (rollback
	// scenario). The registry-level mem store used elsewhere commits
	// only at the end, so a mid-write failure with intact prior state
	// needs this seam-level simulation.
	failReplace bool
}

func newMemStatefulStore() *memStatefulStore {
	return &memStatefulStore{todos: map[string][]store.Todo{}}
}

func (m *memStatefulStore) ListTodos(naviID string, status store.TodoStatus, limit int) ([]store.Todo, error) {
	var out []store.Todo
	for _, t := range m.todos[naviID] {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	// Ordering: priority, then most recent activity.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Priority < out[i].Priority || (out[j].Priority == out[i].Priority && out[j].UpdatedAt.After(out[i].UpdatedAt)) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStatefulStore) ReplaceTodos(naviID string, items []store.Todo) ([]store.Todo, error) {
	if m.failReplace {
		return nil, errors.New("simulated store failure partway")
	}
	out := make([]store.Todo, 0, len(items))
	for _, item := range items {
		m.seq++
		item.ID = fmt.Sprintf("t-%d", m.seq)
		item.NaviID = naviID
		out = append(out, item)
	}
	m.todos[naviID] = out
	return out, nil
}

func (m *memStatefulStore) UpdateTodos(naviID string, updates []store.TodoUpdate) ([]store.Todo, error) {
	list := m.todos[naviID]
	// Guarantee: atomic batch — validate every id BEFORE applying any
	// update, so an unknown id leaves prior state untouched.
	index := make(map[string]int, len(list))
	for i, t := range list {
		index[t.ID] = i
	}
	for _, u := range updates {
		if _, ok := index[u.ID]; !ok {
			return nil, fmt.Errorf("%w: %s", store.ErrTodoNotFound, u.ID)
		}
	}
	for _, u := range updates {
		idx := index[u.ID]
		if u.Remove {
			list = append(list[:idx], list[idx+1:]...)
			index = make(map[string]int, len(list))
			for i, t := range list {
				index[t.ID] = i
			}
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
	return append([]store.Todo(nil), list...), nil
}

func (m *memStatefulStore) ReadNotes(naviID string, q store.NoteQuery) ([]store.Note, error) {
	return nil, errors.New("not implemented")
}

func (m *memStatefulStore) UpsertNote(naviID string, n store.Note) (store.Note, error) {
	return store.Note{}, errors.New("not implemented")
}

func (m *memStatefulStore) RemoveNote(naviID, id string) error {
	return errors.New("not implemented")
}

// newToolRegistry builds a registry with all three todo tools bound to
// navi-a through the shared store, all active, plus a second navi's
// registration for scoping checks.
func newToolRegistry(t *testing.T) (*registry.Registry, *memStatefulStore) {
	t.Helper()
	reg := registry.New()
	st := newMemStatefulStore()
	if err := RegisterTools(reg, "navi-a", st); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if err := reg.SetActive([]string{"todo_read", "todo_write", "todo_update"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	return reg, st
}

func dispatch(t *testing.T, reg *registry.Registry, name string, args string) (string, error) {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return reg.Dispatch(context.Background(), name, raw)
}

func decodeIDs(t *testing.T, out string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	return items
}

func TestDefinitionsComplete(t *testing.T) {
	want := map[string]struct {
		access registry.ToolAccess
		mode   registry.ToolMode
		format string
	}{
		"todo_read":   {registry.AccessReadOnly, registry.ToolModeParallel, "json"},
		"todo_write":  {registry.AccessWrite, registry.ToolModeSequential, "json"},
		"todo_update": {registry.AccessWrite, registry.ToolModeSequential, "json"},
	}
	defs := Definitions()
	if len(defs) != 3 {
		t.Fatalf("Definitions() returned %d defs, want 3", len(defs))
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
		w := want[def.Name]
		if def.Access != w.access || def.Mode != w.mode || def.OutputFormat != w.format {
			t.Errorf("%s metadata mismatch: access=%v mode=%v format=%q", def.Name, def.Access, def.Mode, def.OutputFormat)
		}
		if def.Timeout <= 0 || def.Recovery == "" || def.PromptSnippet == "" {
			t.Errorf("%s incomplete metadata: timeout=%v recovery=%q snippet=%q", def.Name, def.Timeout, def.Recovery, def.PromptSnippet)
		}
		if def.SourceInfo.Origin != registry.OriginSDK {
			t.Errorf("%s origin = %v, want sdk", def.Name, def.SourceInfo.Origin)
		}
		if def.Namespace() != "todo" {
			t.Errorf("%s namespace = %q", def.Name, def.Namespace())
		}
		// Schemas must parse.
		if _, err := registry.ParseSchema(def.InputSchema.Raw()); err != nil {
			t.Errorf("%s schema invalid: %v", def.Name, err)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("Definitions() missing %s", name)
		}
	}
}

func TestTodoReadListingAndFilter(t *testing.T) {
	reg, st := newToolRegistry(t)
	_, err := dispatch(t, reg, "todo_write", `{"items":[
		{"text":"second","priority":5},
		{"text":"first","status":"in_progress","priority":0},
		{"text":"third","priority":2}
	]}`)
	if err != nil {
		t.Fatalf("todo_write: %v", err)
	}

	// Listing all: ordered by priority, then most recent activity.
	out, err := dispatch(t, reg, "todo_read", "")
	if err != nil {
		t.Fatalf("todo_read: %v", err)
	}
	items := decodeIDs(t, out)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d (%s)", len(items), out)
	}
	if items[0]["text"] != "first" || items[1]["text"] != "third" || items[2]["text"] != "second" {
		t.Fatalf("ordering wrong: %s", out)
	}
	for _, item := range items {
		for _, key := range []string{"id", "text", "status", "priority", "created_at", "updated_at"} {
			if _, ok := item[key]; !ok {
				t.Fatalf("item missing %q: %s", key, out)
			}
		}
	}
	// Write with no explicit status defaults to pending.
	if items[0]["status"] != string(store.TodoInProgress) {
		t.Fatalf("first item status = %v, want in_progress", items[0]["status"])
	}
	if items[1]["status"] != string(store.TodoPending) {
		t.Fatalf("defaulted status = %v, want pending", items[1]["status"])
	}

	// Filtering by status.
	out, err = dispatch(t, reg, "todo_read", `{"status":"in_progress"}`)
	if err != nil {
		t.Fatalf("todo_read(filter): %v", err)
	}
	items = decodeIDs(t, out)
	if len(items) != 1 || items[0]["text"] != "first" {
		t.Fatalf("status filter wrong: %s", out)
	}
	// Empty list filter result is [] not null.
	out, err = dispatch(t, reg, "todo_read", `{"status":"done"}`)
	if err != nil {
		t.Fatalf("todo_read(done): %v", err)
	}
	if !strings.Contains(out, "[]") {
		t.Fatalf("empty filter should encode as [], got %s", out)
	}
	_ = st
}

func TestTodoWriteReplacesEntireList(t *testing.T) {
	reg, st := newToolRegistry(t)
	if _, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"old plan"}]}`); err != nil {
		t.Fatalf("todo_write #1: %v", err)
	}
	out, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"new a"},{"text":"new b","priority":1}]}`)
	if err != nil {
		t.Fatalf("todo_write #2: %v", err)
	}
	items := decodeIDs(t, out)
	if len(items) != 2 {
		t.Fatalf("expected 2 persisted items, got %s", out)
	}
	for _, item := range items {
		if id, ok := item["id"].(string); !ok || id == "" {
			t.Fatalf("todo_write must return assigned ids: %s", out)
		}
	}
	todos, _ := st.ListTodos("navi-a", "", 0)
	if len(todos) != 2 || todos[0].Text != "new a" {
		t.Fatalf("full replace left stale items: %+v", todos)
	}
	// Write with empty items clears the list.
	if _, err := dispatch(t, reg, "todo_write", `{"items":[]}`); err != nil {
		t.Fatalf("todo_write(empty): %v", err)
	}
	todos, _ = st.ListTodos("navi-a", "", 0)
	if len(todos) != 0 {
		t.Fatalf("empty write did not clear list: %+v", todos)
	}
}

func TestTodoWriteRollbackOnStoreFailure(t *testing.T) {
	reg, st := newToolRegistry(t)
	before, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"safe plan"}]}`)
	if err != nil {
		t.Fatalf("todo_write setup: %v", err)
	}

	st.failReplace = true
	_, err = dispatch(t, reg, "todo_write", `{"items":[{"text":"doomed plan"}]}`)
	st.failReplace = false
	if err == nil {
		t.Fatal("expected todo_write to fail on store error")
	}
	if !strings.Contains(err.Error(), "previous list intact") {
		t.Fatalf("failure reason not surfaced: %v", err)
	}

	// Previous list intact (rollback scenario).
	after, err := dispatch(t, reg, "todo_read", "")
	if err != nil {
		t.Fatalf("todo_read after failure: %v", err)
	}
	if after != before {
		t.Fatalf("todo list changed after failed write: before=%s after=%s", before, after)
	}
}

func TestTodoUpdateTransitionsAndEdit(t *testing.T) {
	reg, _ := newToolRegistry(t)
	out, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"task one"},{"text":"task two","priority":3}]}`)
	if err != nil {
		t.Fatalf("todo_write: %v", err)
	}
	items := decodeIDs(t, out)
	id1 := items[0]["id"].(string)
	id2 := items[1]["id"].(string)

	// Status transition returns the updated item.
	out, err = dispatch(t, reg, "todo_update", fmt.Sprintf(`{"updates":[{"id":%q,"status":"done"}]}`, id1))
	if err != nil {
		t.Fatalf("todo_update: %v", err)
	}
	updated := decodeIDs(t, out)
	var found map[string]any
	for _, item := range updated {
		if item["id"] == id1 {
			found = item
		}
	}
	if found == nil || found["status"] != string(store.TodoDone) {
		t.Fatalf("transition result missing/wrong: %s", out)
	}

	// Text edit + priority change together.
	out, err = dispatch(t, reg, "todo_update", fmt.Sprintf(`{"updates":[{"id":%q,"text":"renamed","priority":9}]}`, id2))
	if err != nil {
		t.Fatalf("todo_update(edit): %v", err)
	}
	for _, item := range decodeIDs(t, out) {
		if item["id"] == id2 {
			if item["text"] != "renamed" || item["priority"] != float64(9) {
				t.Fatalf("edit result wrong: %s", out)
			}
		}
	}
}

func TestTodoUpdateUnknownIDRejectsBatch(t *testing.T) {
	reg, st := newToolRegistry(t)
	out, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"keep me"}]}`)
	if err != nil {
		t.Fatalf("todo_write: %v", err)
	}
	goodID := decodeIDs(t, out)[0]["id"].(string)

	// Unknown id rejects the whole batch — the good update must not
	// land either.
	_, err = dispatch(t, reg, "todo_update", fmt.Sprintf(
		`{"updates":[{"id":%q,"text":"should not land"},{"id":"no-such-id","status":"done"}]}`, goodID))
	if err == nil {
		t.Fatal("expected batch rejection on unknown id")
	}
	if !strings.Contains(err.Error(), "no-such-id") {
		t.Fatalf("error must identify the unknown id: %v", err)
	}
	if !errors.Is(err, store.ErrTodoNotFound) {
		t.Fatalf("error should wrap store.ErrTodoNotFound: %v", err)
	}
	todos, _ := st.ListTodos("navi-a", "", 0)
	if len(todos) != 1 || todos[0].Text != "keep me" {
		t.Fatalf("atomicity broken, list after failed batch: %+v", todos)
	}

	// Removal by id.
	if _, err := dispatch(t, reg, "todo_update", fmt.Sprintf(`{"updates":[{"id":%q,"remove":true}]}`, goodID)); err != nil {
		t.Fatalf("todo_update(remove): %v", err)
	}
	todos, _ = st.ListTodos("navi-a", "", 0)
	if len(todos) != 0 {
		t.Fatalf("remove did not delete: %+v", todos)
	}
}

func TestTodoPerNaviScoping(t *testing.T) {
	reg, st := newToolRegistry(t)
	// Second registration for navi-b over the SAME store.
	regB := registry.New()
	if err := RegisterTools(regB, "navi-b", st); err != nil {
		t.Fatalf("RegisterTools(navi-b): %v", err)
	}
	if err := regB.SetActive([]string{"todo_read", "todo_write", "todo_update"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	if _, err := dispatch(t, reg, "todo_write", `{"items":[{"text":"a's plan"}]}`); err != nil {
		t.Fatalf("todo_write(navi-a): %v", err)
	}
	out, err := dispatch(t, regB, "todo_read", "")
	if err != nil {
		t.Fatalf("todo_read(navi-b): %v", err)
	}
	if !strings.Contains(out, "[]") {
		t.Fatalf("navi-b saw navi-a's todos: %s", out)
	}
	// navi-b's full replace does not clobber navi-a's list.
	if _, err := dispatch(t, regB, "todo_write", `{"items":[{"text":"b's plan"}]}`); err != nil {
		t.Fatalf("todo_write(navi-b): %v", err)
	}
	out, err = dispatch(t, reg, "todo_read", "")
	if err != nil {
		t.Fatalf("todo_read(navi-a) after b write: %v", err)
	}
	if !strings.Contains(out, "a's plan") || strings.Contains(out, "b's plan") {
		t.Fatalf("cross-navi clobber: %s", out)
	}
}

func TestTodoSchemaValidation(t *testing.T) {
	reg, _ := newToolRegistry(t)
	cases := []struct {
		name string
		tool string
		args string
	}{
		{"read bad status", "todo_read", `{"status":"archived"}`},
		{"read bad limit", "todo_read", `{"limit":0}`},
		{"write missing items", "todo_write", `{}`},
		{"write item missing text", "todo_write", `{"items":[{"status":"done"}]}`},
		{"write empty text", "todo_write", `{"items":[{"text":""}]}`},
		{"write bad status", "todo_write", `{"items":[{"text":"x","status":"archived"}]}`},
		{"write extra property", "todo_write", `{"items":[{"text":"x","id":"forced"}]}`},
		{"update missing updates", "todo_update", `{}`},
		{"update empty batch", "todo_update", `{"updates":[]}`},
		{"update missing id", "todo_update", `{"updates":[{"status":"done"}]}`},
		{"update bad status", "todo_update", `{"updates":[{"id":"x","status":"archived"}]}`},
		{"update bad remove", "todo_update", `{"updates":[{"id":"x","remove":"yes"}]}`},
	}
	for _, tc := range cases {
		_, err := dispatch(t, reg, tc.tool, tc.args)
		if err == nil {
			t.Errorf("%s: expected schema rejection for %s %s", tc.name, tc.tool, tc.args)
		}
	}
}

func TestRegisterOneSelective(t *testing.T) {
	reg := registry.New()
	st := newMemStatefulStore()
	if err := RegisterOne(reg, "navi-a", st, "todo_read"); err != nil {
		t.Fatalf("RegisterOne(todo_read): %v", err)
	}
	if err := RegisterOne(reg, "navi-a", st, "todo_unknown"); err == nil {
		t.Fatal("RegisterOne(unknown) should fail")
	}
	if err := reg.SetActive([]string{"todo_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if _, err := dispatch(t, reg, "todo_read", ""); err != nil {
		t.Fatalf("todo_read dispatch: %v", err)
	}
	// Writers were not registered — dispatch must fail.
	if _, err := dispatch(t, reg, "todo_write", `{"items":[]}`); err == nil {
		t.Fatal("todo_write should be unknown when only todo_read registered")
	}
	if err := RegisterTools(nil, "navi-a", st); err == nil {
		t.Fatal("RegisterTools(nil registry) should fail")
	}
	if err := RegisterTools(registry.New(), "navi-a", nil); err == nil {
		t.Fatal("RegisterTools(nil store) should fail")
	}
}
