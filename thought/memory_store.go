package thought

import "sync"

// MemoryStore is the in-memory Store implementation (tests and
// ephemeral use). Stores deep copies so callers can't mutate stored
// state through returned pointers.
type MemoryStore struct {
	mu       sync.RWMutex
	thoughts map[string]*Thought
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{thoughts: make(map[string]*Thought)}
}

// SaveThought persists a deep copy of the thought.
func (m *MemoryStore) SaveThought(t *Thought) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thoughts[t.ID] = t.Clone()
	return nil
}

// LoadThoughts returns deep copies of all persisted thoughts.
func (m *MemoryStore) LoadThoughts() ([]*Thought, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Thought, 0, len(m.thoughts))
	for _, t := range m.thoughts {
		out = append(out, t.Clone())
	}
	return out, nil
}

// LoadThought returns a deep copy of one thought.
func (m *MemoryStore) LoadThought(id string) (*Thought, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.thoughts[id]
	if !ok {
		return nil, ErrThoughtNotFound
	}
	return t.Clone(), nil
}

// DeleteThought removes a thought.
func (m *MemoryStore) DeleteThought(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.thoughts, id)
	return nil
}
