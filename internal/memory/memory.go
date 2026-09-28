// Package memory implements the four-scope memory store.
// The session tier maps to the session
// system (#149) and is intentionally absent here; user and navi are
// document scopes (one document per subject); memories is the
// long-term entry archive with FTS5 search and expiry pruning.
package memory

import (
	"database/sql"
	"errors"
	"time"
)

// Scope is one of the memory tiers servable by this store.
// The session tier is deliberately absent: it is the #149 session
// system.
type Scope string

const (
	ScopeUser     Scope = "user"
	ScopeNavi     Scope = "navi"
	ScopeMemories Scope = "memories"
)

// Valid reports whether the scope is servable by this store.
func (s Scope) Valid() bool {
	return s == ScopeUser || s == ScopeNavi || s == ScopeMemories
}

// Entry is one memory record. For user/navi scopes it represents the
// whole document for a subject (one markdown file per subject —
// here one row); for the memories scope it is one archive entry with
// metadata.
type Entry struct {
	ID         string
	Scope      Scope
	Subject    string // user id / navi name / entry slug
	Content    string // document body (user/navi) or entry text (memories)
	Source     string // session_<id> | user_memory | explicit_add | auto_store
	Tags       []string
	Importance float64    // 0..1
	ExpiresAt  *time.Time // nil = permanent
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Sentinel errors.
var (
	ErrNotFound     = errors.New("memory not found")
	ErrInvalidScope = errors.New("invalid memory scope")
)

// Store is the memory store over the shared SQLite database
// (migration v4; FTS5 for the memories tier per #224's pattern).
type Store struct {
	db *sql.DB
}

// New wraps an existing connection (migrations must be applied — see
// sqlite.Open).
func New(db *sql.DB) *Store {
	return &Store{db: db}
}
