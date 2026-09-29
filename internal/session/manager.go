package session

import (
	"fmt"
	"time"
)

// SessionManager manages the session lifecycle on top of a Store
// (, narrowed to the #149 scope: create, pause, resume, close).
type SessionManager struct {
	store Store
}

// NewSessionManager creates a manager backed by the given store.
func NewSessionManager(store Store) *SessionManager {
	if store == nil {
		panic("session: NewSessionManager requires a non-nil store")
	}
	return &SessionManager{store: store}
}

// CreateSession creates a new active session for the agent, persists it,
// and returns it.
func (m *SessionManager) CreateSession(agentID, workDir string) (*Session, error) {
	s := NewSession(agentID, workDir)
	if err := m.store.SaveSession(s); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return s, nil
}

// SaveSession persists mutations made to a session (entries, labels,
// current position, ...).
func (m *SessionManager) SaveSession(s *Session) error {
	s.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveSession(s); err != nil {
		return fmt.Errorf("save session %s: %w", s.ID, err)
	}
	return nil
}

// LoadSession loads a session by ID.
func (m *SessionManager) LoadSession(id string) (*Session, error) {
	s, err := m.store.LoadSession(id)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", id, err)
	}
	return s, nil
}

// ForkSession creates a child session branching from the parent at the
// parent's Current position: entries up to Current are
// copied; the child gets parent_id linkage and independent history
// afterwards. The fork point entry also becomes the child's ParentID
// entry linkage.
func (m *SessionManager) ForkSession(parentID string) (*Session, error) {
	parent, err := m.LoadSession(parentID)
	if err != nil {
		return nil, err
	}
	if parent.Current < 0 || parent.Current >= len(parent.Entries) {
		return nil, fmt.Errorf("fork session %s: no current entry to fork at", parentID)
	}
	fork := NewSession(parent.AgentID, parent.WorkDir)
	fork.ParentID = parent.ID
	fork.BranchID = NewUUID()
	fork.Name = parent.Name
	fork.Profile = parent.Profile
	fork.Tags = append([]string(nil), parent.Tags...)

	cut := parent.Current + 1
	fork.Entries = make([]SessionEntry, cut)
	copy(fork.Entries, parent.Entries[:cut])
	fork.Current = cut - 1

	if err := m.store.SaveSession(fork); err != nil {
		return nil, fmt.Errorf("fork session %s: %w", parentID, err)
	}
	return fork, nil
}

// PauseSession marks a session paused. A running agent's conversation
// can be captured into the session beforehand via SyncFromAgent; pause
// itself only transitions status and persists. Resuming is
// ResumeSession; the captured entries are what a resumed agent restores
// from.
func (m *SessionManager) PauseSession(id string) (*Session, error) {
	return m.transition(id, StatusActive, StatusPaused)
}

// ResumeSession moves a paused session back to active.
func (m *SessionManager) ResumeSession(id string) (*Session, error) {
	return m.transition(id, StatusPaused, StatusActive)
}

// CloseSession closes a session. Closed sessions are retained (history
// remains queryable) but no longer accept lifecycle transitions.
func (m *SessionManager) CloseSession(id string) (*Session, error) {
	return m.transition(id, "", StatusClosed)
}

// transition loads the session, validates and applies the status change,
// and persists. fromStatus "" means any non-closed status is accepted.
func (m *SessionManager) transition(id string, fromStatus, toStatus Status) (*Session, error) {
	s, err := m.LoadSession(id)
	if err != nil {
		return nil, err
	}
	if s.Status == StatusClosed {
		return nil, fmt.Errorf("session %s: %w: cannot transition closed session to %q", id, ErrInvalidStatus, toStatus)
	}
	if fromStatus != "" && s.Status != fromStatus {
		return nil, fmt.Errorf("session %s: %w: status is %q, want %q to transition to %q", id, ErrInvalidStatus, s.Status, fromStatus, toStatus)
	}
	s.Status = toStatus
	s.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveSession(s); err != nil {
		return nil, fmt.Errorf("session %s: persist transition to %q: %w", id, toStatus, err)
	}
	return s, nil
}

// DeleteSession removes a session from the store entirely.
func (m *SessionManager) DeleteSession(id string) error {
	if err := m.store.DeleteSession(id); err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}

// ListSessions lists sessions through the store's filters.
func (m *SessionManager) ListSessions(opts ListOptions) ([]SessionInfo, error) {
	out, err := m.store.ListSessions(opts)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// TagSession adds a tag to a session (session_tags; a tag is
// not duplicated on the same session).
func (m *SessionManager) TagSession(id, tag string) (*Session, error) {
	s, err := m.LoadSession(id)
	if err != nil {
		return nil, err
	}
	for _, t := range s.Tags {
		if t == tag {
			return s, nil
		}
	}
	s.Tags = append(s.Tags, tag)
	if err := m.SaveSession(s); err != nil {
		return nil, err
	}
	return s, nil
}
