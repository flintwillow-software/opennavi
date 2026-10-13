package thought

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ── NAVI-336: store-reconcile loop ───────────────────────────────────

// extStore simulates a cross-process writer: the test saves rows
// directly (as `navi enqueue` would), and the running queue adopts
// them via the reconcile loop.
type extStore struct {
	mu       sync.Mutex
	thoughts map[string]*Thought
}

func newExtStore() *extStore {
	return &extStore{thoughts: map[string]*Thought{}}
}

func (m *extStore) SaveThought(t *Thought) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thoughts[t.ID] = t.Clone()
	return nil
}

func (m *extStore) LoadThoughts() ([]*Thought, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Thought, 0, len(m.thoughts))
	for _, t := range m.thoughts {
		out = append(out, t.Clone())
	}
	return out, nil
}

func (m *extStore) LoadThought(id string) (*Thought, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.thoughts[id]
	if !ok {
		return nil, ErrThoughtNotFound
	}
	return t.Clone(), nil
}

func (m *extStore) DeleteThought(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.thoughts, id)
	return nil
}

// extWrite persists a thought row directly — the `navi enqueue`
// shape: a separate process writing the store with no in-process
// event.
func extWrite(s *extStore, t *Thought) {
	_ = s.SaveThought(t)
}

func waitReconcile(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reconcileQueue(t *testing.T, cfg Config, st *extStore) (*Queue, func()) {
	t.Helper()
	if cfg.ReconcileInterval == 0 {
		cfg.ReconcileInterval = 20 * time.Millisecond // test cadence
	}
	q, err := NewQueue(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	stop := q.StartReconcile()
	return q, stop
}

// TestReconcileAdoptsExternalPending: a thought persisted by an
// external process while the queue runs is adopted within a tick —
// it joins pending, an EventEnqueued fires (observers/TUI see it
// identically to an in-process enqueue), and Dequeue claims it.
func TestReconcileAdoptsExternalPending(t *testing.T) {
	st := newExtStore()
	rec := &routeRecorder{}
	q, stop := reconcileQueue(t, Config{MaxConcurrent: 2}, st)
	defer stop()
	q.OnEvent(rec.record)

	// External door writes the store while the queue is running (after
	// the loop's startup reconcile pass — the adoption under test is
	// the ticker path, like a real second-terminal enqueue).
	time.Sleep(60 * time.Millisecond)
	extWrite(st, &Thought{ID: "ext-1", Prompt: "from CLI", Priority: 4, Status: ThoughtPending})

	// Adopted within a tick: IN MEMORY (not just the store fallback —
	// GetThought falls back to the store for unknown ids) AND its enqueue
	// event emitted. Wait for BOTH together: the adoption and its event are
	// collected under the queue lock but the event is emitted only after the
	// lock is released, so observing the thought alone can beat the event by
	// a scheduling quantum — the race this test used to lose.
	waitReconcile(t, 2*time.Second, func() bool {
		inMemory := false
		for _, tt := range q.GetThoughts() {
			if tt.ID == "ext-1" && tt.Status == ThoughtPending {
				inMemory = true
			}
		}
		if !inMemory {
			return false
		}
		for _, ev := range rec.allEvents() {
			if ev.Kind == EventEnqueued && ev.ThoughtID == "ext-1" && strings.Contains(ev.Detail, "external-adopted") {
				return true
			}
		}
		return false
	}, "external thought adoption and its enqueue event")

	acks := rec.allEvents()
	found := false
	for _, ev := range acks {
		if ev.Kind == EventEnqueued && ev.ThoughtID == "ext-1" && strings.Contains(ev.Detail, "external-adopted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no external-adopted enqueue event: %+v", acks)
	}

	// The pool claims it like any other pending thought.
	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != "ext-1" {
		t.Fatalf("external thought not claimable: %+v", claimed)
	}
}

// TestReconcileHonorsExternalDeferUntil: an externally deferred
// thought joins pending but is NOT claimable until due.
func TestReconcileHonorsExternalDeferUntil(t *testing.T) {
	st := newExtStore()
	q, stop := reconcileQueue(t, Config{MaxConcurrent: 2}, st)
	defer stop()

	extWrite(st, &Thought{
		ID: "ext-def", Prompt: "later", Priority: 4, Status: ThoughtPending,
		DeferUntil: time.Now().UTC().Add(2 * time.Hour),
	})

	waitReconcile(t, 2*time.Second, func() bool {
		got, err := q.GetThought("ext-def")
		return err == nil && got.Status == ThoughtPending
	}, "deferred external adoption")

	// Not claimable while deferred.
	if claimed, ok := q.Dequeue(); ok {
		t.Fatalf("deferred external thought claimed before due: %+v", claimed)
	}
}

// TestReconcileNoDuplicateAdoptionAfterRestart: restart crash recovery
// parks the queue's own rows in memory; the reconcile loop must NOT
// re-adopt them (queue-owned rows reconciled to themselves).
func TestReconcileNoDuplicateAdoptionAfterRestart(t *testing.T) {
	st := newExtStore()
	// First queue persists two thoughts through normal Enqueue.
	q1, err := NewQueue(Config{MaxConcurrent: 2, ReconcileInterval: -1, FlushInterval: -1}, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := q1.Enqueue(&Thought{ID: "own-1", Prompt: "a", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if err := q1.Enqueue(&Thought{ID: "own-2", Prompt: "b", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}

	// Second queue (fresh restart) over the same store, reconcile on.
	q2, err := NewQueue(Config{MaxConcurrent: 2}, st)
	if err != nil {
		t.Fatal(err)
	}
	rec := &routeRecorder{}
	q2.OnEvent(rec.record)
	stop := q2.StartReconcile()
	defer stop()

	// After several ticks: exactly one copy of each thought — the
	// recovered one (crash recovery) is the same id the reconcile loop
	// would adopt; no duplicates, no spurious events.
	time.Sleep(100 * time.Millisecond)
	seen := map[string]int{}
	for _, t := range q2.GetThoughts() {
		seen[t.ID]++
	}
	for _, id := range []string{"own-1", "own-2"} {
		if seen[id] != 1 {
			t.Fatalf("thought %s appears %d times after restart+reconcile, want 1", id, seen[id])
		}
	}
	enqueued := 0
	for _, ev := range rec.allEvents() {
		if ev.Kind == EventEnqueued && strings.Contains(ev.Detail, "external-adopted") {
			enqueued++
		}
	}
	if enqueued != 0 {
		t.Fatalf("reconcile re-adopted queue-owned rows (%d events)", enqueued)
	}
}

// TestReconcileTerminalSkipped: externally-persisted terminal rows are
// adopted into the terminal list (inspection), not pending.
func TestReconcileTerminalSkipped(t *testing.T) {
	st := newExtStore()
	q, stop := reconcileQueue(t, Config{MaxConcurrent: 2}, st)
	defer stop()

	extWrite(st, &Thought{ID: "ext-done", Prompt: "old", Priority: 4, Status: ThoughtComplete})

	waitReconcile(t, 2*time.Second, func() bool {
		got, err := q.GetThought("ext-done")
		return err == nil && got.Status == ThoughtComplete
	}, "terminal adoption")

	// Terminal: not claimable.
	if claimed, ok := q.Dequeue(); ok && claimed.ID == "ext-done" {
		t.Fatal("terminal thought was claimed")
	}
}

// TestReconcileRunningAdoptsAsPending: an externally-written running
// row reaching a live queue is a crash artifact for THIS queue —
// adopted as pending for recovery (same transition recover() makes).
func TestReconcileRunningAdoptsAsPending(t *testing.T) {
	st := newExtStore()
	q, stop := reconcileQueue(t, Config{MaxConcurrent: 2}, st)
	defer stop()

	extWrite(st, &Thought{ID: "ext-run", Prompt: "orphaned", Priority: 4, Status: ThoughtRunning, SlotID: "slot-x", WorkerID: "worker-dead"})

	waitReconcile(t, 2*time.Second, func() bool {
		got, err := q.GetThought("ext-run")
		return err == nil && got.Status == ThoughtPending
	}, "running-artifact adoption (running → pending)")

	got, _ := q.GetThought("ext-run")
	if got.SlotID != "" || got.WorkerID != "" {
		t.Fatalf("crash-artifact slots not cleared: %+v", got)
	}
}

// TestReconcileFailedAdoptsAwaitingRetry: externally-persisted failed
// thoughts join pending awaiting Retry — adopting them as
// claimable pending would dodge the failure record.
func TestReconcileFailedAdoptsAwaitingRetry(t *testing.T) {
	st := newExtStore()
	q, stop := reconcileQueue(t, Config{MaxConcurrent: 2}, st)
	defer stop()

	extWrite(st, &Thought{ID: "ext-fail", Prompt: "flaky", Priority: 4, Status: ThoughtFailed})

	waitReconcile(t, 2*time.Second, func() bool {
		got, err := q.GetThought("ext-fail")
		return err == nil && got.Status == ThoughtFailed
	}, "failed external adoption")
}

// TestReconcileDisabledWhenNegative: a negative cadence opts out —
// no adoption ever (embedded/tested queues with no external writers).
func TestReconcileDisabledWhenNegative(t *testing.T) {
	st := newExtStore()
	q, err := NewQueue(Config{MaxConcurrent: 2, ReconcileInterval: -1}, st)
	if err != nil {
		t.Fatal(err)
	}
	stop := q.StartReconcile()
	defer stop()

	extWrite(st, &Thought{ID: "never", Prompt: "x", Priority: 4, Status: ThoughtPending})
	time.Sleep(80 * time.Millisecond)

	// Disabled: the row is NOT in the queue's memory (not claimable).
	// GetThought still finds the raw store row by its store-fallback
	// 'store passthrough, not adoption'.
	for _, tt := range q.GetThoughts() {
		if tt.ID == "never" {
			t.Fatalf("disabled reconcile adopted external rows: %+v", tt)
		}
	}
	if claimed, ok := q.Dequeue(); ok && claimed.ID == "never" {
		t.Fatal("disabled reconcile: external row claimed")
	}
}

// TestReconcileEmitsOutsideLock: the adopt path must not hold q.mu
// while emitting — a handler that re-enters the queue (GetThought)
// completes instead of deadlocking (the #280 discipline).
func TestReconcileEmitsOutsideLock(t *testing.T) {
	st := newExtStore()
	q, err := NewQueue(Config{MaxConcurrent: 2}, st)
	if err != nil {
		t.Fatal(err)
	}
	q.OnEvent(func(ev Event) {
		if ev.Kind == EventEnqueued && ev.Detail == "external-adopted" {
			// Re-entry: would deadlock if the event source held q.mu.
			_, _ = q.GetThought(ev.ThoughtID)
		}
	})
	stop := q.StartReconcile()
	defer stop()

	extWrite(st, &Thought{ID: "p1", Prompt: "x", Priority: 4, Status: ThoughtPending})

	done := make(chan struct{})
	go func() {
		waitReconcile(t, 2*time.Second, func() bool {
			got, err := q.GetThought("p1")
			return err == nil && got.Status == ThoughtPending
		}, "adoption with re-entering handler")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile deadlocked — event emitted under q.mu")
	}
}
