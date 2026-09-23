package thought

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SlotExhaustionPolicy determines what happens when all slots are full
// (spec §3.1).
type SlotExhaustionPolicy string

const (
	PolicyBlock  SlotExhaustionPolicy = "block"  // block until a slot frees
	PolicyReject SlotExhaustionPolicy = "reject" // reject with an error
	PolicyQueue  SlotExhaustionPolicy = "queue"  // queue indefinitely (unbounded pending)
)

// Slot is one unit of processing capacity (spec §3.1).
type Slot struct {
	ID        string
	WorkerID  string
	ThoughtID string
	Reserved  bool // reserved for high-priority thoughts
	// Foreground marks a slot held for the binary run's interactive
	// thought (S010003): only foreground-tagged thoughts may claim
	// it, so interactive work never starves behind daemon backlog.
	// Foreground thoughts may still use open slots, and preemption
	// applies to them like everything else (same priority rules).
	Foreground bool
}

// HighPriorityThreshold: thoughts at or above this priority (i.e. this
// number or lower) may use reserved slots and preempt running thoughts.
const HighPriorityThreshold = 3

// Config configures a ThoughtQueue (spec §3.2).
type Config struct {
	MaxConcurrent int
	PrioritySlots int
	// ForegroundSlots reserves capacity for the binary run's
	// interactive thought (S010003): foreground-tagged thoughts claim
	// these first (they may also use open slots), and non-foreground
	// thoughts never occupy them. Same priority/preemption rules as
	// every other slot. Disjoint from PrioritySlots.
	ForegroundSlots int
	SlotExhaustion  SlotExhaustionPolicy
}

// QueueEventKind enumerates queue event types (for observers/logging).
type QueueEventKind string

const (
	EventEnqueued  QueueEventKind = "enqueued"
	EventStarted   QueueEventKind = "started"
	EventPausedEv  QueueEventKind = "paused"
	EventResumed   QueueEventKind = "resumed"
	EventCompleted QueueEventKind = "completed"
	EventFailed    QueueEventKind = "failed"
	EventCancelled QueueEventKind = "cancelled"
	EventPreempted QueueEventKind = "preempted"
	// EventPersistFailed fires when a worker cannot persist a thought
	// transition (Complete/Fail/Pause) — in-memory and stored state may
	// have diverged.
	EventPersistFailed QueueEventKind = "persist_failed"
)

// QueueEvent is emitted on notable queue transitions.
type QueueEvent struct {
	Kind      QueueEventKind
	ThoughtID string
	Detail    string
	At        time.Time
	// Delivery is the routing path for EventMessageRouted acks
	// (S010312: "context-append" | "steering" | "resume-with-message"
	// | ""). Empty on lifecycle events.
	Delivery string
}

// ThoughtQueue is a priority queue with bounded capacity (spec §3.1).
// Persistent state lives in a Store; the queue itself is the in-memory
// scheduler over it.
type ThoughtQueue struct {
	cfg Config
	mu  sync.Mutex

	pending  []*Thought // priority-sorted, awaiting a slot + worker
	running  []*Thought // claimed by workers, in flight
	paused   []*Thought
	terminal []*Thought // complete/cancelled: kept for inspection, not scheduled
	slots    []Slot

	store Store

	// steeringRouter delivers messages to a running thought's agent
	// (S010312 running-path); invoked OUTSIDE q.mu — implementations
	// must not re-enter the queue. Wired by the runtime (NAVI-320).
	steeringRouter SteeringRouter

	eventsMu sync.Mutex
	handlers []func(QueueEvent)
}

// Store persists queue state (spec §3.4). The SQLite implementation
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
// performs crash recovery (spec §3.5): previously-running thoughts
// return to pending for re-acquisition.
func NewQueue(cfg Config, store Store) (*ThoughtQueue, error) {
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
			// Foreground reservation (S010003): carved from the tail of
			// the slot pool, disjoint from the priority-reserved head.
			Foreground: i >= cfg.MaxConcurrent-cfg.ForegroundSlots,
		}
	}

	q := &ThoughtQueue{cfg: cfg, slots: slots, store: store}
	if err := q.recover(); err != nil {
		return nil, err
	}
	return q, nil
}

// recover implements crash recovery (spec §3.5): persisted thoughts are
// reloaded; running ones return to pending (they need re-acquisition).
func (q *ThoughtQueue) recover() error {
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
	return nil
}

func (q *ThoughtQueue) sortPending() {
	sort.SliceStable(q.pending, func(i, j int) bool {
		return q.pending[i].EffectivePriority() < q.pending[j].EffectivePriority()
	})
}

// ── events ───────────────────────────────────────────────────────────────

// OnEvent registers an observer for queue events.
func (q *ThoughtQueue) OnEvent(fn func(QueueEvent)) {
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
// The deferral parameter arrives on the thought itself (S130600 §02):
// when t.DeferUntil is set, the thought is queued — and persisted —
// immediately but is not claimable by the pool until the not-before
// time passes (durable state, S010002). Self-scheduled thoughts
// (Metadata origin `self`/`tool`) have their priority clamped to the
// conservative band (§1.2) — a navi cannot enqueue its own work ahead
// of external work by default.
func (q *ThoughtQueue) Enqueue(t *Thought) error {
	if t.ID == "" {
		return fmt.Errorf("thought: enqueue requires an ID")
	}
	if t.Status != ThoughtPending {
		return fmt.Errorf("%w: enqueue requires pending status, got %s", ErrInvalidTransition, t.Status)
	}
	if origin, ok := t.Metadata["origin"]; ok && (origin == OriginSelf || origin == OriginTool) {
		t.Priority = t.EffectiveSelfPriority()
	}

	// Events collected under lock, emitted after unlock — OnEvent
	// handlers may re-enter the queue (e.g. the daemon's RecordFire
	// wiring calls GetThought), which would deadlock if emit ran under
	// q.mu.
	var evts []QueueEvent

	q.mu.Lock()
	if _, exists := q.findByID(t.ID); exists {
		q.mu.Unlock()
		return fmt.Errorf("thought: duplicate ID %s", t.ID)
	}

	// Reject policy: reject when NO slot is immediately available and
	// the thought can't preempt one.
	if q.cfg.SlotExhaustion == PolicyReject && q.findSlotFor(t) == "" && q.preemptCandidate(t) == "" {
		q.mu.Unlock()
		return fmt.Errorf("thought: all slots full (policy: reject)")
	}

	// Preemption: a high-priority thought with no free slot pauses the
	// lowest-priority running thought, freeing a slot for the next
	// claim (spec §3.2). Paused thoughts resume via Resume.
	t.UpdatedAt = time.Now().UTC()
	q.pending = append(q.pending, t)
	q.sortPending()
	if q.findSlotFor(t) == "" {
		if victim := q.preemptCandidate(t); victim != "" {
			q.preempt(victim, t, &evts)
		}
	}
	collect(&evts, EventEnqueued, t.ID, fmt.Sprintf("priority %d", t.EffectivePriority()))
	saveErr := q.save(t)
	q.mu.Unlock()

	q.emit(evts)
	if saveErr != nil {
		return saveErr
	}
	return nil
}

// Dequeue claims the highest-priority pending thought that is DUE and
// for which a slot is available, transitions it to running, and
// allocates the slot (spec §3.3, S130600 §02). Deferred thoughts are
// skipped until their not-before time passes — a deferred entry never
// blocks a due thought of any priority. Returns ok=false when nothing
// is claimable.
func (q *ThoughtQueue) Dequeue() (*Thought, bool) {
	var evts []QueueEvent
	now := time.Now().UTC()

	q.mu.Lock()
	for i, t := range q.pending {
		if !t.Due(now) {
			continue // deferred: not claimable until defer_until passes
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
		saveErr := q.save(t)
		// Copy-in/copy-out: the worker/runner gets a Clone and owns it
		// until the thought reaches a terminal state. Runner mutations
		// (Checkpoint, and anything else) never race with external
		// GetThought readers — they work on the clone; the worker merges
		// the checkpoint back via Pause on cancellation/preemption.
		// Mutations to other fields (SessionID, LogPath, ...) are
		// isolated to the clone and discarded.
		claimed := t.Clone()
		q.mu.Unlock()

		// Emit outside q.mu — OnEvent handlers may re-enter the queue.
		q.emit(evts)
		_ = saveErr
		return claimed, true
	}
	q.mu.Unlock()
	return nil, false
}

// BumpPriority changes a thought's priority and re-sorts the queue.
func (q *ThoughtQueue) BumpPriority(id string, newPriority int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, err := q.lookup(id)
	if err != nil {
		return err
	}
	t.Priority = newPriority
	t.UpdatedAt = time.Now().UTC()
	q.sortPending()
	if err := q.save(t); err != nil {
		return err
	}
	return nil
}

// Pause stops a running thought and frees its slot (spec §2.2). The
// checkpoint passed here (typically the runner's snapshot) is stored
// on the thought for resume. Workers also use Pause to honour a
// preemption request — it clears the PreemptRequested flag.
func (q *ThoughtQueue) Pause(id string, checkpoint []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, idx := q.findRunning(id)
	if t == nil {
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	t.Checkpoint = checkpoint
	if err := t.Transition(ThoughtPaused); err != nil {
		return err
	}
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.paused = append(q.paused, t)
	if err := q.save(t); err != nil {
		return err
	}
	q.emit([]QueueEvent{{Kind: EventPausedEv, ThoughtID: id, At: time.Now().UTC()}})
	return nil
}

// Resume moves a paused thought back to pending (it re-acquires a slot
// via the normal scheduling path).
func (q *ThoughtQueue) Resume(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, idx := q.findIn(q.paused, id)
	if t == nil {
		return fmt.Errorf("thought: %s: %w (not paused)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtPending); err != nil {
		return err
	}
	q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
	q.pending = append(q.pending, t)
	q.sortPending()
	if err := q.save(t); err != nil {
		return err
	}
	q.emit([]QueueEvent{{Kind: EventResumed, ThoughtID: id, At: time.Now().UTC()}})
	return nil
}

// Cancel a pending or paused thought (spec §2.2: running thoughts must
// be paused/failed first).
func (q *ThoughtQueue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, idx := q.findRunning(id); t != nil {
		_ = idx // running thoughts cancel via Pause first (spec §2.2)
		return fmt.Errorf("thought: %s is running — pause it before cancelling", id)
	}
	if t, idx := q.findIn(q.pending, id); t != nil {
		if err := t.Transition(ThoughtCancelled); err != nil {
			return err
		}
		q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
		q.running = append(q.running, t) // terminal: kept for inspection
		q.emit([]QueueEvent{{Kind: EventCancelled, ThoughtID: id, At: time.Now().UTC()}})
		return q.save(t)
	}
	if t, idx := q.findIn(q.paused, id); t != nil {
		if err := t.Transition(ThoughtCancelled); err != nil {
			return err
		}
		q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
		q.running = append(q.running, t)
		q.emit([]QueueEvent{{Kind: EventCancelled, ThoughtID: id, At: time.Now().UTC()}})
		return q.save(t)
	}
	return fmt.Errorf("thought: %s: %w", id, ErrThoughtNotFound)
}

// Complete marks a running thought complete (called by the worker).
func (q *ThoughtQueue) Complete(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, idx := q.findRunning(id)
	if t == nil {
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtComplete); err != nil {
		return err
	}
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.terminal = append(q.terminal, t)
	q.emit([]QueueEvent{{Kind: EventCompleted, ThoughtID: id, At: time.Now().UTC()}})
	return q.save(t)
}

// Fail marks a running thought failed (called by the worker on error).
func (q *ThoughtQueue) Fail(id string, detail string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, idx := q.findRunning(id)
	if t == nil {
		return fmt.Errorf("thought: %s: %w (not running)", id, ErrThoughtNotFound)
	}
	if err := t.Transition(ThoughtFailed); err != nil {
		return err
	}
	q.freeSlot(id)
	t.SlotID = ""
	t.WorkerID = ""
	t.PreemptRequested = false
	q.running = append(q.running[:idx], q.running[idx+1:]...)
	q.pending = append(q.pending, t) // failed awaits Retry
	q.emit([]QueueEvent{{Kind: EventFailed, ThoughtID: id, Detail: detail, At: time.Now().UTC()}})
	return q.save(t)
}

// GetThoughts returns all known thoughts.
func (q *ThoughtQueue) GetThoughts() []*Thought {
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
func (q *ThoughtQueue) GetThought(id string) (*Thought, error) {
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
func (q *ThoughtQueue) findSlotFor(t *Thought) string {
	high := t.EffectivePriority() <= HighPriorityThreshold
	var normalFree, foregroundFree string
	for _, s := range q.slots {
		if s.ThoughtID != "" {
			continue
		}
		if s.Foreground {
			// Foreground reservation (S010003): held for foreground
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
	// capacity (schedulable like any other, S010003 §06).
	if t.Foreground && foregroundFree != "" {
		return foregroundFree
	}
	return normalFree
}

// preemptCandidate picks the lowest-priority running thought the given
// high-priority thought may preempt (only strictly lower-priority
// victims; spec §3.2).
//
// Slot-type rule (S010003 foreground reservation): a victim whose slot
// the preemptor could not claim must not be picked — preemption frees
// the victim's slot for the preemptor, and freeing a slot the
// preemptor can never use (a foreground slot, for a non-foreground
// preemptor) would park the interactive thought for nothing. The
// victim's occupied slot is resolved by r.SlotID (running thoughts
// carry their claim); foreground-slot victims are preemptable only by
// foreground preemptors, normal-slot victims by anyone meeting the
// priority rule (same priority/preemption rules as everything else).
func (q *ThoughtQueue) preemptCandidate(t *Thought) string {
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

// preempt pauses the victim running thought (freeing its slot, spec
// §2.2 running → paused) and parks it as paused for later resume;
// the caller's thought then starts on the freed slot.
func (q *ThoughtQueue) preempt(victimID string, by *Thought, evts *[]QueueEvent) {
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
	_ = q.save(t)
	collect(evts, EventPreempted, victimID, fmt.Sprintf("by %s (priority %d)", by.ID, by.EffectivePriority()))
}

// claimSlot moves a thought to running on the given slot. Caller holds mu.
func (q *ThoughtQueue) claimSlot(t *Thought, slotID string) {
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
func (q *ThoughtQueue) freeSlot(thoughtID string) {
	for i, s := range q.slots {
		if s.ThoughtID == thoughtID {
			q.slots[i].ThoughtID = ""
			q.slots[i].WorkerID = ""
			return
		}
	}
}

func (q *ThoughtQueue) findByID(id string) (*Thought, bool) {
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

func (q *ThoughtQueue) lookup(id string) (*Thought, error) {
	if t, ok := q.findByID(id); ok {
		return t, nil
	}
	return nil, fmt.Errorf("thought: %s: %w", id, ErrThoughtNotFound)
}

func (q *ThoughtQueue) findRunning(id string) (*Thought, int) {
	for i, t := range q.running {
		if t.ID == id && t.Status == ThoughtRunning {
			return t, i
		}
	}
	return nil, -1
}

func (q *ThoughtQueue) findIn(list []*Thought, id string) (*Thought, int) {
	for i, t := range list {
		if t.ID == id {
			return t, i
		}
	}
	return nil, -1
}

// slotAvailableFor reports whether any slot could serve the thought
// right now.
func (q *ThoughtQueue) slotAvailableFor(t *Thought) bool {
	return q.findSlotFor(t) != ""
}

func (q *ThoughtQueue) save(t *Thought) error {
	if q.store == nil {
		return nil
	}
	return q.store.SaveThought(t)
}

// Retry returns a failed thought to the pending queue for
// re-acquisition (spec §2.2: failed → pending).
func (q *ThoughtQueue) Retry(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Fail parks failed thoughts in q.pending: flip in place —
	// appending again would duplicate the entry (ghost thought).
	if t, _ := q.findIn(q.pending, id); t != nil {
		if t.Status != ThoughtFailed {
			return fmt.Errorf("thought: %s: %w: status %s is not failed", id, ErrInvalidTransition, t.Status)
		}
		if err := t.Retry(); err != nil {
			return err
		}
		q.sortPending()
		q.emit([]QueueEvent{{Kind: EventEnqueued, ThoughtID: id, Detail: "retry", At: time.Now().UTC()}})
		return q.save(t)
	}

	// Coming from elsewhere (e.g. the terminal list): re-queue.
	t, err := q.lookup(id)
	if err != nil {
		return err
	}
	if err := t.Retry(); err != nil {
		return err
	}
	q.pending = append(q.pending, t)
	if _, idx := q.findIn(q.terminal, id); idx >= 0 {
		q.terminal = append(q.terminal[:idx], q.terminal[idx+1:]...)
	}
	q.sortPending()
	q.emit([]QueueEvent{{Kind: EventEnqueued, ThoughtID: id, Detail: "retry", At: time.Now().UTC()}})
	return q.save(t)
}

// collect appends an event to the pending list (call under q.mu).
func collect(evts *[]QueueEvent, kind QueueEventKind, thoughtID, detail string) {
	*evts = append(*evts, QueueEvent{Kind: kind, ThoughtID: thoughtID, Detail: detail, At: time.Now().UTC()})
}

// emit fans events out to registered observers. MUST be called without
// q.mu held — handlers may re-enter the queue.
func (q *ThoughtQueue) emit(evts []QueueEvent) {
	q.eventsMu.Lock()
	handlers := append([]func(QueueEvent){}, q.handlers...)
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
func (q *ThoughtQueue) Slots() []Slot {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Slot, len(q.slots))
	copy(out, q.slots)
	return out
}
