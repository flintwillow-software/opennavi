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

	// DeferUntil is the not-before timestamp (S130600 §02): the
	// thought is queued — and persisted — immediately but is not
	// claimable by the pool until this time passes. Zero = claimable
	// now. Deferral is durable state (S010002), not an in-memory
	// timer: crash recovery preserves it.
	DeferUntil time.Time

	// Provider / Model are optional per-thought overrides (S010104):
	// when Provider is non-empty the runner resolves THAT provider
	// (unknown names fail the thought with a clear error — no silent
	// fallback); Model (when set) overrides the agent model for the
	// run. Empty = the runtime primary + fallback chain / runtime
	// model. Both are durable (S010002).
	Provider string
	Model    string

	Status    ThoughtStatus
	SessionID string // associated LLM session (if running)

	SlotID   string // which slot this thought occupies (if running)
	WorkerID string // which worker is processing this (if running)

	LogPath    string
	Checkpoint []byte // last checkpoint state (for resume)

	// Result is the outcome a runner reports when the thought completes:
	// the completion message. It is deliberately a plain string for now —
	// a real agent produces tool calls and multi-turn state, not one line,
	// so expect this to be reshaped when the agent runner lands. Keeping
	// it here means "what did this thought produce?" is answerable without
	// hunting through a transcript.
	Result string

	// Foreground marks this thought as the binary run's interactive
	// work (S010003): it may claim the queue's reserved foreground
	// slots (Config.ForegroundSlots) and competes by the same
	// priority/preemption rules as everything else.
	Foreground bool

	// PreemptRequested is set by the queue when a higher-priority
	// thought needs this running thought's slot. The runner honours it
	// via ctx cancellation: snapshot progress into Checkpoint and
	// return; the worker then parks the thought (running → paused).
	PreemptRequested bool
}

// OriginSelf / OriginTool tag thoughts the navi scheduled for itself
// (S130600 §1.2 "origin tagging"): self-directed work is distinguishable
// from connector-sourced work in dashboards, and the queue clamps its
// priority to the conservative band. The spec uses `origin: self`; the
// thought-tool layers tag `origin: tool` — both spellings count as
// self-scheduled.
const OriginSelf = "self"
const OriginTool = "tool"

// SelfScheduledHighBound is the default priority floor for
// self-scheduled thoughts (S130600 §1.2 "priority clamp"): any
// priority above it (numerically lower) is clamped down, so
// self-directed work can never preempt external work (preemption
// needs priority ≤ HighPriorityThreshold) or squat the reserved slots
// unless runtime config grants it.
const SelfScheduledHighBound = 5

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

// EffectiveSelfPriority clamps a self-scheduled thought's priority to
// the conservative band (S130600 §1.2): the navi cannot schedule
// itself ahead of external work by default. The queue applies this
// inside Enqueue for origin-tagged thoughts; the tool layer may also
// pre-clamp before enqueue.
func (t *Thought) EffectiveSelfPriority() int {
	p := t.EffectivePriority()
	if p < SelfScheduledHighBound {
		return SelfScheduledHighBound
	}
	return p
}

// Due reports whether the thought is claimable by the pool at time
// now: no deferral, or the not-before time has passed (S130600 §02).
func (t *Thought) Due(now time.Time) bool {
	if t.DeferUntil.IsZero() {
		return true
	}
	return !t.DeferUntil.After(now)
}

// ErrThoughtProtected wraps delete rejections for thoughts whose
// lifecycle forbids removal (running, terminal). Check errors.Is.
var ErrThoughtProtected = errors.New("thought protected from deletion")
