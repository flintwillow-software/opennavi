package thought

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SlotExhaustionPolicy determines what happens when all slots are full
type SlotExhaustionPolicy string

const (
	PolicyBlock  SlotExhaustionPolicy = "block"  // block until a slot frees
	PolicyReject SlotExhaustionPolicy = "reject" // reject with an error
	PolicyQueue  SlotExhaustionPolicy = "queue"  // queue indefinitely (unbounded pending)
)

// Slot is one unit of processing capacity.
type Slot struct {
	ID        string
	WorkerID  string
	ThoughtID string
	Reserved  bool // reserved for high-priority thoughts
	// Foreground marks a slot held for the binary run's interactive
	// thought: only foreground-tagged thoughts may claim
	// it, so interactive work never starves behind daemon backlog.
	// Foreground thoughts may still use open slots, and preemption
	// applies to them like everything else (same priority rules).
	Foreground bool
}

// HighPriorityThreshold: thoughts at or above this priority (i.e. this
// number or lower) may use reserved slots and preempt running thoughts.
const HighPriorityThreshold = 3

// Config configures a Queue.
type Config struct {
	MaxConcurrent int
	PrioritySlots int
	// ForegroundSlots reserves capacity for the binary run's
	// interactive thought: foreground-tagged thoughts claim
	// these first (they may also use open slots), and non-foreground
	// thoughts never occupy them. Same priority/preemption rules as
	// every other slot. Disjoint from PrioritySlots.
	ForegroundSlots int
	// ReconcileInterval is the store-reconcile cadence (NAVI-336):
	// the loop adopts externally-persisted thoughts (navi enqueue from
	// another process) so external doors reach a running navi. Zero →
	// default 1s; negative disables the loop (embedded/tested queues
	// with no external writers).
	ReconcileInterval time.Duration
	// FlushInterval is the dirty-set flusher cadence (NAVI-338
	// memory-first persistence): mutations mark thoughts dirty
	// in-memory; the flusher persists them on this cadence outside
	// q.mu. Zero → default 250ms; negative keeps the legacy
	// synchronous write-through (no flusher). Durability-critical
	// writes (park checkpoint, adoption claims, delete) stay
	// synchronous regardless.
	FlushInterval  time.Duration
	SlotExhaustion SlotExhaustionPolicy
}

// Queue is a priority queue with bounded capacity.
// Persistent state lives in a Store; the queue itself is the in-memory
// scheduler over it.
type Queue struct {
	cfg Config
	mu  sync.Mutex

	pending  []*Thought // priority-sorted, awaiting a slot + worker
	running  []*Thought // claimed by workers, in flight
	paused   []*Thought
	terminal []*Thought // complete/cancelled: kept for inspection, not scheduled
	slots    []Slot

	store Store

	// assemble is the creation-time context assembler (ADR 0013 decision
	// 2), installed by the composition root via SetAssembler. The queue
	// cannot assemble itself — the content is navi-specific (compiled
	// skills, short-term memory) — so this is a seam, like Store. It is
	// set before serving begins and read outside q.mu.
	assemble func(*Thought) error

	// steeringRouter delivers messages to a running thought's agent
	// (running-path); invoked OUTSIDE q.mu — implementations
	// must not re-enter the queue. Wired by the runtime (NAVI-320).
	steeringRouter SteeringRouter

	// reconcile loop lifecycle (NAVI-336): reconcileStop guards the
	// single loop per queue (StartReconcile is idempotent).
	reconcileMu   sync.Mutex
	reconcileStop chan struct{}

	// dirty-set flusher (NAVI-338 memory-first): mutations mark
	// thoughts dirty under q.mu (zero I/O in the critical section);
	// the flusher goroutine persists them outside the lock.
	// dirty/dirtyIDs are guarded by q.mu.
	dirty     map[string]*Thought
	dirtyIDs  map[string]*Thought
	flushMu   sync.Mutex
	flushStop chan struct{}

	eventsMu sync.Mutex
	handlers []func(Event)
}

// Store persists queue state. The SQLite implementation
// (store/sqlite, migration v3) and an in-memory implementation
// live alongside; the queue persists through this interface rather than
// inventing a parallel storage layer.
type Store interface {
	SaveThought(t *Thought) error
	LoadThoughts() ([]*Thought, error)
	LoadThought(id string) (*Thought, error)
	DeleteThought(id string) error
}

// NewQueue builds a queue with the given config and persistence, and
// performs crash recovery: previously-running thoughts
// return to pending for re-acquisition.
func NewQueue(cfg Config, store Store) (*Queue, error) {
	if cfg.MaxConcurrent <= 0 {
		return nil, fmt.Errorf("thought: max_concurrent must be > 0")
	}
	if cfg.PrioritySlots < 0 || cfg.PrioritySlots > cfg.MaxConcurrent {
		return nil, fmt.Errorf("thought: priority_slots must be in [0, max_concurrent]")
	}
	if cfg.ForegroundSlots < 0 || cfg.ForegroundSlots > cfg.MaxConcurrent {
		return nil, fmt.Errorf("thought: foreground_slots must be in [0, max_concurrent]")
	}
	if cfg.ForegroundSlots+cfg.PrioritySlots > cfg.MaxConcurrent {
		return nil, fmt.Errorf("thought: foreground_slots + priority_slots must be <= max_concurrent")
	}
	if cfg.SlotExhaustion == "" {
		cfg.SlotExhaustion = PolicyQueue
	}

	slots := make([]Slot, cfg.MaxConcurrent)
	for i := range slots {
		slots[i] = Slot{
			ID:       fmt.Sprintf("slot-%d", i+1),
			Reserved: i < cfg.PrioritySlots, // priority thoughts fill reserved slots first
			// Foreground reservation: carved from the tail of
			// the slot pool, disjoint from the priority-reserved head.
			Foreground: i >= cfg.MaxConcurrent-cfg.ForegroundSlots,
		}
	}

	q := &Queue{
		cfg: cfg, slots: slots, store: store,
		dirty:    make(map[string]*Thought),
		dirtyIDs: make(map[string]*Thought),
	}
	if err := q.recover(); err != nil {
		return nil, err
	}
	return q, nil
}

// recover implements crash recovery: persisted thoughts are
// reloaded; running ones return to pending (they need re-acquisition).
func (q *Queue) recover() error {
	if q.store == nil {
		return nil
	}
	all, err := q.store.LoadThoughts()
	if err != nil {
		return fmt.Errorf("thought: crash recovery: %w", err)
	}
	for _, t := range all {
		switch t.Status {
		case ThoughtRunning, ThoughtPaused:
			t.Status = ThoughtPending
			t.SlotID = ""
			t.WorkerID = ""
			q.pending = append(q.pending, t)
		case ThoughtPending, ThoughtFailed:
			q.pending = append(q.pending, t)
		case ThoughtComplete, ThoughtCancelled:
			// Terminal: not scheduled; GetThought falls back to the
			// store for these.
			q.terminal = append(q.terminal, t)
		}
	}
	q.sortPending()
	// A dependency that is already terminal-but-unsatisfied (failed or
	// cancelled) can never release its dependents, so the recovered graph
	// is settled: those dependents are failed rather than left blocked
	// forever. Unresolvable ids stay blocked and surface through
	// blocked_by.
	var evts []Event
	q.settlePendingDependents(&evts)
	q.emit(evts)
	return nil
}

// Recover loads persisted thoughts into the queue. NewQueue calls the
// private recover once at construction; the runtime calls Recover again at
// assemble, after a late-bound store (the navi's datastore adapter) is
// ready, so work persisted in a previous process is adopted. It is
// idempotent: thoughts already known to the queue are skipped, so calling
// it more than once adopts nothing new.
func (q *Queue) Recover() {
	q.reconcileOnce()
}

func (q *Queue) sortPending() {
	sort.SliceStable(q.pending, func(i, j int) bool {
		return q.pending[i].EffectivePriority() < q.pending[j].EffectivePriority()
	})
}

// ── dependencies ────────────────────────────────────────────────────────
//
// A thought may name the thoughts it waits for (Thought.DependsOn,
// NAVI-378). The edges are validated at the door, persisted with the
// row, and honored here: Dequeue will not claim a thought until every
// dependency has reached "complete". Priority cannot express ordering
// once more than one worker runs, and a paused dependency is not
// complete — so preempting a chain's head parks the whole chain, as it
// should. "Blocked" is derived from these edges for display; it is never
// a lifecycle status.

// depsSatisfied reports whether every dependency of t has completed.
// Only "complete" satisfies: a failed, cancelled, paused, or running
// dependency leaves the dependent blocked. An id the queue cannot
// resolve is unsatisfied too — an unresolvable dependency never releases
// its dependent. Caller holds q.mu.
func (q *Queue) depsSatisfied(t *Thought) bool {
	for _, dep := range t.DependsOn {
		d, ok := q.findByID(dep)
		if !ok || d.Status != ThoughtComplete {
			return false
		}
	}
	return true
}

// validateDeps is the dependency door check: every id must already be
// known (a successor is enqueued after its predecessor), a thought may
// not depend on itself, and the edge may not close a cycle. Caller holds
// q.mu.
func (q *Queue) validateDeps(t *Thought) error {
	for _, dep := range t.DependsOn {
		if dep == t.ID {
			return fmt.Errorf("thought: enqueue %s: depends_on %q: a thought cannot depend on itself", t.ID, dep)
		}
		if _, ok := q.findByID(dep); !ok {
			return fmt.Errorf("thought: enqueue %s: depends_on %q: %w (a successor is enqueued after its predecessor)", t.ID, dep, ErrThoughtNotFound)
		}
	}
	if dep, ok := q.dependsOnCycle(t); ok {
		return fmt.Errorf("thought: enqueue %s: depends_on %q would create a dependency cycle", t.ID, dep)
	}
	return nil
}

// dependsOnCycle walks the stored dependency graph from t's edges and
// reports the id that closes a cycle back to t. The door rule above
// (ids must already exist) makes a cycle unreachable for ordinary
// in-process enqueues — nothing can already point at a brand-new thought
// — but a row adopted from an external writer may point at an id that is
// not enqueued yet, and enqueueing that id would close the loop. The
// edge is refused rather than wedging the chain. Caller holds q.mu.
func (q *Queue) dependsOnCycle(t *Thought) (string, bool) {
	visited := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		if id == t.ID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		d, ok := q.findByID(id)
		if !ok {
			return false
		}
		for _, next := range d.DependsOn {
			if walk(next) {
				return true
			}
		}
		return false
	}
	for _, dep := range t.DependsOn {
		if walk(dep) {
			return dep, true
		}
	}
	return "", false
}

// failDependents propagates a dead dependency: a pending thought whose
// dependency failed, was cancelled, or was removed can never become
// claimable, so it is failed too, transitively — a whole chain behind
// one failure is surfaced instead of hanging silently. Each dependent's
// EventFailed detail names the dependency that killed it, and Retry
// re-applies the gate, so retrying the dependency first is the recovery.
// Caller holds q.mu.
func (q *Queue) failDependents(id, cause string, evts *[]Event) {
	q.propagateFailure(map[string]string{id: cause}, evts)
}

// settlePendingDependents fails pending thoughts whose dependency is
// already terminal-but-unsatisfied (failed or cancelled). It is the
// load/adoption pass: recover and reconcile call it so a restart cannot
// keep a dependent blocked on a dependency that can never complete.
// Caller holds q.mu (or holds the queue exclusively, as recover does).
func (q *Queue) settlePendingDependents(evts *[]Event) {
	seeds := map[string]string{}
	for _, list := range [][]*Thought{q.pending, q.running, q.paused, q.terminal} {
		for _, t := range list {
			switch t.Status {
			case ThoughtFailed, ThoughtCancelled:
				seeds[t.ID] = string(t.Status)
			}
		}
	}
	q.propagateFailure(seeds, evts)
}

// propagateFailure fails (transitively) the pending thoughts that depend
// on any id in seeds, each with a detail naming the dependency and its
// cause. Caller holds q.mu.
func (q *Queue) propagateFailure(seeds map[string]string, evts *[]Event) {
	if len(seeds) == 0 {
		return
	}
	dead := make([]string, 0, len(seeds))
	handled := make(map[string]bool, len(seeds))
	for id := range seeds {
		dead = append(dead, id)
		handled[id] = true
	}
	for len(dead) > 0 {
		d := dead[0]
		dead = dead[1:]
		cause := seeds[d]
		for _, t := range q.pending {
			if t.Status != ThoughtPending || handled[t.ID] || !dependsOn(t, d) {
				continue
			}
			if err := t.Transition(ThoughtFailed); err != nil {
				continue
			}
			handled[t.ID] = true
			seeds[t.ID] = "failed"
			collect(evts, EventFailed, t.ID, fmt.Sprintf("blocked by dependency %s (%s)", d, cause))
			q.markDirty(t)
			dead = append(dead, t.ID)
		}
	}
}

// dependsOn reports whether t waits on id.
func dependsOn(t *Thought, id string) bool {
	for _, dep := range t.DependsOn {
		if dep == id {
			return true
		}
	}
	return false
}

// ── events ───────────────────────────────────────────────────────────────

// OnEvent registers an observer for queue events.
func (q *Queue) OnEvent(fn func(Event)) {
	q.eventsMu.Lock()
	defer q.eventsMu.Unlock()
	q.handlers = append(q.handlers, fn)
}

// Enqueue adds a thought to the pending queue (priority-sorted). With
// PolicyReject and no slot available (including via preemption), it
// returns an error immediately; with PolicyQueue/PolicyBlock the
// thought waits for a worker to claim it. High-priority thoughts
// preempt lower-priority running thoughts when no slot is free.
//
// The deferral parameter arrives on the thought itself:
// when t.DeferUntil is set, the thought is queued — and persisted —
// immediately but is not claimable by the pool until the not-before
// time passes (durable state). Self-scheduled thoughts
// (Metadata origin `self`/`tool`) have their priority clamped to the
// conservative band — a navi cannot enqueue its own work ahead
// of external work by default.
// SetAssembler installs the creation-time context assembler (ADR 0013
// decision 2). Enqueue calls it outside q.mu once the thought is validated,
// so a thought carries its assembled context and a thought that cannot be
// assembled is rejected at the door rather than mid-run. Set it before
// serving begins; with no assembler, Enqueue schedules as before.
func (q *Queue) SetAssembler(fn func(*Thought) error) {
	q.assemble = fn
}

func (q *Queue) Enqueue(t *Thought) error {
	if t.ID == "" {
		return fmt.Errorf("thought: enqueue requires an ID")
	}
	// A zero status means "a new thought": the caller is constructing
	// work to schedule, not asserting a lifecycle state, and making them
	// restate the queue's entry condition is a trap rather than a check.
	// Anything else must be pending — enqueueing a running or completed
	// thought is still an invalid transition.
	if t.Status == "" {
		t.Status = ThoughtPending
	}
	if t.Status != ThoughtPending {
		return fmt.Errorf("%w: enqueue requires pending status, got %s", ErrInvalidTransition, t.Status)
	}
	if origin, ok := t.Metadata["origin"]; ok && (origin == OriginSelf || origin == OriginTool) {
		t.Priority = t.EffectiveSelfPriority()
	}

	// Context assembly at the door (ADR 0013 decision 2). Runs outside q.mu:
	// the assembler reads the navi's compiled skills and in-RAM memory, never
	// the queue.
	if q.assemble != nil {
		if err := q.assemble(t); err != nil {
			return fmt.Errorf("thought: enqueue %s: assemble: %w", t.ID, err)
		}
	}

	// Events collected under lock, emitted after unlock — OnEvent
	// handlers may re-enter the queue (reading a thought, for
	// example), which would deadlock if emit ran under q.mu.
	var evts []Event

	q.mu.Lock()
	if _, exists := q.findByID(t.ID); exists {
		q.mu.Unlock()
		return fmt.Errorf("thought: duplicate ID %s", t.ID)
	}
	if err := q.validateDeps(t); err != nil {
		q.mu.Unlock()
		return err
	}

	// Reject policy: reject when NO slot is immediately available and
	// the thought can't preempt one.
	if q.cfg.SlotExhaustion == PolicyReject && q.findSlotFor(t) == "" && q.preemptCandidate(t) == "" {
		q.mu.Unlock()
		return fmt.Errorf("thought: all slots full (policy: reject)")
	}

	// Preemption: a high-priority thought with no free slot pauses the
	// lowest-priority running thought, freeing a slot for the next
	// claim. Paused thoughts resume via Resume.
	t.UpdatedAt = time.Now().UTC()
	q.pending = append(q.pending, t)
	q.sortPending()
	if q.findSlotFor(t) == "" {
		if victim := q.preemptCandidate(t); victim != "" {
			q.preempt(victim, t, &evts)
		}
	}
	collect(&evts, EventEnqueued, t.ID, fmt.Sprintf("priority %d", t.EffectivePriority()))
	// NAVI-338 memory-first: mark dirty (in-memory, zero I/O in the
	// critical section); the flusher persists outside the lock.
	q.markDirty(t)
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// Dequeue claims the highest-priority pending thought that is DUE and
// for which a slot is available, transitions it to running, and
// allocates the slot. Deferred thoughts are
// skipped until their not-before time passes — a deferred entry never
// blocks a due thought of any priority. Returns ok=false when nothing
// is claimable.
func (q *Queue) Dequeue() (*Thought, bool) {
	var evts []Event
	now := time.Now().UTC()

	q.mu.Lock()
	for i, t := range q.pending {
		if !t.Due(now) {
			continue // deferred: not claimable until defer_until passes
		}
		if !q.depsSatisfied(t) {
			continue // blocked: a dependency has not completed yet
		}
		slotID := q.findSlotFor(t)
		if slotID == "" {
			continue
		}
		if err := t.Transition(ThoughtRunning); err != nil {
			continue
		}
		q.claimSlot(t, slotID)
		q.pending = append(q.pending[:i], q.pending[i+1:]...)
		q.running = append(q.running, t)
		collect(&evts, EventStarted, t.ID, fmt.Sprintf("slot %s", slotID))
		// NAVI-338 memory-first: claim state change marks dirty (the
		// flusher persists outside the lock).
		q.markDirty(t)
		// Copy-in/copy-out: the worker/runner gets a Clone and owns it
		// until the thought reaches a terminal state. Runner mutations
		// (Checkpoint, and anything else) never race with external
		// GetThought readers — they work on the clone; the worker merges
		// the checkpoint back via Pause on cancellation/preemption.
		// Mutations to other fields (LogPath, Result, ...) are
		// isolated to the clone and discarded.
		claimed := t.Clone()
		q.mu.Unlock()

		// Emit outside q.mu — OnEvent handlers may re-enter the queue.
		q.emit(evts)

		return claimed, true
	}
	q.mu.Unlock()
	return nil, false
}

// BumpPriority changes a thought's priority and re-sorts the queue.
func (q *Queue) BumpPriority(id string, newPriority int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, err := q.lookup(id)
	if err != nil {
		return err
	}
	t.Priority = newPriority
	t.UpdatedAt = time.Now().UTC()
	q.sortPending()
	// NAVI-338: dirty mark (flusher persists outside the lock).
	q.markDirty(t)
	return nil
}

// Pause stops a running thought and frees its slot. The
// checkpoint passed here (typically the runner's snapshot) is stored
// on the thought for resume. Workers also use Pause to honour a
// preemption request — it clears the PreemptRequested flag.
// Pause parks a thought, recording no checkpoint.
//
// This is the form components get through ThoughtSystem: parking is a
// control action, and a connector has no business constructing the
// agent's serialized state. The checkpointed variant lives on the
// concrete type, where only the runtime can reach it.
func (q *Queue) Pause(id string) error { return q.PauseWithCheckpoint(id, nil) }

// PauseWithCheckpoint parks a thought and records the checkpoint the
// runner snapshotted, so a resumed thought continues rather than
// restarts (NAVI-338: the checkpoint is on disk at park time).
func (q *Queue) PauseWithCheckpoint(id string, checkpoint []byte) error {
	var evts []Event

	q.mu.Lock()
	t, idx := q.findRunning(id)
	if t == nil {
		q.mu.Unlock()
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	t.Checkpoint = checkpoint
	if err := t.Transition(ThoughtPaused); err != nil {
		q.mu.Unlock()
		return err
	}
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.paused = append(q.paused, t)
	// DURABILITY-CRITICAL (NAVI-338): park-with-checkpoint is the
	// kill-and-lose promise — the checkpoint MUST be on disk at park
	// time. Synchronous write, then emit.
	if err := q.syncSave(t); err != nil {
		q.mu.Unlock()
		return err
	}
	collect(&evts, EventPaused, id, "")
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// Resume moves a paused thought back to pending (it re-acquires a slot
// via the normal scheduling path).
func (q *Queue) Resume(id string) error {
	var evts []Event

	q.mu.Lock()
	t, idx := q.findIn(q.paused, id)
	if t == nil {
		q.mu.Unlock()
		return fmt.Errorf("thought: %s: %w (not paused)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtPending); err != nil {
		q.mu.Unlock()
		return err
	}
	q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
	q.pending = append(q.pending, t)
	q.sortPending()
	// NAVI-338: dirty mark.
	q.markDirty(t)
	collect(&evts, EventResumed, id, "")
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// Cancel a pending or paused thought (running thoughts must
// be paused/failed first). A cancelled thought can never complete, so
// its dependents are failed with a detail naming the dependency.
func (q *Queue) Cancel(id string) error {
	var evts []Event

	q.mu.Lock()
	if t, idx := q.findRunning(id); t != nil {
		_ = idx // running thoughts cancel via Pause first
		q.mu.Unlock()
		return fmt.Errorf("thought: %s is running — pause it before cancelling", id)
	}
	if t, idx := q.findIn(q.pending, id); t != nil {
		if err := t.Transition(ThoughtCancelled); err != nil {
			q.mu.Unlock()
			return err
		}
		q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
		q.running = append(q.running, t) // terminal: kept for inspection
		collect(&evts, EventCancelled, id, "")
		// NAVI-338: dirty mark.
		q.markDirty(t)
		q.failDependents(id, "cancelled", &evts)
		q.mu.Unlock()
		q.emit(evts)
		return nil
	}
	if t, idx := q.findIn(q.paused, id); t != nil {
		if err := t.Transition(ThoughtCancelled); err != nil {
			q.mu.Unlock()
			return err
		}
		q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
		q.running = append(q.running, t)
		collect(&evts, EventCancelled, id, "")
		// NAVI-338: dirty mark.
		q.markDirty(t)
		q.failDependents(id, "cancelled", &evts)
		q.mu.Unlock()
		q.emit(evts)
		return nil
	}
	q.mu.Unlock()
	return fmt.Errorf("thought: %s: %w", id, ErrThoughtNotFound)
}

// Complete marks a running thought complete (called by the worker).
// Complete marks a running thought complete and records its completion
// message. result is the runner's reported outcome; it is persisted so a
// thought's output is readable from the queue itself rather than only
// from its session transcript.
func (q *Queue) Complete(id string, result string) error {
	var evts []Event

	q.mu.Lock()
	t, idx := q.findRunning(id)
	if t == nil {
		q.mu.Unlock()
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtComplete); err != nil {
		q.mu.Unlock()
		return err
	}
	t.Result = result
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.terminal = append(q.terminal, t)
	// NAVI-338: dirty mark.
	q.markDirty(t)
	collect(&evts, EventCompleted, id, "")
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// Fail marks a running thought failed (called by the worker on error).
// Dependents are failed too (with a detail naming the dependency): a
// failed dependency can never release them, so hanging is not an option.
func (q *Queue) Fail(id string, detail string) error {
	var evts []Event

	q.mu.Lock()
	t, idx := q.findRunning(id)
	if t == nil {
		q.mu.Unlock()
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtFailed); err != nil {
		q.mu.Unlock()
		return err
	}
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.pending = append(q.pending, t) // failed awaits Retry
	collect(&evts, EventFailed, id, detail)
	// NAVI-338: dirty mark.
	q.markDirty(t)
	q.failDependents(id, "failed", &evts)
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// GetThoughts returns all known thoughts.
func (q *Queue) GetThoughts() []*Thought {
	q.mu.Lock()
	defer q.mu.Unlock()
	all := make([]*Thought, 0, len(q.pending)+len(q.running)+len(q.paused)+len(q.terminal))
	all = append(all, q.pending...)
	all = append(all, q.running...)
	all = append(all, q.paused...)
	all = append(all, q.terminal...)
	out := make([]*Thought, len(all))
	for i, t := range all {
		out[i] = t.Clone()
	}
	return out
}

// GetThought returns a copy of a thought by ID. If the thought has
// already left memory (terminal), it is loaded from the store.
func (q *Queue) GetThought(id string) (*Thought, error) {
	q.mu.Lock()
	t, ok := q.findByID(id)
	if ok {
		cp := t.Clone()
		q.mu.Unlock()
		return cp, nil
	}
	q.mu.Unlock()

	if q.store != nil {
		return q.store.LoadThought(id)
	}
	return nil, fmt.Errorf("thought: %s: %w", id, ErrThoughtNotFound)
}

// ── internals ────────────────────────────────────────────────────────────

// findSlotFor returns a free slot ID for the thought, preferring
// reserved slots for high-priority thoughts and never giving a reserved
// slot to a low-priority thought while a normal one might free up.
func (q *Queue) findSlotFor(t *Thought) string {
	high := t.EffectivePriority() <= HighPriorityThreshold
	var normalFree, foregroundFree string
	for _, s := range q.slots {
		if s.ThoughtID != "" {
			continue
		}
		if s.Foreground {
			// Foreground reservation: held for foreground
			// thoughts only — daemon backlog cannot starve the
			// interactive session, and a foreground thought prefers its
			// reserved slot over consuming shared capacity.
			if t.Foreground && foregroundFree == "" {
				foregroundFree = s.ID
			}
			continue
		}
		if s.Reserved {
			if high && normalFree == "" {
				normalFree = s.ID // priority thoughts fill reserved slots first
			}
			continue
		}
		if normalFree == "" {
			normalFree = s.ID
		}
	}
	// Foreground thoughts prefer their reserved slot, then normal
	// capacity (schedulable like any other).
	if t.Foreground && foregroundFree != "" {
		return foregroundFree
	}
	return normalFree
}

// preemptCandidate picks the lowest-priority running thought the given
// high-priority thought may preempt (only strictly lower-priority
// victims;).
//
// Slot-type rule (foreground reservation): a victim whose slot
// the preemptor could not claim must not be picked — preemption frees
// the victim's slot for the preemptor, and freeing a slot the
// preemptor can never use (a foreground slot, for a non-foreground
// preemptor) would park the interactive thought for nothing. The
// victim's occupied slot is resolved by r.SlotID (running thoughts
// carry their claim); foreground-slot victims are preemptable only by
// foreground preemptors, normal-slot victims by anyone meeting the
// priority rule (same priority/preemption rules as everything else).
func (q *Queue) preemptCandidate(t *Thought) string {
	if t.EffectivePriority() > HighPriorityThreshold {
		return ""
	}
	victimID := ""
	victimPrio := 0
	for _, r := range q.running {
		if r.EffectivePriority() <= t.EffectivePriority() {
			continue // can only preempt strictly lower priority
		}
		if slot := q.slotBy(r.SlotID); slot != nil && slot.Foreground && !t.Foreground {
			continue // would free a slot the preemptor can never claim
		}
		if victimID == "" || r.EffectivePriority() > victimPrio {
			victimID = r.ID
			victimPrio = r.EffectivePriority()
		}
	}
	return victimID
}

// preempt pauses the victim running thought (freeing its slot,
// running → paused) and parks it as paused for later resume;
// the caller's thought then starts on the freed slot.
func (q *Queue) preempt(victimID string, by *Thought, evts *[]Event) {
	// Cooperative preemption (caller holds q.mu): mark the victim — it
	// keeps running until its runner honours the ctx cancellation the
	// pool sends on this event, snapshots its checkpoint, and returns;
	// then the worker parks it via Pause and the freed slot serves the
	// next claim. Acceleration is honestly bounded by runner
	// ctx-responsiveness.
	t, ok := q.findByID(victimID)
	if !ok || t.Status != ThoughtRunning {
		return
	}
	t.PreemptRequested = true
	// NAVI-338: dirty mark.
	q.markDirty(t)
	collect(evts, EventPreempted, victimID, fmt.Sprintf("by %s (priority %d)", by.ID, by.EffectivePriority()))
}

// claimSlot moves a thought to running on the given slot. Caller holds mu.
func (q *Queue) claimSlot(t *Thought, slotID string) {
	for i, s := range q.slots {
		if s.ID == slotID && s.ThoughtID == "" {
			q.slots[i].ThoughtID = t.ID
			q.slots[i].WorkerID = fmt.Sprintf("worker-%s", strings.TrimPrefix(slotID, "slot-"))
			t.SlotID = slotID
			t.WorkerID = q.slots[i].WorkerID
			break
		}
	}
}

// freeSlot releases the slot held by a thought.
func (q *Queue) freeSlot(thoughtID string) {
	for i, s := range q.slots {
		if s.ThoughtID == thoughtID {
			q.slots[i].ThoughtID = ""
			q.slots[i].WorkerID = ""
			return
		}
	}
}

func (q *Queue) findByID(id string) (*Thought, bool) {
	if t, _ := q.findIn(q.pending, id); t != nil {
		return t, true
	}
	if t, _ := q.findIn(q.running, id); t != nil {
		return t, true
	}
	if t, _ := q.findIn(q.paused, id); t != nil {
		return t, true
	}
	if t, _ := q.findIn(q.terminal, id); t != nil {
		return t, true
	}
	return nil, false
}

func (q *Queue) lookup(id string) (*Thought, error) {
	if t, ok := q.findByID(id); ok {
		return t, nil
	}
	return nil, fmt.Errorf("thought: %s: %w", id, ErrThoughtNotFound)
}

func (q *Queue) findRunning(id string) (*Thought, int) {
	for i, t := range q.running {
		if t.ID == id && t.Status == ThoughtRunning {
			return t, i
		}
	}
	return nil, -1
}

func (q *Queue) findIn(list []*Thought, id string) (*Thought, int) {
	for i, t := range list {
		if t.ID == id {
			return t, i
		}
	}
	return nil, -1
}

// slotAvailableFor reports whether any slot could serve the thought
// right now.
func (q *Queue) slotAvailableFor(t *Thought) bool {
	return q.findSlotFor(t) != ""
}

func (q *Queue) save(t *Thought) error {
	if q.store == nil {
		return nil
	}
	return q.store.SaveThought(t)
}

// Retry returns a failed thought to the pending queue for
// re-acquisition (failed → pending).
func (q *Queue) Retry(id string) error {
	var evts []Event

	q.mu.Lock()

	// Fail parks failed thoughts in q.pending: flip in place —
	// appending again would duplicate the entry (ghost thought).
	if t, _ := q.findIn(q.pending, id); t != nil {
		if t.Status != ThoughtFailed {
			q.mu.Unlock()
			return fmt.Errorf("thought: %s: %w: status %s is not failed", id, ErrInvalidTransition, t.Status)
		}
		if err := t.Retry(); err != nil {
			q.mu.Unlock()
			return err
		}
		q.sortPending()
		collect(&evts, EventEnqueued, id, "retry")
		// NAVI-338: dirty mark.
		q.markDirty(t)
		q.mu.Unlock()
		q.emit(evts)
		return nil
	}

	// Coming from elsewhere (e.g. the terminal list): re-queue.
	t, err := q.lookup(id)
	if err != nil {
		q.mu.Unlock()
		return err
	}
	if err := t.Retry(); err != nil {
		q.mu.Unlock()
		return err
	}
	q.pending = append(q.pending, t)
	if _, idx := q.findIn(q.terminal, id); idx >= 0 {
		q.terminal = append(q.terminal[:idx], q.terminal[idx+1:]...)
	}
	q.sortPending()
	collect(&evts, EventEnqueued, id, "retry")
	// NAVI-338: dirty mark.
	q.markDirty(t)
	q.mu.Unlock()

	q.emit(evts)
	return nil
}

// collect appends an event to the pending list (call under q.mu).
func collect(evts *[]Event, kind EventKind, thoughtID, detail string) {
	*evts = append(*evts, Event{Kind: kind, ThoughtID: thoughtID, Detail: detail, At: time.Now().UTC()})
}

// emit fans events out to registered observers. MUST be called without
// q.mu held — handlers may re-enter the queue (GetThought/GetThoughts),
// and a handler that does so while the emitting transition still holds
// q.mu deadlocks (NAVI-67). Every transition site follows the
// collect-under-lock / emit-after-unlock pattern: build the events in a
// local slice, release q.mu, then emit.
func (q *Queue) emit(evts []Event) {
	q.eventsMu.Lock()
	handlers := append([]func(Event){}, q.handlers...)
	q.eventsMu.Unlock()
	for _, fn := range handlers {
		for _, ev := range evts {
			fn(ev)
		}
	}
}

// Slots returns a snapshot of the queue's slots — diagnostic seam
// (foreground-reservation visibility for the binary-run entry and the
// health view).
func (q *Queue) Slots() []Slot {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Slot, len(q.slots))
	copy(out, q.slots)
	return out
}

// AppendContext appends a message to a thought's Context
// (context append): the queue owns the record — callers mutate through
// this method, never through GetThought clones (which the queue no
// longer controls). Persisted with the change.
func (q *Queue) AppendContext(id, msg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, err := q.lookup(id)
	if err != nil {
		return err
	}
	t.AppendMessage(msg)
	// NAVI-338: dirty mark.
	q.markDirty(t)
	// NAVI-338: dirty mark.
	q.markDirty(t)
	return nil
}

// Delete removes a thought record entirely from the queue and its
// persistence. Lifecycle protection: a RUNNING thought
// cannot be deleted in place — it must be paused or cancelled first
// (the error names the constraint); terminal thoughts (complete/
// cancelled) stay queued for inspection rather than deleted here.
// Pending (including deferred) and paused thoughts are removable.
func (q *Queue) Delete(id string) error {
	var evts []Event

	q.mu.Lock()
	t, _, idx, ok := q.locate(id)
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("thought: delete %q: %w", id, ErrThoughtNotFound)
	}
	switch t.Status {
	case ThoughtRunning:
		q.mu.Unlock()
		return fmt.Errorf("thought: delete %s: %w (running — pause or cancel via the lifecycle first)", id, ErrThoughtProtected)
	case ThoughtComplete, ThoughtCancelled:
		q.mu.Unlock()
		return fmt.Errorf("thought: delete %s: %w (terminal record kept for inspection)", id, ErrThoughtProtected)
	case ThoughtPending:
		q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
	case ThoughtPaused:
		q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
	default:
		q.mu.Unlock()
		return fmt.Errorf("thought: delete %s: %w (state %s)", id, ErrThoughtProtected, t.Status)
	}
	// Ack event with the PRIOR status (-style accounting:
	// metrics observers only see events — Detail names the gauge to
	// decrement). Deferred collect/emit: event leaves q.mu's scope.
	evts = append(evts, Event{
		Kind:      EventDeleted,
		ThoughtID: id,
		Detail:    string(t.Status),
		At:        time.Now().UTC(),
	})
	// A removed thought can never complete, so its dependents are failed
	// rather than left blocked on a record that no longer exists.
	q.failDependents(id, "deleted", &evts)
	if q.store != nil {
		if err := q.store.DeleteThought(id); err != nil {
			q.mu.Unlock()
			return fmt.Errorf("thought: delete %s: persist: %w", id, err)
		}
	}
	q.mu.Unlock()
	q.emit(evts)
	return nil
}

// The concrete runtime handle must satisfy the component interface, since
// components receive it narrowed to ThoughtSystem through Deps.
var _ ThoughtSystem = (*Queue)(nil)
