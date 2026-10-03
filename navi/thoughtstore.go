package navi

import (
	"errors"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// datastoreThoughtStore adapts the datastore onto the thought.Store the
// queue persists through. Writes are staged on the datastore's ordered
// writer, so the queue's flusher is a producer rather than a second writer
// (ADR 0011 decision 13). Reads go to the bound backend.
//
// The queue is constructed in New, before assembly resolves which datastore
// candidate is active, so before Bind the adapter falls back to an
// in-memory store — a navi that declares no datastore still runs, with
// volatile thoughts. The runtime calls thought.Queue.Recover at assembly to
// adopt work persisted in a previous process once the datastore is bound.
type datastoreThoughtStore struct {
	handle *datastore.Datastore
	mem    *thought.MemoryStore
}

func newDatastoreThoughtStore(handle *datastore.Datastore) *datastoreThoughtStore {
	return &datastoreThoughtStore{handle: handle, mem: thought.NewMemoryStore()}
}

// SaveThought is the durability path (park checkpoint, delete): it waits
// for the write to be applied, ordered after queued work.
func (s *datastoreThoughtStore) SaveThought(t *thought.Thought) error {
	if !s.handle.Bound() {
		return s.mem.SaveThought(t)
	}
	return s.handle.SaveThought(t)
}

// EnqueueThought is the non-blocking path the queue's flusher prefers: the
// write is staged on the ordered writer and drained asynchronously.
func (s *datastoreThoughtStore) EnqueueThought(t *thought.Thought) error {
	if !s.handle.Bound() {
		return s.mem.SaveThought(t)
	}
	s.handle.EnqueueThought(t)
	return nil
}

func (s *datastoreThoughtStore) LoadThoughts() ([]*thought.Thought, error) {
	if !s.handle.Bound() {
		return s.mem.LoadThoughts()
	}
	loaded, err := s.handle.LoadThoughts()
	if errors.Is(err, datastore.ErrUnbound) {
		return s.mem.LoadThoughts()
	}
	return loaded, err
}

func (s *datastoreThoughtStore) LoadThought(id string) (*thought.Thought, error) {
	if !s.handle.Bound() {
		return s.mem.LoadThought(id)
	}
	t, err := s.handle.LoadThought(id)
	if errors.Is(err, datastore.ErrUnbound) {
		return s.mem.LoadThought(id)
	}
	return t, err
}

func (s *datastoreThoughtStore) DeleteThought(id string) error {
	if !s.handle.Bound() {
		return s.mem.DeleteThought(id)
	}
	return s.handle.DeleteThought(id)
}
