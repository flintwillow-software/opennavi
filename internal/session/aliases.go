// Package session keeps the session *behavior* internal (manager,
// compaction strategy, agent snapshot sync) per The
// session *data model* — Session, SessionEntry, the Store interface,
// and MemoryStore — is public in github.com/flintwillow-software/
// opennavi/store (NAVI-276); the aliases below keep internal consumers
// and tests unchanged.
package session

import "github.com/flintwillow-software/opennavi/datastore"

type (
	Status       = datastore.Status
	Session      = datastore.Session
	SessionEntry = datastore.SessionEntry
	Role         = datastore.Role
	ListOptions  = datastore.ListOptions
	SessionInfo  = datastore.SessionInfo
	Store        = datastore.Store
	MemoryStore  = datastore.MemoryStore
)

const (
	StatusActive = datastore.StatusActive
	StatusPaused = datastore.StatusPaused
	StatusClosed = datastore.StatusClosed

	RoleUser      = datastore.RoleUser
	RoleAssistant = datastore.RoleAssistant
	RoleTool      = datastore.RoleTool
	RoleSystem    = datastore.RoleSystem
)

var (
	ErrEntryNotFound   = datastore.ErrEntryNotFound
	ErrSessionNotFound = datastore.ErrSessionNotFound
	ErrInvalidStatus   = datastore.ErrInvalidStatus
)

// NewSession builds an active session (see datastore.NewSession).
func NewSession(agentID, workDir string) *Session { return datastore.NewSession(agentID, workDir) }

// NewEntry creates an entry with a fresh UUID (see datastore.NewEntry).
func NewEntry(role Role, message string) SessionEntry { return datastore.NewEntry(role, message) }

// NewUUID returns a random RFC 4122 version-4 UUID (see datastore.NewUUID).
func NewUUID() string { return datastore.NewUUID() }

// NewMemoryStore creates an empty in-memory store (see datastore.NewMemoryStore).
func NewMemoryStore() *MemoryStore { return datastore.NewMemoryStore() }
