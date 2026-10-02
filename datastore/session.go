package datastore

import (
	"errors"
	"fmt"
	"time"
)

// Status is the lifecycle state of a session (issue #149: create, pause,
// resume, close).
type Status string

const (
	StatusActive Status = "active"
	StatusPaused Status = "paused"
	StatusClosed Status = "closed"
)

// Session is the primary unit of conversation: an ordered sequence of
// entries with branching support.
type Session struct {
	ID      string
	Name    string
	AgentID string
	Profile string
	WorkDir string

	ParentID string // parent session ID (empty for root)
	BranchID string
	Label    string

	Status Status

	Entries []SessionEntry
	Current int // index of the current entry (branching position)

	CreatedAt time.Time
	UpdatedAt time.Time

	Tags []string
}

// NewSession builds an active session with a fresh UUID. Used by
// SessionManager.CreateSession; exported for direct construction in
// tests and tooling.
func NewSession(agentID, workDir string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        NewUUID(),
		AgentID:   agentID,
		WorkDir:   workDir,
		Status:    StatusActive,
		Current:   -1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AppendEntry adds an entry to the sequence, filling in the ID and
// timestamp when unset, and advances Current.
func (s *Session) AppendEntry(entry SessionEntry) {
	if entry.ID == "" {
		entry.ID = NewUUID()
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	s.Entries = append(s.Entries, entry)
	s.Current = len(s.Entries) - 1
	s.UpdatedAt = time.Now().UTC()
}

// GetEntry returns the entry with the given ID.
func (s *Session) GetEntry(id string) (*SessionEntry, error) {
	for i := range s.Entries {
		if s.Entries[i].ID == id {
			return &s.Entries[i], nil
		}
	}
	return nil, fmt.Errorf("session %s: entry %s: %w", s.ID, id, ErrEntryNotFound)
}

// SetCurrent moves the branching position to the given index. The next
// fork from this session copies entries up to the new Current.
func (s *Session) SetCurrent(index int) error {
	if index < 0 || index >= len(s.Entries) {
		return fmt.Errorf("session %s: entry index %d out of range (0..%d)", s.ID, index, len(s.Entries)-1)
	}
	s.Current = index
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// LabelEntry sets a user label on the entry with the given ID.
func (s *Session) LabelEntry(id, label string) error {
	entry, err := s.GetEntry(id)
	if err != nil {
		return err
	}
	entry.Label = label
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// GetPathToRoot returns the active path: entries from the start of the
// session through the current position, in chronological order. Forked
// entries after Current are not part of the active path.
func (s *Session) GetPathToRoot() []SessionEntry {
	if len(s.Entries) == 0 || s.Current < 0 {
		return nil
	}
	end := s.Current
	if end >= len(s.Entries) {
		end = len(s.Entries) - 1
	}
	path := make([]SessionEntry, end+1)
	copy(path, s.Entries[:end+1])
	return path
}

// Clone returns a deep copy of the session (used by forking and by
// stores that must not hand out internal state).
func (s *Session) Clone() *Session {
	out := *s
	out.Entries = make([]SessionEntry, len(s.Entries))
	copy(out.Entries, s.Entries)
	out.Tags = append([]string(nil), s.Tags...)
	return &out
}

// MessageCount returns the number of entries.
func (s *Session) MessageCount() int { return len(s.Entries) }

// Info renders the list-view projection of the session.
func (s *Session) Info() SessionInfo {
	return SessionInfo{
		ID:           s.ID,
		Name:         s.Name,
		AgentID:      s.AgentID,
		MessageCount: len(s.Entries),
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
		Status:       s.Status,
		Tags:         append([]string(nil), s.Tags...),
	}
}

// Sentinel errors for session operations.
var (
	ErrEntryNotFound   = errors.New("entry not found")
	ErrSessionNotFound = errors.New("session not found")
	ErrInvalidStatus   = errors.New("invalid status transition")
)
