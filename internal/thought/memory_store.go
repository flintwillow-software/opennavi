package thought

import (
	"sync"
)

// MemoryStore is the in-memory Store implementation (tests and
// ephemeral use).
type MemoryStore struct {
	mu       sync.RWMutex
	thoughts map[string]*Thought
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{thoughts: make(map[string]*Thought)}
}

// SaveThought persists a copy of the thought.
func (m *MemoryStore) SaveThought(t *Thought) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.thoughts[t.ID] = &cp
	return nil
}

// LoadThoughts returns copies of all persisted thoughts.
func (m *MemoryStore) LoadThoughts() ([]*Thought, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Thought, 0, len(m.thoughts))
	for _, t := range m.thoughts {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

// LoadThought returns a copy of one thought.
func (m *MemoryStore) LoadThought(id string) (*Thought, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.thoughts[id]
	if !ok {
		return nil, ErrThoughtNotFound
	}
	cp := *t
	return &cp, nil
}

// DeleteThought removes a thought.
func (m *MemoryStore) DeleteThought(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.thoughts, id)
	return nil
}
