package datastore

import (
	"errors"
	"sync"

	"github.com/flintwillow-software/opennavi/thought"
)

// ErrUnbound is returned by a view used before its handle was bound to an
// active backend. Assembly binds every claimed handle, so this is only
// reachable when a view is taken and used with no candidate to resolve to.
var ErrUnbound = errors.New("datastore: no active backend bound")

// Datastore is the stable handle a consumer holds. It is created at
// declaration and bound to the active backend at assembly, so a
// component's view survives a backend swap at runtime. Handles are views,
// not backends (ADR 0011 decision 4): the handle never names a concrete
// implementation and never leaks one to the consumer.
type Datastore struct {
	mu        sync.RWMutex
	backend   Backend
	thoughts  Thoughts
	toolState ToolState
	memory    Memory
	claimed   bool
}

// NewDatastore returns an unbound handle. It takes no backend: which
// backend serves it is a runtime decision made at assembly.
func NewDatastore() *Datastore { return &Datastore{} }

// Bind resolves the handle to the active backend's views. Assembly calls
// it once, after Resolve and Open.
func (d *Datastore) Bind(b Backend) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.backend = b
	d.thoughts = b.Thoughts()
	d.toolState = b.ToolState()
	d.memory = b.Memory()
}

// Bound reports whether a backend is bound.
func (d *Datastore) Bound() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.backend != nil
}

// Claimed reports whether any view has been requested from the handle.
// Assembly uses it to tell "no datastore is declared and nobody wants
// one" from "a consumer holds a handle that has nothing to bind to": the
// latter fails at Run.
func (d *Datastore) Claimed() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.claimed
}

// Thoughts returns the thought/transcript view. Calling it claims the
// handle.
func (d *Datastore) Thoughts() Thoughts {
	d.claim()
	return thoughtsHandle{d}
}

// Stateful returns the stateful tool state view. Calling it claims the
// handle. A tool factory takes this at construction:
//
//	todo.Tools(naviID, ds.Stateful())
func (d *Datastore) Stateful() ToolState {
	d.claim()
	return toolStateHandle{d}
}

// Memory returns the memory view. Calling it claims the handle.
func (d *Datastore) Memory() Memory {
	d.claim()
	return memoryHandle{d}
}

func (d *Datastore) claim() {
	d.mu.Lock()
	d.claimed = true
	d.mu.Unlock()
}

func (d *Datastore) thoughtsView() (Thoughts, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.thoughts == nil {
		return nil, ErrUnbound
	}
	return d.thoughts, nil
}

func (d *Datastore) toolStateView() (ToolState, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.toolState == nil {
		return nil, ErrUnbound
	}
	return d.toolState, nil
}

func (d *Datastore) memoryView() (Memory, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.memory == nil {
		return nil, ErrUnbound
	}
	return d.memory, nil
}

// ── view proxies ────────────────────────────────────────────────────────
//
// A view is handed out at declaration, before the active backend is
// known, so each call resolves through the handle. That indirection is
// what lets the same view keep working after a backend swap.

type thoughtsHandle struct{ d *Datastore }

func (h thoughtsHandle) SaveThought(t *thought.Thought) error {
	v, err := h.d.thoughtsView()
	if err != nil {
		return err
	}
	return v.SaveThought(t)
}

func (h thoughtsHandle) LoadThoughts() ([]*thought.Thought, error) {
	v, err := h.d.thoughtsView()
	if err != nil {
		return nil, err
	}
	return v.LoadThoughts()
}

func (h thoughtsHandle) LoadThought(id string) (*thought.Thought, error) {
	v, err := h.d.thoughtsView()
	if err != nil {
		return nil, err
	}
	return v.LoadThought(id)
}

func (h thoughtsHandle) DeleteThought(id string) error {
	v, err := h.d.thoughtsView()
	if err != nil {
		return err
	}
	return v.DeleteThought(id)
}

func (h thoughtsHandle) SaveTranscript(thoughtID string, entries []Entry) error {
	v, err := h.d.thoughtsView()
	if err != nil {
		return err
	}
	return v.SaveTranscript(thoughtID, entries)
}

func (h thoughtsHandle) LoadTranscript(thoughtID string) ([]Entry, error) {
	v, err := h.d.thoughtsView()
	if err != nil {
		return nil, err
	}
	return v.LoadTranscript(thoughtID)
}

func (h thoughtsHandle) TranscriptInfo(thoughtID string) (TranscriptInfo, error) {
	v, err := h.d.thoughtsView()
	if err != nil {
		return TranscriptInfo{}, err
	}
	return v.TranscriptInfo(thoughtID)
}

func (h thoughtsHandle) DeleteTranscript(thoughtID string) error {
	v, err := h.d.thoughtsView()
	if err != nil {
		return err
	}
	return v.DeleteTranscript(thoughtID)
}

type toolStateHandle struct{ d *Datastore }

func (h toolStateHandle) ListTodos(naviID string, status TodoStatus, limit int) ([]Todo, error) {
	v, err := h.d.toolStateView()
	if err != nil {
		return nil, err
	}
	return v.ListTodos(naviID, status, limit)
}

func (h toolStateHandle) ReplaceTodos(naviID string, items []Todo) ([]Todo, error) {
	v, err := h.d.toolStateView()
	if err != nil {
		return nil, err
	}
	return v.ReplaceTodos(naviID, items)
}

func (h toolStateHandle) UpdateTodos(naviID string, updates []TodoUpdate) ([]Todo, error) {
	v, err := h.d.toolStateView()
	if err != nil {
		return nil, err
	}
	return v.UpdateTodos(naviID, updates)
}

func (h toolStateHandle) ReadNotes(naviID string, q NoteQuery) ([]Note, error) {
	v, err := h.d.toolStateView()
	if err != nil {
		return nil, err
	}
	return v.ReadNotes(naviID, q)
}

func (h toolStateHandle) UpsertNote(naviID string, n Note) (Note, error) {
	v, err := h.d.toolStateView()
	if err != nil {
		return Note{}, err
	}
	return v.UpsertNote(naviID, n)
}

func (h toolStateHandle) RemoveNote(naviID, id string) error {
	v, err := h.d.toolStateView()
	if err != nil {
		return err
	}
	return v.RemoveNote(naviID, id)
}

type memoryHandle struct{ d *Datastore }

func (h memoryHandle) Remember(rec MemoryRecord) (MemoryRecord, error) {
	v, err := h.d.memoryView()
	if err != nil {
		return MemoryRecord{}, err
	}
	return v.Remember(rec)
}

func (h memoryHandle) Recall(q RecallQuery) ([]MemoryRecord, error) {
	v, err := h.d.memoryView()
	if err != nil {
		return nil, err
	}
	return v.Recall(q)
}

func (h memoryHandle) Forget(id string) error {
	v, err := h.d.memoryView()
	if err != nil {
		return err
	}
	return v.Forget(id)
}
