package store

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Role classifies the origin of a session entry.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// SessionEntry is one record in a session's conversation history
// (spec 03-session-context/01-session-management.md §01). It mirrors the
// SQLite `messages` row shape (§02) so the SQLite store (#163) can map
// entries to rows directly.
type SessionEntry struct {
	ID   string // UUID
	Time time.Time

	Role    Role   // user, assistant, tool, system
	Message string // text content

	// Tool metadata. Assistant entries may carry a tool call; tool
	// entries carry the result's call ID.
	ToolCallID string
	ToolName   string
	ToolInput  json.RawMessage // JSON-encoded tool call arguments

	// Branching and organization.
	ParentID string // parent entry ID (empty for sequential entries)
	BranchID string // branch identifier
	Label    string // user-assigned label

	// IsCompacted marks a compaction summary entry (original messages
	// are preserved, not deleted — spec §06).
	IsCompacted bool
}

// NewUUID returns a random RFC 4122 version-4 UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("session: generate uuid: %v", err)) // crypto/rand failure is unrecoverable
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewEntry creates an entry with a fresh UUID and the current timestamp.
func NewEntry(role Role, message string) SessionEntry {
	return SessionEntry{
		ID:      NewUUID(),
		Time:    time.Now().UTC(),
		Role:    role,
		Message: message,
	}
}
