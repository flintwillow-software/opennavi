package datastore

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// ErrTranscriptNotFound is returned when a thought has no persisted
// transcript.
var ErrTranscriptNotFound = errors.New("datastore: transcript not found")

// Role classifies the origin of a transcript entry.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// Entry is one record in a thought's transcript. The retired session
// model's branching/label/fork fields are deliberately absent (ADR 0011
// decision 8): a thought transcript is an ordered conversation, nothing
// more.
type Entry struct {
	// ID is a stable entry id (UUID).
	ID string
	// Time is when the entry was produced.
	Time time.Time
	// Role is the entry's origin.
	Role Role
	// Message is the text content.
	Message string
	// ToolCallID / ToolName / ToolInput carry an assistant tool call or a
	// tool result's call id.
	ToolCallID string
	ToolName   string
	ToolInput  json.RawMessage
	// IsCompacted marks an entry superseded by a compaction summary. Soft
	// -deleted entries are retained, never removed.
	IsCompacted bool
}

// TranscriptInfo is the metadata-only projection of a transcript:
// enough to answer "what thoughts exist, how long is the transcript"
// without loading the body (ADR 0011 decision 5).
type TranscriptInfo struct {
	ThoughtID    string
	MessageCount int
	UpdatedAt    time.Time
}

// NewEntry creates an entry with a fresh id and the current time.
func NewEntry(role Role, message string) Entry {
	return Entry{
		ID:      NewID(),
		Time:    time.Now().UTC(),
		Role:    role,
		Message: message,
	}
}

// NewID returns a random RFC 4122 version-4 UUID.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("datastore: generate uuid: %v", err)) // crypto/rand failure is unrecoverable
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// cloneEntries deep-copies a transcript slice so a queued write owns its
// data and a later caller mutation cannot reach it.
func cloneEntries(in []Entry) []Entry {
	if in == nil {
		return nil
	}
	out := make([]Entry, len(in))
	copy(out, in)
	for i := range out {
		if out[i].ToolInput != nil {
			out[i].ToolInput = append([]byte(nil), out[i].ToolInput...)
		}
	}
	return out
}

// Thoughts is the thought/transcript aggregate view. A thought is the
// persistence aggregate (ADR 0011 decision 8): its transcript is keyed by
// thought id and reached through this view, never through a session
// handle.
//
// The thought-row methods mirror thought.Store exactly, so a datastore
// backend can serve the queue directly once the runtime is wired through
// it (NAVI-374) with no ABI change.
type Thoughts interface {
	// SaveThought persists a thought row.
	SaveThought(t *thought.Thought) error
	// LoadThoughts returns every persisted thought.
	LoadThoughts() ([]*thought.Thought, error)
	// LoadThought returns one thought by id.
	LoadThought(id string) (*thought.Thought, error)
	// DeleteThought removes a thought row.
	DeleteThought(id string) error

	// SaveTranscript replaces the transcript for thoughtID with entries.
	// A thought's transcript is written as a unit, so a caller never needs
	// to read before writing.
	SaveTranscript(thoughtID string, entries []Entry) error
	// LoadTranscript returns the transcript for thoughtID, or
	// ErrTranscriptNotFound when the thought has none.
	LoadTranscript(thoughtID string) ([]Entry, error)
	// TranscriptInfo returns the metadata projection for thoughtID, or
	// ErrTranscriptNotFound when the thought has none.
	TranscriptInfo(thoughtID string) (TranscriptInfo, error)
	// DeleteTranscript removes a thought's transcript.
	DeleteTranscript(thoughtID string) error
}
