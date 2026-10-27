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

	"github.com/flintwillow-software/opennavi/principal"
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
	// Priority ranges 0..10 (0 = highest, ascending in urgency as the
	// number falls; ADR 0035 decision 1). The named bands are 0 = urgent
	// (a preemption cancels), 1 = "respond to me", 2+ = planned work (a
	// preemption steers).
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
	// DependsOn names the thoughts this one waits for: the thought is
	// queued immediately but is not claimable until every id here has
	// reached "complete". It is how a plan of thoughts keeps its order
	// with more than one worker and with preemption in play — priority
	// alone cannot express "after", because self-scheduled work is
	// clamped to one band. Validated at the door (ids must already
	// exist, no self-dependency, no cycles) and persisted with the row.
	// "Blocked" is derived from this field, never a lifecycle status.
	DependsOn []string
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
	// SteerRequested is set by the queue when a strictly-higher-priority
	// thought preempts this running thought in the steering band (priority
	// ≥1, ADR 0035 decision 3). Unlike PreemptRequested it is advisory: the
	// victim is TOLD to put its work down at the turn boundary and chooses
	// its stopping point. It is never paired with a context cancellation —
	// steering is the alternative to cancellation, not a prelude to it — so
	// a victim that ignores the steer keeps running (a guidance failure,
	// owned by the core thoughts skill, not a fallback cancel).
	SteerRequested bool
	// PreemptedBy names the thought whose arrival parked this one, when the
	// park was a preemption (ADR 0035 decision 7). It is the release edge:
	// when that preemptor reaches a terminal state the victim is resumed
	// automatically. Empty for a deliberate park (operator, thoughts_update,
	// a shelf), which is never auto-resumed. Latest preemptor wins: a victim
	// parked again by a newer arrival is released by that arrival, not by
	// the whole stack of preemptors it has ever had.
	PreemptedBy string
	// HandoffNote is the runtime-synthesised handoff written at park time
	// (ADR 0035 decision 3): a mechanical note built from the thought's
	// prompt, its context, and whatever result it had reached, so a resumed
	// thought is never contextless. It is a FLOOR, not the mechanism — a
	// thought's own writes are the ceiling — and it is deliberately separate
	// from Context so "what I was told" (the connector's immutable enqueue
	// context) is not conflated with "what I concluded". It is surfaced into
	// the resumed run's prompt.
	HandoffNote string
	// ParkReason records why a paused thought is paused. It is how the queue
	// distinguishes a preemption park (auto-resumable) from a deliberate one
	// (operator, thoughts_update, shelf — never auto-resumed, ADR 0035
	// decision 8). Empty means the park predates this field or the record
	// was never stamped; such a thought is treated as deliberately parked.
	ParkReason ParkReason
	// ParkNote is the STATED REASON a thought was parked, in the words of
	// whoever parked it (ADR 0036 decision 5): "waiting on the vendor",
	// "superseded by NAVI-900", "shelved until the migration lands". It is
	// what makes "I decided to come back to this" recorded rather than
	// inferred from a bare `paused`.
	//
	// It is deliberately distinct from ParkReason (the queue's own enum, which
	// drives auto-resume and is not the thought's to state) and from
	// HandoffNote (what the next run needs to CONTINUE, which is not a reason
	// for stopping). A shelf reason is not a handoff and a handoff is not a
	// reason, so folding them together would lose one of them.
	ParkNote string

	// Origin is the verified principal that CAUSED this thought (ADR 0022
	// decision 2). It is recorded when the thought is enqueued — from the
	// portal boundary's verified actor, or the navi's own service principal
	// for system-originated work — and never from a value the thought author
	// supplied. The zero reference means the runtime has not attributed the
	// thought yet; the navi treats it as system-originated (navi:<name>),
	// which is why fail-closed authorization does not disable the runtime's
	// own machinery (ADR 0021 decision 10, ADR 0022 decision 3).
	Origin principal.Ref
	// OriginInfo records how the originating request arrived, so a policy
	// condition evaluated later in the chain — on a follow-up or a tool call
	// — sees the same facts the binding supplied at the door. Empty for a
	// system-originated thought, which has no request behind it.
	OriginInfo OriginInfo
	// Lineage records the thought-to-thought chain that scheduled this
	// thought (ADR 0022 decision 2): a follow-up appends its parent's id, so
	// "on whose behalf did this happen" is answerable from the record. It is
	// empty for a thought enqueued directly.
	Lineage []string
}

// OriginInfo is how the originating request arrived, as the binding that
// admitted it reported: the transport, the input channel, the source address,
// and the time. It is a fact about the REQUEST, never an identity (ADR 0021
// decision 9) — the identity is Origin.
//
// The fields are deliberately plain strings rather than a shared contract
// type: the thought package stays dependency-light, and the navi maps this
// onto the engine's request context (ADR 0022 decision 1).
//
// The time is NOT authoritative for authorization: NAVI-414 records that a
// deferred thought's clock is the local one. A time-scoped condition is
// evaluated at the point of the decision, not frozen at enqueue.
type OriginInfo struct {
	// Transport names how the request arrived ("lan", "tailnet", "lora",
	// "in-process").
	Transport string
	// Channel names the input channel that carried the intent ("console",
	// "microphone", "sensor", "api").
	Channel string
	// RemoteAddr is the source address when the binding had one.
	RemoteAddr string
	// At is when the request was admitted. Zero when there is none.
	At time.Time
}

// IsZero reports whether no origin facts were recorded.
func (o OriginInfo) IsZero() bool {
	return o.Transport == "" && o.Channel == "" && o.RemoteAddr == "" && o.At.IsZero()
}

// InheritFrom makes t a follow-up of parent (ADR 0022 decision 3): it takes
// the parent's originating principal and request facts, and appends the
// parent's id to the lineage. It does nothing when t already has an origin —
// an explicitly attributed thought is never overwritten — or when parent is
// nil.
func (t *Thought) InheritFrom(parent *Thought) {
	if t == nil || parent == nil || !t.Origin.IsZero() {
		return
	}
	t.Origin = parent.Origin
	t.OriginInfo = parent.OriginInfo
	lineage := make([]string, 0, len(parent.Lineage)+1)
	lineage = append(lineage, parent.Lineage...)
	lineage = append(lineage, parent.ID)
	t.Lineage = lineage
}

// ParkReason records why a thought was parked, so a preemption park (which
// the queue releases automatically when its preemptor finishes) is
// distinguishable from a deliberate one (which nothing may resurrect,
// ADR 0035 decision 8).
type ParkReason string

const (
	// ParkPreempted is a park caused by a strictly-higher-priority arrival.
	// It carries PreemptedBy and is auto-resumed when that preemptor reaches
	// a terminal state.
	ParkPreempted ParkReason = "preempted"
	// ParkDeliberate is any park a person or the navi chose — an operator
	// action, a thoughts_update, or a plan shelving work. It is never
	// auto-resumed: the queue must not resurrect work someone stopped.
	ParkDeliberate ParkReason = "deliberate"
)

// OriginSelf / OriginTool tag thoughts the navi scheduled for itself
// ("origin tagging"): self-directed work is distinguishable
// from connector-sourced work in dashboards, and the queue clamps its
// priority to the conservative band. The queue's spelling is `origin: self`; the
// thought-tool layers tag `origin: tool` — both spellings count as
// self-scheduled.
const OriginSelf = "self"
const OriginTool = "tool"

// Metadata keys and role values the thought system understands. They live
// here so every layer spells them the same way; the metadata map itself
// stays free-form (an author may carry anything).
const (
	// MetadataOrigin is the key whose OriginSelf/OriginTool value tags a
	// thought as self-scheduled (the priority clamp).
	MetadataOrigin = "origin"
	// MetadataRole names what a thought is for, when that is not obvious
	// from its prompt.
	MetadataRole = "role"
	// RoleVerification marks a plan's closing check (ADR 0016): a thought
	// whose job is to verify what a plan produced rather than to extend
	// it. The runtime never queues a verification of a verification, so the
	// tag bounds the plan automatically.
	RoleVerification = "verification"
	// MetadataPlan carries the synthesized verification's plan id.
	MetadataPlan = "plan"
)

// IsVerification reports whether the thought is a plan-closing check
// (metadata role verification, ADR 0016).
func (t *Thought) IsVerification() bool {
	return t != nil && t.Metadata[MetadataRole] == RoleVerification
}

// SelfScheduledHighBound is the default priority floor for self-scheduled
// thoughts ("priority clamp"): any priority above it
// (numerically lower) is clamped down, so self-directed work can never
// preempt external work (preemption needs a strictly-lower priority than
// the victim) or squat the reserved slots unless runtime config grants it.
//
// ADR 0035 decision 5 keeps this clamp unchanged, and ADR 0036 decision 3
// shows it needs no exception: a connector's ingress thought carries
// external origin, so it is not self-scheduled and its hint survives the
// clamp intact. The rule the clamp expresses is "work the navi discovers is
// not more urgent than work it was given".
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
	// ErrDuplicateID is returned by Enqueue when a thought with the same id
	// already exists. An idempotent producer that derives a deterministic id
	// from an external event (a connector's poll loop) treats it as
	// already-handled rather than as a failure. Check errors.Is.
	ErrDuplicateID = errors.New("duplicate ID")
)

// validTransitions is the lifecycle state machine.
var validTransitions = map[ThoughtStatus][]ThoughtStatus{
	// pending → failed is the blocked-dependent path: a pending thought
	// whose dependency failed or was cancelled can never become
	// claimable, so it is failed (retryable once the dependency is
	// retried) rather than left blocked forever.
	ThoughtPending:   {ThoughtRunning, ThoughtFailed, ThoughtCancelled},
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
	if t.DependsOn != nil {
		out.DependsOn = append([]string(nil), t.DependsOn...)
	}
	if t.Lineage != nil {
		out.Lineage = append([]string(nil), t.Lineage...)
	}
	return &out
}

// IsPreemptionPark reports whether the thought is paused because it was
// preempted (as opposed to paused deliberately). Only a preemption park is
// eligible for automatic resumption (ADR 0035 decision 7).
func (t *Thought) IsPreemptionPark() bool {
	return t != nil && t.Status == ThoughtPaused && t.ParkReason == ParkPreempted && t.PreemptedBy != ""
}

// EffectivePriority returns the priority clamped to the 0-10 range. The
// floor is 0, not 1: ADR 0035 decision 1 makes 0 the highest band (urgent),
// and a clamp to 1 would silently promote every p0 into the p1 "respond to
// me" band — collapsing the cancel/steer distinction the scheduler is built
// on. Priority values are not validated elsewhere on the queue path, so an
// unset (zero-valued) Priority reads as p0; callers that want planned work
// must set 2+ explicitly.
func (t *Thought) EffectivePriority() int {
	if t.Priority < 0 {
		return 0
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

// UnmetDependencies returns the ids among t.DependsOn that are not
// complete in the supplied snapshot — the "blocked" derivation. A
// non-empty result means t is waiting on work that has not finished; it
// is a *display* state, not a lifecycle status, so the scheduler and the
// record keep one vocabulary. An id absent from the snapshot is unmet
// (an unresolvable dependency never releases its dependent), which
// surfaces the orphan through blocked_by rather than letting it run.
func UnmetDependencies(t *Thought, all []*Thought) []string {
	if t == nil || len(t.DependsOn) == 0 {
		return nil
	}
	status := make(map[string]ThoughtStatus, len(all))
	for _, o := range all {
		status[o.ID] = o.Status
	}
	var unmet []string
	for _, dep := range t.DependsOn {
		if status[dep] != ThoughtComplete {
			unmet = append(unmet, dep)
		}
	}
	return unmet
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
