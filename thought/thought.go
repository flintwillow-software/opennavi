// Package thought is the public thought contract (ADR 0003: the
// thought system is first-class; ADR 0004: contract packages are
// public and dependency-light). This package defines the Thought
// value, the ThoughtSystem handle components operate through, and the
// event stream they observe — interfaces only, stdlib-only. M3
// promotes internal/thought onto this contract; until then the two
// coexist and nothing may import this package from the runtime. The
// Thought value mirrors the runtime thought's consumer-relevant
// fields so the promotion is mechanical.
package thought

import (
	"errors"
	"time"
)

// Thought is one piece of the navi's reasoning queue: a discrete task
// with a lifecycle, durable overrides, and metadata.
type Thought struct {
	// ID is stable per thought.
	ID string
	// Prompt is the reasoning task / goal.
	Prompt string
	// Context is supporting context (steers append here).
	Context string
	// Result is the completion message: the outcome reported when the
	// thought reaches "complete" (empty until then, and legitimately
	// empty for a thought that produced no visible content). Deliberately
	// a plain string for now — a real agent produces tool calls and
	// multi-turn state, not one line, so expect this to be reshaped.
	Result string
	// Metadata is arbitrary key-value metadata (origin tagging, ...).
	Metadata map[string]string
	// Priority ranges 1..10 (1 = highest).
	Priority int
	// Status is the lifecycle state ("pending" | "running" |
	// "paused" | "complete" | "failed" | "cancelled").
	Status string
	// Provider / Model are per-thought overrides (empty = runtime
	// default; ADR 0009).
	Provider string
	Model    string
	// Mode names the capability profile this thought runs under
	// (empty = the navi's default; ADR 0007). The composition root
	// validates it against the declared Modes at Run.
	Mode string
	// DeferUntil makes the thought unclaimable before this time
	// (zero = claimable now); durable.
	DeferUntil time.Time
	// SessionID links the thought to its persisted session transcript
	// (when one exists).
	SessionID string
	// Foreground marks session-created thoughts that claim the
	// reserved foreground slot.
	Foreground bool
	// CreatedAt / UpdatedAt are store-managed timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectivePriority clamps Priority to 1..10.
func (t *Thought) EffectivePriority() int {
	if t.Priority < 1 {
		return 1
	}
	if t.Priority > 10 {
		return 10
	}
	return t.Priority
}

// EventKind enumerates thought-system events.
type EventKind string

const (
	EventEnqueued  EventKind = "enqueued"
	EventStarted   EventKind = "started"
	EventPaused    EventKind = "paused"
	EventResumed   EventKind = "resumed"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
	EventCancelled EventKind = "cancelled"
	EventPreempted EventKind = "preempted"
	// EventMessageRouted is the routing delivery ack (S010312),
	// carrying the injection path in Delivery.
	EventMessageRouted EventKind = "message_routed"
)

// Event is one observation on the thought-system stream.
type Event struct {
	Kind      EventKind
	ThoughtID string
	Detail    string
	// Delivery is the routing path for message_routed acks; empty
	// otherwise.
	Delivery string
	At       time.Time
}

// ErrThoughtNotFound is returned when a thought id does not exist.
var ErrThoughtNotFound = errors.New("thought not found")

// ThoughtSystem is the handle components (connectors, portals)
// operate through: scheduling, steering, lifecycle controls, and
// observation. A navi satisfies it structurally (ADR 0004 rule 2);
// the contract never names the composition root.
type ThoughtSystem interface {
	// Enqueue schedules a thought (deferral rides the Thought).
	Enqueue(t *Thought) error
	// RouteMessage delivers a message to a thought (S010312
	// lifecycle-dependent delivery).
	RouteMessage(id, text string) error
	Pause(id string) error
	Resume(id string) error
	Cancel(id string) error
	// BumpPriority changes a queued thought's priority.
	BumpPriority(id string, prio int) error
	// Retry re-queues a failed thought.
	Retry(id string) error
	// Complete reports a thought's outcome: it moves a running thought to
	// "complete" and records result as its completion message. Reporting
	// is not claiming — a component cannot take work off the queue with
	// this, only close out a thought it already has the id for.
	Complete(id string, result string) error
	// Fail reports a thought's failure, with a detail message.
	Fail(id string, detail string) error
	// GetThought returns a copy of one thought.
	GetThought(id string) (*Thought, error)
	// GetThoughts returns the full queue snapshot (copies).
	GetThoughts() []*Thought
	// OnEvent registers a queue-event observer.
	OnEvent(fn func(Event))
}
