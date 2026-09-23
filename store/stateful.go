package store

import (
	"errors"
	"time"
)

// TodoStatus is the lifecycle state of a durable todo item
// (S130500 §00–02). The zero value is not a valid status; a status
// must always be set explicitly.
type TodoStatus string

const (
	// TodoPending marks an item not yet started.
	TodoPending TodoStatus = "pending"
	// TodoInProgress marks an item the navi is currently working on.
	TodoInProgress TodoStatus = "in_progress"
	// TodoDone marks a completed item.
	TodoDone TodoStatus = "done"
)

// Valid reports whether s is a defined todo status.
func (s TodoStatus) Valid() bool {
	switch s {
	case TodoPending, TodoInProgress, TodoDone:
		return true
	}
	return false
}

// Todo is one durable todo item (S130500 §00). IDs are stable
// across partial updates; a full-replace write reassigns them.
type Todo struct {
	// ID is the stable item id, assigned on persist.
	ID string
	// NaviID is the owning navi identity (scoping key).
	NaviID string
	// Text is the item description.
	Text string
	// Status is the lifecycle state.
	Status TodoStatus
	// Priority orders items (lower = earlier in todo_read output).
	Priority int
	// CreatedAt / UpdatedAt are store-managed timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TodoUpdate is a partial update applied to one todo item by id
// (S130500 §02). Pointer fields are only applied when non-nil; Remove
// deletes the item instead.
type TodoUpdate struct {
	ID       string
	Text     *string
	Status   *TodoStatus
	Priority *int
	Remove   bool
}

// Note is one verbatim scratchpad entry (S130500 §04–05). Bodies are
// stored verbatim — no semantic processing (semantic recall belongs to
// the memory system, not notes).
type Note struct {
	// ID is the stable note id; empty on create means assign a new one.
	ID string
	// NaviID is the owning navi identity (scoping key).
	NaviID string
	// Title is the note's short title (searched literally).
	Title string
	// Body is the verbatim note content, subject to the tool layer's
	// 64KB size cap.
	Body string
	// Tags are free-form labels; note_read filters on them exactly.
	Tags []string
	// CreatedAt / UpdatedAt are store-managed timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NoteQuery selects notes for ReadNotes. Zero fields are ignored;
// at most one of ID/Tag/Query is meaningful to the tool layer — the
// store applies ID first, then Tag, then literal substring on
// title/body, then Limit.
type NoteQuery struct {
	ID    string
	Tag   string
	Query string
	Limit int
}

// Sentinel errors for stateful tool storage operations. Stores wrap
// them so callers can errors.Is against the sentinel.
var (
	// ErrTodoNotFound is returned when an update references a todo id
	// that does not exist for the navi.
	ErrTodoNotFound = errors.New("todo not found")
	// ErrNoteNotFound is returned when a note id does not exist for
	// the navi.
	ErrNoteNotFound = errors.New("note not found")
)

// StatefulToolStore persists stateful catalog tool state
// (S130500 §07): todo and note state for tools registered through the
// stateful registration variant (tools/registry.RegisterStateful).
//
// Per-navi scoping is part of the contract: naviID is the first
// parameter of every method and implementations MUST scope all reads
// and writes to that identity — two navis on the same host never see
// each other's state. State persists across restarts: implementations
// are backed by the durable store (SQLite), not process memory.
//
// The SQLite implementation lives in store/sqlite (NewStatefulStore).
type StatefulToolStore interface {
	// ListTodos returns the navi's todo items ordered by priority,
	// then most recent activity, optionally filtered by status
	// (empty status = all).
	ListTodos(naviID string, status TodoStatus, limit int) ([]Todo, error)
	// ReplaceTodos atomically replaces the navi's entire todo list
	// with items, assigning stable ids, and returns the persisted
	// items with ids and timestamps. On failure the previous list
	// is left intact.
	ReplaceTodos(naviID string, items []Todo) ([]Todo, error)
	// UpdateTodos applies partial updates atomically: either every
	// update lands or none does. An unknown id rejects the whole
	// batch with ErrTodoNotFound (wrapped).
	UpdateTodos(naviID string, updates []TodoUpdate) ([]Todo, error)
	// ReadNotes returns notes matching the query (by id, tag, or
	// literal substring on title/body), ordered by most recent
	// activity.
	ReadNotes(naviID string, q NoteQuery) ([]Note, error)
	// UpsertNote stores a note verbatim: new id assigned when n.ID
	// is empty, existing note updated when it matches. Returns the
	// persisted note.
	UpsertNote(naviID string, n Note) (Note, error)
	// RemoveNote deletes the note with the given id; unknown ids
	// return ErrNoteNotFound (wrapped).
	RemoveNote(naviID, id string) error
}
