// Package session keeps the session *behavior* internal (manager,
// compaction strategy, agent snapshot sync) per The
// session *data model* — Session, SessionEntry, the Store contract,
// and MemoryStore — is public in github.com/flintwillow-software/
// opennavi/store (NAVI-276); the aliases below keep internal consumers
// and tests unchanged.
package session

import "github.com/flintwillow-software/opennavi/store"

type (
	Status       = store.Status
	Session      = store.Session
	SessionEntry = store.SessionEntry
	Role         = store.Role
	ListOptions  = store.ListOptions
	SessionInfo  = store.SessionInfo
	Store        = store.Store
	MemoryStore  = store.MemoryStore
)

const (
	StatusActive = store.StatusActive
	StatusPaused = store.StatusPaused
	StatusClosed = store.StatusClosed

	RoleUser      = store.RoleUser
	RoleAssistant = store.RoleAssistant
	RoleTool      = store.RoleTool
	RoleSystem    = store.RoleSystem
)

var (
	ErrEntryNotFound   = store.ErrEntryNotFound
	ErrSessionNotFound = store.ErrSessionNotFound
	ErrInvalidStatus   = store.ErrInvalidStatus
)

// NewSession builds an active session (see store.NewSession).
func NewSession(agentID, workDir string) *Session { return store.NewSession(agentID, workDir) }

// NewEntry creates an entry with a fresh UUID (see store.NewEntry).
func NewEntry(role Role, message string) SessionEntry { return store.NewEntry(role, message) }

// NewUUID returns a random RFC 4122 version-4 UUID (see store.NewUUID).
func NewUUID() string { return store.NewUUID() }

// NewMemoryStore creates an empty in-memory store (see store.NewMemoryStore).
func NewMemoryStore() *MemoryStore { return store.NewMemoryStore() }
