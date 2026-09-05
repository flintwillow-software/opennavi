// Package thought implements the thought queue for daemon navis (spec
// 01-agent-runtime/00-thought-queue.md): priority scheduling, slot
// management, lifecycle, persistence, and crash recovery. A thought is
// a discrete reasoning task; the queue is the navi's short-term memory.
package thought

import (
	"errors"
	"fmt"
	"time"
)

// ThoughtStatus is the lifecycle state of a thought (spec §2.1).
type ThoughtStatus string

const (
	ThoughtPending   ThoughtStatus = "pending"
	ThoughtRunning   ThoughtStatus = "running"
	ThoughtPaused    ThoughtStatus = "paused"
	ThoughtComplete  ThoughtStatus = "complete"
	ThoughtFailed    ThoughtStatus = "failed"
	ThoughtCancelled ThoughtStatus = "cancelled"
)

// Thought is a discrete reasoning task with a defined lifecycle (spec
// §2.1).
type Thought struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time

	Prompt   string            // the reasoning task / goal
	Context  string            // supporting context
	Metadata map[string]string // arbitrary key-value metadata

	Priority int // 1 (highest) to 10 (lowest)

	Status    ThoughtStatus
	SessionID string // associated LLM session (if running)

	SlotID   string // which slot this thought occupies (if running)
	WorkerID string // which worker is processing this (if running)

	LogPath    string
	Checkpoint []byte // last checkpoint state (for resume)

	// PreemptRequested is set by the queue when a higher-priority
	// thought needs this running thought's slot. The runner honours it
	// via ctx cancellation: snapshot progress into Checkpoint and
	// return; the worker then parks the thought (running → paused).
	PreemptRequested bool
}

// Sentinel lifecycle errors.
var (
	ErrInvalidTransition = errors.New("invalid thought transition")
	ErrThoughtNotFound   = errors.New("thought not found")
)

// validTransitions is the lifecycle state machine (spec §2.2).
var validTransitions = map[ThoughtStatus][]ThoughtStatus{
	ThoughtPending:   {ThoughtRunning, ThoughtCancelled},
	ThoughtRunning:   {ThoughtComplete, ThoughtPaused, ThoughtFailed},
	ThoughtPaused:    {ThoughtRunning, ThoughtPending, ThoughtCancelled},
	ThoughtFailed:    {ThoughtPending}, // retry
	ThoughtComplete:  {},
	ThoughtCancelled: {},
}

// Note on paused → pending: resume re-queues a paused thought; the
// pending → running transition happens when a worker claims it and a
// slot is allocated (spec §2.2 "resume requested, slot available").

// Transition validates and applies a status change.
func (t *Thought) Transition(to ThoughtStatus) error {
	for _, allowed := range validTransitions[t.Status] {
		if allowed == to {
			t.Status = to
			t.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return fmt.Errorf("%w: thought %s: %s -> %s", ErrInvalidTransition, t.ID, t.Status, to)
}

// CanRetry reports whether a failed thought may be re-queued.
func (t *Thought) CanRetry() bool { return t.Status == ThoughtFailed }

// Retry returns a failed thought to pending for re-acquisition.
func (t *Thought) Retry() error {
	if !t.CanRetry() {
		return fmt.Errorf("%w: thought %s: %s -> pending (only failed thoughts retry)", ErrInvalidTransition, t.ID, t.Status)
	}
	t.Status = ThoughtPending
	t.SlotID = ""
	t.WorkerID = ""
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Clone returns a deep copy — safe to hand to callers while the queue
// keeps mutating the original (GetThought/GetThoughts use this).
func (t *Thought) Clone() *Thought {
	out := *t
	if t.Metadata != nil {
		out.Metadata = make(map[string]string, len(t.Metadata))
		for k, v := range t.Metadata {
			out.Metadata[k] = v
		}
	}
	if t.Checkpoint != nil {
		out.Checkpoint = append([]byte(nil), t.Checkpoint...)
	}
	return &out
}

// EffectivePriority returns the priority clamped to the 1-10 range.
func (t *Thought) EffectivePriority() int {
	if t.Priority < 1 {
		return 1
	}
	if t.Priority > 10 {
		return 10
	}
	return t.Priority
}
