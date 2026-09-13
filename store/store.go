package store

import (
	"sync"
	"time"
)

// ListOptions filters session listings (spec §04).
type ListOptions struct {
	Profile string
	AgentID string
	Status  Status
	Tags    []string
	Since   time.Time
	Limit   int
	Offset  int
}

// SessionInfo is the list-view projection of a session.
type SessionInfo struct {
	ID           string
	Name         string
	AgentID      string
	MessageCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Status       Status
	Tags         []string
}

// Store persists sessions. The SQLite implementation arrives with
// #153/#163; SaveSession must persist the full session (entries
// included) such that LoadSession round-trips it. Implementations must
// return ErrSessionNotFound (wrapped) for missing IDs.
type Store interface {
	SaveSession(s *Session) error
	LoadSession(id string) (*Session, error)
	DeleteSession(id string) error
	ListSessions(opts ListOptions) ([]SessionInfo, error)
}

// MemoryStore is the in-memory Store implementation. It hands out deep
// copies so callers can't mutate stored state through returned pointers.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]*Session)}
}

// SaveSession persists a deep copy of the session.
func (m *MemoryStore) SaveSession(s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s.Clone()
	return nil
}

// LoadSession returns a deep copy of the stored session.
func (m *MemoryStore) LoadSession(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s.Clone(), nil
}

// DeleteSession removes the session.
func (m *MemoryStore) DeleteSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return ErrSessionNotFound
	}
	delete(m.sessions, id)
	return nil
}

// ListSessions returns SessionInfo projections for sessions matching
// all provided filters, ordered by UpdatedAt descending.
func (m *MemoryStore) ListSessions(opts ListOptions) ([]SessionInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tagSet := make(map[string]bool, len(opts.Tags))
	for _, t := range opts.Tags {
		tagSet[t] = true
	}

	var out []SessionInfo
	for _, s := range m.sessions {
		if opts.Profile != "" && s.Profile != opts.Profile {
			continue
		}
		if opts.AgentID != "" && s.AgentID != opts.AgentID {
			continue
		}
		if opts.Status != "" && s.Status != opts.Status {
			continue
		}
		if !opts.Since.IsZero() && s.UpdatedAt.Before(opts.Since) {
			continue
		}
		if len(tagSet) > 0 {
			match := false
			for _, t := range s.Tags {
				if tagSet[t] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, s.Info())
	}

	// Newest activity first.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.After(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}

	if opts.Offset > 0 {
		if opts.Offset >= len(out) {
			return []SessionInfo{}, nil
		}
		out = out[opts.Offset:]
	}
	if opts.Limit > 0 && opts.Limit < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}
