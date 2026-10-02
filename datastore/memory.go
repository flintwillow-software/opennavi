package datastore

import (
	"errors"
	"time"
)

// ErrMemoryNotFound is returned when a memory id does not exist.
var ErrMemoryNotFound = errors.New("datastore: memory not found")

// Scope is a memory ownership scope (docs/architecture/memory.md). The
// scope names who owns an entry; load timing — always in context versus
// searched on demand — is the separate tier axis and is not part of
// storage.
type Scope string

const (
	// ScopeSession is working state for one interaction.
	ScopeSession Scope = "session"
	// ScopeUser holds notes about a human, always injected.
	ScopeUser Scope = "user"
	// ScopeNavi holds notes about another navi.
	ScopeNavi Scope = "navi"
	// ScopeMemories is the searched long-term knowledge archive.
	ScopeMemories Scope = "memories"
)

// MemoryRecord is one persisted memory entry. Memory is inherent to the
// navi; the datastore translates it to whichever persistence layer is
// active and never interprets it.
type MemoryRecord struct {
	// ID is the stable entry id; empty on create means assign a new one.
	ID string
	// Scope is the ownership scope.
	Scope Scope
	// Subject is the in-scope subject (a user or navi identity, or the
	// session id). Empty means the scope has no subject.
	Subject string
	// Content is the entry body.
	Content string
	// Source records where the entry came from, when known.
	Source string
	// Tags are free-form labels; Recall filters on them exactly.
	Tags []string
	// Importance ranks entries for recall (0..1); zero is the backend
	// default.
	Importance float64
	// ExpiresAt is the optional expiry (zero = never).
	ExpiresAt time.Time
	// CreatedAt / UpdatedAt are store-managed timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RecallQuery selects memory records. Zero fields are ignored; Query is a
// literal substring match on Content unless a backend supports more.
type RecallQuery struct {
	// Scope filters by ownership scope.
	Scope Scope
	// Subject filters by in-scope subject.
	Subject string
	// Query is a literal substring match on Content.
	Query string
	// Tags require every listed tag.
	Tags []string
	// Limit caps the result count (0 = backend default).
	Limit int
}

// Memory is the memory aggregate view. Both memory tiers are persisted
// through it, and a navi that declares memory without a datastore fails
// at assembly rather than silently keeping memory volatile (ADR 0011
// decision 9).
type Memory interface {
	// Remember stores a record, assigning an id when empty, and returns
	// the persisted record.
	Remember(rec MemoryRecord) (MemoryRecord, error)
	// Recall returns records matching the query, most relevant first.
	Recall(q RecallQuery) ([]MemoryRecord, error)
	// Forget deletes the record with the given id; unknown ids return
	// ErrMemoryNotFound (wrapped).
	Forget(id string) error
}
