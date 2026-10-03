// Package thought is the public thought system (ADR 0003: the thought
// system is first-class; ADR 0004: component packages are public and
// dependency-light).
//
// It holds three things, deliberately together: the Thought value and its
// lifecycle, the ThoughtSystem handle components operate through, and the
// priority Queue that runs them.
//
// The first two are the component interface. The Queue is the *runtime*
// handle: the composition root and its workers hold the concrete type,
// while components receive only ThoughtSystem through Deps — so a
// component cannot take work off the queue, and slot accounting stays out
// of the component surface even though the scheduler lives here.
package thought

import (
	"errors"
	"fmt"
	"time"
)

// ThoughtStatus is the lifecycle state of a thought.
type ThoughtStatus string

const (
	ThoughtPending   ThoughtStatus = "pending"
	ThoughtRunning   ThoughtStatus = "running"
	ThoughtPaused    ThoughtStatus = "paused"
	ThoughtComplete  ThoughtStatus = "complete"
	ThoughtFailed    ThoughtStatus = "failed"
	ThoughtCancelled ThoughtStatus = "cancelled"
)

// Thought is one piece of the navi's reasoning queue: a discrete task
// with a lifecycle, durable overrides, and metadata.
//
// The first group of fields is what a *component* acts on — connectors
// and portals read and set these. The second group is runtime-managed:
// the scheduler owns it, and a component has no business writing it.
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
	// Status is the lifecycle state.
	Status ThoughtStatus
	// Provider / Model are per-thought overrides (empty = the navi
	// default; ADR 0009).
	Provider string
	Model    string
	// Mode names the capability profile this thought runs under (empty =
	// the navi's default; ADR 0007).
	Mode string
	// Skills names the skills this thought loads (ADR 0013 decision 5, the
	// thought-declared path): a thought tells its follow-ups what to load.
	// Names resolve against the compiled-in set; an unknown name is an
	// error, not a fetch.
	Skills []string
	// Assembled is the context the thought was created with (ADR 0013
	// decision 2): the composition root fills it at Enqueue, the runner
	// consumes it instead of rebuilding per turn, and it is recorded as the
	// thought's first transcript entry. Empty when no assembler is
	// installed or nothing was assembled.
	Assembled string
	// DeferUntil is the not-before timestamp: the thought
	// is queued — and persisted — immediately but is not claimable until
	// this time passes. Zero = claimable now. Durability is real state
	// not an in-memory timer: crash recovery preserves it.
	DeferUntil time.Time
	// SessionID links the thought to its persisted transcript, when one
	// exists.
	SessionID string
	// Foreground marks this thought as the binary run's interactive work
	// it may claim the queue's reserved foreground slots and
	// competes by the same priority/preemption rules as everything else.
	Foreground bool
	// CreatedAt / UpdatedAt are store-managed timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time

	// SlotID / WorkerID record the capacity slot and worker holding this
	// thought while it runs. Runtime-managed.
	SlotID   string
	WorkerID string
	// LogPath is where this thought's run log lives, when one is kept.
	LogPath string
	// Checkpoint is the last parked state, written on
	// running→paused so a resumed thought continues rather than restarts
	// (NAVI-338's durability promise: the checkpoint is on disk at park
	// time).
	Checkpoint []byte
	// PreemptRequested is set by the queue when a higher-priority thought
	// needs this running thought's slot. The runner honours it via ctx
	// cancellation: snapshot progress into Checkpoint and return; the
	// worker then parks the thought (running → paused).
	PreemptRequested bool
}

// OriginSelf / OriginTool tag thoughts the navi scheduled for itself
// ("origin tagging"): self-directed work is distinguishable
// from connector-sourced work in dashboards, and the queue clamps its
// priority to the conservative band. The queue's spelling is `origin: self`; the
// thought-tool layers tag `origin: tool` — both spellings count as
// self-scheduled.
const OriginSelf = "self"
const OriginTool = "tool"

// SelfScheduledHighBound is the default priority floor for self-scheduled
// thoughts ("priority clamp"): any priority above it
// (numerically lower) is clamped down, so self-directed work can never
// preempt external work (preemption needs priority ≤
// HighPriorityThreshold) or squat the reserved slots unless runtime
// config grants it.
const SelfScheduledHighBound = 5

// Sentinel lifecycle errors.
var (
	// ErrInvalidTransition is returned by Transition for a move the
	// lifecycle state machine forbids.
	ErrInvalidTransition = errors.New("invalid thought transition")
	// ErrThoughtNotFound is returned when a thought id does not exist.
	ErrThoughtNotFound = errors.New("thought not found")
	// ErrThoughtProtected wraps delete rejections for thoughts whose
	// lifecycle forbids removal (running, terminal). Check errors.Is.
	ErrThoughtProtected = errors.New("thought protected from deletion")
)

// validTransitions is the lifecycle state machine.
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
// slot is allocated ("resume requested, slot available").

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
	if t.Skills != nil {
		out.Skills = append([]string(nil), t.Skills...)
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

// EffectiveSelfPriority clamps a self-scheduled thought's priority to the
// conservative band: the navi cannot schedule itself ahead
// of external work by default. The queue applies this inside Enqueue for
// origin-tagged thoughts; the tool layer may also pre-clamp before
// enqueue.
func (t *Thought) EffectiveSelfPriority() int {
	p := t.EffectivePriority()
	if p < SelfScheduledHighBound {
		return SelfScheduledHighBound
	}
	return p
}

// Due reports whether the thought is claimable by the pool at time now:
// no deferral, or the not-before time has passed.
func (t *Thought) Due(now time.Time) bool {
	if t.DeferUntil.IsZero() {
		return true
	}
	return !t.DeferUntil.After(now)
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
	// EventMessageRouted is the routing delivery ack,
	// carrying the injection path in Delivery.
	EventMessageRouted EventKind = "message_routed"
	// EventDeleted fires when a thought record is removed entirely
	// (Queue.Delete) — distinct from EventCancelled:
	// removal is not cancellation, and the audit/ack trail
	// distinguishes them. Detail carries the PRIOR status so metrics
	// observers (which only see events) can decrement the right
	// by_status gauge.
	EventDeleted EventKind = "deleted"
	// EventPersistFailed fires when a worker cannot persist a thought
	// transition — in-memory and stored state may have diverged.
	EventPersistFailed EventKind = "persist_failed"
)

// Event is one observation on the thought-system stream.
type Event struct {
	Kind      EventKind
	ThoughtID string
	Detail    string
	// Delivery is the routing path for message_routed acks
	// ("context-append" | "steering" | "resume-with-message" | "").
	// Empty on lifecycle events.
	Delivery string
	At       time.Time
}

// ThoughtSystem is the handle components (connectors, portals) operate
// through: scheduling, steering, lifecycle controls, and observation. A
// navi satisfies it structurally (ADR 0004 rule 2); the interface never
// names the composition root.
//
// Everything here is *control* or *reporting*. Nothing here claims work:
// taking the next claimable thought is deliberately absent, because a
// component that can take work off the queue has become a worker. That
// lives on the concrete Queue, which only the runtime holds.
type ThoughtSystem interface {
	// Enqueue schedules a thought (deferral rides the Thought).
	Enqueue(t *Thought) error
	// RouteMessage delivers a message to a thought
	// (lifecycle-dependent delivery).
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
	// this, only close out a thought it already holds the id for.
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
