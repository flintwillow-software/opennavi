package view

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/object"
)

// thoughtType mirrors the navi's thought descriptor closely enough to exercise
// every resolution rule: declared list columns, section hints, a sensitive
// field, relations, actions with and without schemas, and a detail collection.
func thoughtType() object.Type {
	return object.Type{
		Name: "thought", Label: "Thought", ID: "id",
		Fields: []object.Field{
			{Name: "id", Kind: object.KindString, Section: "identity"},
			{Name: "prompt", Label: "Prompt", Kind: object.KindText, Section: "core"},
			{Name: "status", Kind: object.KindEnum, Section: "state",
				Enum: []string{"pending", "running", "complete"}},
			{Name: "priority", Kind: object.KindInt, Section: "state"},
			{Name: "foreground", Kind: object.KindBool, Section: "run"},
			{Name: "skills", Kind: object.KindList, ItemKind: object.KindString, Section: "run"},
			{Name: "depends_on", Kind: object.KindList, ItemKind: object.KindRef, Section: "relations"},
			{Name: "verification", Kind: object.KindBool, Section: "run", ReadOnly: true},
			{Name: "updated_at", Kind: object.KindTime, Section: "timestamps"},
			{Name: "checkpoint", Kind: object.KindJSON, Section: "internal", Sensitive: true},
		},
		Relations: []object.Relation{
			{Name: "depends_on", Label: "Depends on", Target: "thought", Kind: object.RelationMany},
		},
		Details: []object.Detail{{Name: "transcript", Label: "Transcript", Type: "entry", Key: "thought_id"}},
		Actions: []object.Action{
			{Name: "pause", Label: "Pause"},
			{Name: "steer", Label: "Steer", Args: json.RawMessage(`{"type":"object"}`)},
			{Name: "remove", Confirm: true},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Thoughts", Columns: []string{"prompt", "status", "priority", "updated_at"}},
			{Kind: object.ViewDetail, Label: "Thought", Sections: []object.Section{
				{Name: "core", Label: "Task"},
				{Name: "state", Label: "State"},
			}},
			{Kind: object.ViewChat, Label: "Conversation", Detail: "transcript"},
			{Kind: object.ViewBoard, Label: "Board", GroupBy: "status"},
		},
	}
}

func thoughtObj(id string, fields map[string]any) object.Object {
	all := map[string]any{
		"id": id, "prompt": "prompt " + id, "status": "pending", "priority": 4,
		"foreground": true, "skills": []string{"a", "b"}, "depends_on": []any{},
		"verification": false, "updated_at": time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
	for k, v := range fields {
		all[k] = v
	}
	return object.Object{Type: "thought", ID: id, Version: 1, Fields: all}
}

// ── list ────────────────────────────────────────────────────────────────

func TestColumnsComeFromTheDeclaredHint(t *testing.T) {
	cols := Columns(thoughtType())
	want := []string{"prompt", "status", "priority", "updated_at"}
	if len(cols) != len(want) {
		t.Fatalf("columns = %+v, want %v", cols, want)
	}
	for i, name := range want {
		if cols[i].Field != name {
			t.Fatalf("column %d = %q, want %q", i, cols[i].Field, name)
		}
	}
	if cols[0].Label != "Prompt" || cols[2].Label != "Priority" {
		t.Fatalf("labels = %q/%q, want Prompt/Priority", cols[0].Label, cols[2].Label)
	}
}

func TestColumnsDefaultToNonSensitiveFields(t *testing.T) {
	ty := thoughtType()
	ty.Views = nil // no hints: the toolkit must still render something
	cols := Columns(ty)
	if len(cols) == 0 {
		t.Fatal("a type with no hints must render its non-sensitive fields")
	}
	for _, c := range cols {
		if c.Field == "checkpoint" {
			t.Fatal("a sensitive field must never become a default column")
		}
	}
	if cols[0].Field != "id" {
		t.Fatalf("first default column = %q, want the identity field", cols[0].Field)
	}
}

func TestRowsFormatByKindAndTolerateMissingFields(t *testing.T) {
	ty := thoughtType()
	cols := Columns(ty)
	rows := Rows(ty, []object.Object{
		thoughtObj("t1", map[string]any{"skills": []string{"core_tools"}}),
		{Type: "thought", ID: "t2", Fields: map[string]any{"prompt": "bare"}},
	}, cols)

	if len(rows) != 2 || rows[0].ID != "t1" || rows[1].ID != "t2" {
		t.Fatalf("rows = %+v, want t1 then t2", rows)
	}
	if rows[0].Cells[0].Value != "prompt t1" {
		t.Fatalf("prompt cell = %q", rows[0].Cells[0].Value)
	}
	if rows[0].Cells[3].Value != "2026-10-08 12:00:00Z" {
		t.Fatalf("time cell = %q, want a compact UTC stamp", rows[0].Cells[3].Value)
	}
	// A field the object does not carry is an empty cell, not a panic or an
	// error: the surface omits what the caller may not read.
	if rows[1].Cells[1].Value != "" || rows[1].Cells[3].Value != "" {
		t.Fatalf("missing fields rendered as %+v, want empty cells", rows[1].Cells)
	}
	if rows[0].Cells[0].Raw != "prompt t1" {
		t.Fatal("the raw value must survive for a renderer that wants its own presentation")
	}
}

func TestFormatByKind(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		kind object.Kind
		val  any
		want string
	}{
		{"nil", object.KindString, nil, ""},
		{"string", object.KindString, "hi", "hi"},
		{"text", object.KindText, "multi\nline", "multi\nline"},
		{"int", object.KindInt, 7, "7"},
		{"int float", object.KindInt, float64(7), "7"},
		{"float", object.KindFloat, 1.5, "1.5"},
		{"bool", object.KindBool, true, "true"},
		{"enum", object.KindEnum, "pending", "pending"},
		{"time", object.KindTime, at, "2026-10-08 12:30:00Z"},
		{"zero time", object.KindTime, time.Time{}, ""},
		{"duration", object.KindDuration, 90 * time.Second, "1m30s"},
		{"list", object.KindList, []any{"a", "b"}, "a, b"},
		{"string list", object.KindList, []string{"a"}, "a"},
		{"ref", object.KindRef, "t9", "t9"},
		{"json", object.KindJSON, map[string]any{"a": 1}, `{"a":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Format(tc.kind, tc.val); got != tc.want {
				t.Fatalf("Format(%s, %v) = %q, want %q", tc.kind, tc.val, got, tc.want)
			}
		})
	}
}

// ── detail ──────────────────────────────────────────────────────────────

func TestSectionsFollowTheHintOrderAndSkipWhatIsNotReadable(t *testing.T) {
	ty := thoughtType()
	obj := thoughtObj("t1", map[string]any{"checkpoint": `{"secret":true}`})
	d := DetailOf(ty, obj)

	if d.Title != "prompt t1" {
		t.Fatalf("title = %q, want the prompt", d.Title)
	}
	// The hint orders core before state; identity/timestamps are undeclared and
	// follow in first-seen order.
	if len(d.Sections) == 0 || d.Sections[0].Name != "core" || d.Sections[0].Label != "Task" {
		t.Fatalf("sections = %+v, want the declared 'core' section first", d.Sections)
	}
	if d.Sections[1].Name != "state" {
		t.Fatalf("second section = %q, want state", d.Sections[1].Name)
	}
	for _, s := range d.Sections {
		for _, f := range s.Fields {
			if f.Name == "checkpoint" {
				t.Fatal("a sensitive field must never appear in a detail view")
			}
		}
	}
	var sawStatus bool
	for _, f := range d.Sections[1].Fields {
		if f.Name == "status" {
			sawStatus = f.Value == "pending"
		}
	}
	if !sawStatus {
		t.Fatalf("status field missing from the state section: %+v", d.Sections[1])
	}
}

func TestSectionsSkipAbsentFields(t *testing.T) {
	ty := thoughtType()
	d := DetailOf(ty, object.Object{Type: "thought", ID: "t2", Fields: map[string]any{"prompt": "only this"}})
	for _, s := range d.Sections {
		for _, f := range s.Fields {
			if f.Name != "prompt" {
				t.Fatalf("section %q carried %q, but the object only has prompt", s.Name, f.Name)
			}
		}
	}
}

func TestActionsCarryConfirmAndSchema(t *testing.T) {
	acts := Actions(thoughtType())
	if len(acts) != 3 {
		t.Fatalf("actions = %+v, want three", acts)
	}
	if acts[0].Name != "pause" || acts[0].Confirm || len(acts[0].Args) != 0 {
		t.Fatalf("pause = %+v", acts[0])
	}
	if len(acts[1].Args) == 0 {
		t.Fatal("steer lost its argument schema")
	}
	if !acts[2].Confirm || acts[2].Label != "Remove" {
		t.Fatalf("remove = %+v, want Confirm with its declared label", acts[2])
	}
}

// ── chat ────────────────────────────────────────────────────────────────

func TestChatTurnsMapEntries(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	entries := []object.Object{
		{Type: "entry", ID: "e1", Fields: map[string]any{"role": "user", "message": "hi", "time": at}},
		{Type: "entry", ID: "e2", Fields: map[string]any{"role": "assistant", "message": "let me check",
			"time": at}},
		{Type: "entry", ID: "e3", Fields: map[string]any{"role": "assistant", "tool_name": "todo_write",
			"tool_input": `{"text":"x"}`, "time": at}},
		{Type: "entry", ID: "e4", Fields: map[string]any{"role": "tool", "tool_name": "todo_write",
			"message": "added", "time": at}},
	}
	chat := ChatOf(thoughtType(), "t1", "transcript", entries, "next-page")

	if len(chat.Turns) != 4 || chat.Next != "next-page" {
		t.Fatalf("chat = %+v, want four turns and the cursor", chat)
	}
	if chat.Turns[0].Role != "user" || chat.Turns[0].Text != "hi" {
		t.Fatalf("turn 0 = %+v", chat.Turns[0])
	}
	if chat.Turns[2].Tool != "todo_write" || chat.Turns[2].Input != `{"text":"x"}` || chat.Turns[2].Text != "" {
		t.Fatalf("tool call turn = %+v, want Tool+Input only", chat.Turns[2])
	}
	if chat.Turns[3].Tool != "todo_write" || chat.Turns[3].Output != "added" {
		t.Fatalf("tool result turn = %+v, want Tool+Output", chat.Turns[3])
	}
	if !chat.Turns[0].At.Equal(at) {
		t.Fatalf("turn time = %v, want %v", chat.Turns[0].At, at)
	}
}

// ── board and graph ─────────────────────────────────────────────────────

func TestBoardGroupsByTheDeclaredField(t *testing.T) {
	ty := thoughtType()
	board := BoardOf(ty, []object.Object{
		thoughtObj("t1", nil),
		thoughtObj("t2", map[string]any{"status": "running"}),
		thoughtObj("t3", nil),
	})
	if board.GroupBy != "status" {
		t.Fatalf("groupBy = %q, want status", board.GroupBy)
	}
	if len(board.Groups) != 2 {
		t.Fatalf("groups = %+v, want pending and running", board.Groups)
	}
	if board.Groups[0].Key != "pending" || len(board.Groups[0].Rows) != 2 {
		t.Fatalf("group 0 = %+v, want two pending rows", board.Groups[0])
	}
	if board.Groups[1].Key != "running" || len(board.Groups[1].Rows) != 1 {
		t.Fatalf("group 1 = %+v, want one running row", board.Groups[1])
	}
}

func TestGraphResolvesDeclaredRelations(t *testing.T) {
	ty := thoughtType()
	g := GraphOf(ty, []object.Object{
		thoughtObj("t1", nil),
		thoughtObj("t2", map[string]any{"depends_on": []any{"t1"}}),
		thoughtObj("t3", map[string]any{"depends_on": []any{"t2", "t9"}}),
	})
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes = %+v, want t1..t3 plus the out-of-set t9", g.Nodes)
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges = %+v, want three", g.Edges)
	}
	if g.Edges[0].From != "t2" || g.Edges[0].To != "t1" || g.Edges[0].Name != "depends_on" {
		t.Fatalf("edge 0 = %+v", g.Edges[0])
	}
	var sawMissing bool
	for _, n := range g.Nodes {
		if n.ID == "t9" && n.Type == "thought" {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatal("a reference outside the read set must still appear as a node")
	}
}

// ── hints ───────────────────────────────────────────────────────────────

func TestPrimaryAndHintDefaults(t *testing.T) {
	ty := thoughtType()
	if got := Primary(ty); got != object.ViewList {
		t.Fatalf("Primary = %q, want list", got)
	}
	if _, ok := Hint(ty, object.ViewChat); !ok {
		t.Fatal("the chat hint is declared")
	}
	ty.Views = nil
	if got := Primary(ty); got != object.ViewList {
		t.Fatalf("Primary with no hints = %q, want list", got)
	}
	if _, ok := Hint(ty, object.ViewDetail); ok {
		t.Fatal("a type with no hints declares no detail view")
	}
}

// ── the read path ───────────────────────────────────────────────────────

// fakeSurface serves one type's objects and detail entries.
type fakeSurface struct {
	types   []object.Type
	objects []object.Object
	entries []object.Object
}

func (f *fakeSurface) Types() ([]object.Type, error) { return f.types, nil }

func (f *fakeSurface) List(typeName string, _ object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	return f.objects, "cursor-1", nil
}

func (f *fakeSurface) Get(typeName, id string) (object.Object, error) {
	if typeName != "thought" {
		return object.Object{}, object.ErrUnknownType
	}
	for _, o := range f.objects {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (f *fakeSurface) Detail(typeName, id, detail string, _ object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	if detail != "transcript" {
		return nil, "", object.ErrUnknownDetail
	}
	if _, err := f.Get(typeName, id); err != nil {
		return nil, "", err
	}
	return f.entries, "", nil
}

func (f *fakeSurface) Act(string, string, string, map[string]any) error {
	return object.ErrUnsupported
}

func (f *fakeSurface) Watch(int) ([]object.Delta, int, bool, error) { return nil, 0, false, nil }

func newFakeSurface() *fakeSurface {
	return &fakeSurface{
		types:   []object.Type{thoughtType()},
		objects: []object.Object{thoughtObj("t1", nil), thoughtObj("t2", nil)},
		entries: []object.Object{{Type: "entry", ID: "e1", Fields: map[string]any{"role": "user", "message": "hi"}}},
	}
}

func TestReadListResolvesTheTypeFromTheSurface(t *testing.T) {
	l, err := ReadList(newFakeSurface(), "thought", object.Query{Limit: 2})
	if err != nil {
		t.Fatalf("ReadList: %v", err)
	}
	if l.Type.Name != "thought" || l.Kind != object.ViewList {
		t.Fatalf("list = %+v", l)
	}
	if len(l.Rows) != 2 || l.Next != "cursor-1" {
		t.Fatalf("list rows = %d, next %q", len(l.Rows), l.Next)
	}
	if len(l.Columns) != 4 {
		t.Fatalf("columns = %+v, want the declared four", l.Columns)
	}
}

func TestReadDetailAndChat(t *testing.T) {
	s := newFakeSurface()

	d, err := ReadDetail(s, "thought", "t1")
	if err != nil {
		t.Fatalf("ReadDetail: %v", err)
	}
	if d.Object.ID != "t1" || d.Title != "prompt t1" || len(d.Actions) != 3 {
		t.Fatalf("detail = %+v", d)
	}

	c, err := ReadChat(s, "thought", "t1", "transcript", object.Query{})
	if err != nil {
		t.Fatalf("ReadChat: %v", err)
	}
	if c.Detail != "transcript" || len(c.Turns) != 1 || c.Turns[0].Text != "hi" {
		t.Fatalf("chat = %+v", c)
	}
}

func TestReadsReportSurfaceErrors(t *testing.T) {
	s := newFakeSurface()
	if _, err := ReadList(s, "ghost", object.Query{}); !errors.Is(err, object.ErrUnknownType) {
		t.Fatalf("ReadList(ghost) = %v, want ErrUnknownType", err)
	}
	if _, err := ReadDetail(s, "thought", "nope"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("ReadDetail(nope) = %v, want ErrNotFound", err)
	}
	if _, err := ReadChat(s, "thought", "t1", "ghost", object.Query{}); !errors.Is(err, object.ErrUnknownDetail) {
		t.Fatalf("ReadChat(ghost) = %v, want ErrUnknownDetail", err)
	}
}
