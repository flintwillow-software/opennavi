package navi

import (
	"errors"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/object"
)

// Names of the stateful-tool object types.
const (
	// TypeTodo is a durable todo item.
	TypeTodo = "todo"
	// TypeNote is a durable note.
	TypeNote = "note"
)

// ToolStateTypes returns the todo and note registrations for a navi identity,
// bound to the stateful view the tools were given. A navi that wires the
// stateful tools registers them, so the object type reads through the same
// view the component writes (ADR 0017 decision 8):
//
//	state := n.Datastores.Handle().Stateful()
//	for _, reg := range navi.ToolStateTypes(naviID, state) {
//	    if err := n.Types.Add(reg); err != nil { return err }
//	}
//
// The types are not exposed by default because the stateful view is chosen
// per component at declaration, not by the navi: the author holds the view,
// so the author registers the type with it.
func ToolStateTypes(naviID string, view datastore.ToolState) []TypeRegistration {
	return []TypeRegistration{
		{Type: todoType(), Provider: &todoProvider{naviID: naviID, view: view}},
		{Type: noteType(), Provider: &noteProvider{naviID: naviID, view: view}},
	}
}

// ── todo ────────────────────────────────────────────────────────────────

func todoType() object.Type {
	return object.Type{
		Name:  TypeTodo,
		Label: "Todo",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString},
			{Name: "text", Label: "Text", Kind: object.KindText},
			{Name: "status", Label: "Status", Kind: object.KindEnum,
				Enum: []string{"pending", "in_progress", "done"}},
			{Name: "priority", Label: "Priority", Kind: object.KindInt},
			{Name: "created_at", Label: "Created", Kind: object.KindTime},
			{Name: "updated_at", Label: "Updated", Kind: object.KindTime},
		},
		Actions: []object.Action{
			{Name: "complete", Label: "Complete"},
			{Name: "start", Label: "Start"},
			{Name: "remove", Label: "Remove", Confirm: true},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Todos", Columns: []string{"text", "status", "priority"},
				GroupBy: "status"},
			{Kind: object.ViewDetail, Label: "Todo"},
		},
	}
}

// todoProvider serves durable todos through the stateful view, scoped to the
// navi identity the type was registered for.
type todoProvider struct {
	naviID string
	view   datastore.ToolState
}

func (p *todoProvider) List(q object.Query) ([]object.Object, string, error) {
	todos, err := p.list()
	if err != nil {
		return nil, "", err
	}
	start, end := pageWindow(len(todos), q)
	out := make([]object.Object, 0, end-start)
	for _, t := range todos[start:end] {
		out = append(out, todoObject(t))
	}
	return out, nextCursor(end, len(todos)), nil
}

func (p *todoProvider) Get(id string) (object.Object, error) {
	todos, err := p.list()
	if err != nil {
		return object.Object{}, err
	}
	for _, t := range todos {
		if t.ID == id {
			return todoObject(t), nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (p *todoProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

func (p *todoProvider) list() ([]datastore.Todo, error) {
	if p.view == nil {
		return nil, nil
	}
	todos, err := p.view.ListTodos(p.naviID, "", 0)
	if errors.Is(err, datastore.ErrUnbound) {
		return nil, nil
	}
	return todos, err
}

func todoObject(t datastore.Todo) object.Object {
	return object.Object{
		Type:    TypeTodo,
		ID:      t.ID,
		Version: t.UpdatedAt.UnixNano(),
		Fields: map[string]any{
			"id": t.ID, "text": t.Text, "status": string(t.Status), "priority": t.Priority,
			"created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
		},
	}
}

// ── note ────────────────────────────────────────────────────────────────

func noteType() object.Type {
	return object.Type{
		Name:  TypeNote,
		Label: "Note",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString},
			{Name: "title", Label: "Title", Kind: object.KindString},
			{Name: "body", Label: "Body", Kind: object.KindText},
			{Name: "tags", Label: "Tags", Kind: object.KindList, ItemKind: object.KindString},
			{Name: "created_at", Label: "Created", Kind: object.KindTime},
			{Name: "updated_at", Label: "Updated", Kind: object.KindTime},
		},
		Actions: []object.Action{
			{Name: "remove", Label: "Remove", Confirm: true},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Notes", Columns: []string{"title", "tags", "updated_at"}},
			{Kind: object.ViewDetail, Label: "Note"},
		},
	}
}

// noteProvider serves notes through the stateful view, scoped to the navi
// identity the type was registered for.
type noteProvider struct {
	naviID string
	view   datastore.ToolState
}

func (p *noteProvider) List(q object.Query) ([]object.Object, string, error) {
	notes, err := p.list(datastore.NoteQuery{})
	if err != nil {
		return nil, "", err
	}
	start, end := pageWindow(len(notes), q)
	out := make([]object.Object, 0, end-start)
	for _, n := range notes[start:end] {
		out = append(out, noteObject(n))
	}
	return out, nextCursor(end, len(notes)), nil
}

func (p *noteProvider) Get(id string) (object.Object, error) {
	notes, err := p.list(datastore.NoteQuery{ID: id})
	if err != nil {
		return object.Object{}, err
	}
	if len(notes) == 0 {
		return object.Object{}, object.ErrNotFound
	}
	return noteObject(notes[0]), nil
}

func (p *noteProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

func (p *noteProvider) list(q datastore.NoteQuery) ([]datastore.Note, error) {
	if p.view == nil {
		return nil, nil
	}
	notes, err := p.view.ReadNotes(p.naviID, q)
	if errors.Is(err, datastore.ErrUnbound) {
		return nil, nil
	}
	return notes, err
}

func noteObject(n datastore.Note) object.Object {
	return object.Object{
		Type:    TypeNote,
		ID:      n.ID,
		Version: n.UpdatedAt.UnixNano(),
		Fields: map[string]any{
			"id": n.ID, "title": n.Title, "body": n.Body, "tags": n.Tags,
			"created_at": n.CreatedAt, "updated_at": n.UpdatedAt,
		},
	}
}
