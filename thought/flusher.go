package thought

import (
	"time"
)

// ── Memory-first persistence: dirty-set + flusher (NAVI-338) ─────────
//
// Every queue mutation previously performed a synchronous store write
// WHILE HOLDING q.mu (q.save inside Enqueue/transitions/preemption) —
// disk latency serialized the runtime's critical section and
// throttled the TUI and other observers.
//
// Memory-first (NAVI-338): memory is the source of truth at runtime.
// Mutations mark thoughts dirty (in-memory, under q.mu — zero I/O in
// the critical section); a flusher goroutine persists the dirty set
// on a short cadence OUTSIDE the lock.
//
// DURABILITY-CRITICAL POINTS STAY SYNCHRONOUS — the promise is the
// kill-and-lose boundary:
//   - park-with-checkpoint (Pause) — interrupt path; the checkpoint
//     must be on disk at park time
//   - crash-recovery writes (recover()) — startup boundary
//   - external-adoption claims (reconcile) — the store row IS the
//     cross-process claim (the CLI checks for the row; an eventual
//     write would make adopted work invisible)
//   - Delete — record removal is the observable effect
//
// Loss window (documented honestly, delta): a kill -9 loses
// at most ONE flush interval of non-checkpoint mutations (by default
// 250ms). Checkpoint/park writes are synchronous, so interrupt
// durability is unchanged.
//
// Flush failures never block the queue: they are surfaced as
// EventPersistFailed (the metrics collector's persist-failure counter
// keeps working) and the failed thoughts re-mark dirty for the next
// tick.

// DefaultFlushInterval is the flusher cadence default.
const DefaultFlushInterval = 250 * time.Millisecond

// flushInterval resolves the cadence (Config.FlushInterval; zero →
// default; negative → synchronous mode, see below).
func (c Config) flushInterval() time.Duration {
	if c.FlushInterval < 0 {
		return 0 // disabled: legacy synchronous write-through
	}
	if c.FlushInterval == 0 {
		return DefaultFlushInterval
	}
	return c.FlushInterval
}

// markDirty records a thought as needing persistence — IN-MEMORY,
// under q.mu, zero I/O. The flusher drains the set outside the lock.
//
// FALLBACK: when the flusher is not running (disabled/legacy mode —
// Config.FlushInterval < 0, or StartFlusher never called), the mark
// degrades to a synchronous write — the store is the durability
// interface and the flusher is the only other writer.
//
// LOCKING: called WITH q.mu held (mutation paths). The fallback write
// runs inside that lock (legacy semantics: write-through). The
// flusher's dirty-map access is under the same lock, so the map
// manipulation is safe.
func (q *Queue) markDirty(t *Thought) {
	q.dirty[t.ID] = t.Clone()
	q.dirtyIDs[t.ID] = t
	// Memory-first mode (FlushInterval >= 0): the flusher persists —
	// zero I/O here, even before StartFlusher is called (the runtime
	// starts it at assembly; late start just means a later drain).
	// Legacy synchronous mode (FlushInterval < 0): write through now.
	if q.cfg.FlushInterval < 0 {
		if err := q.save(t); err == nil {
			delete(q.dirty, t.ID)
			delete(q.dirtyIDs, t.ID)
		}
	}
}

// StartFlusher launches the dirty-set flusher (one per queue;
// idempotent). A negative Config.FlushInterval keeps the legacy
// synchronous write-through behavior (the flusher is not started).
func (q *Queue) StartFlusher() func() {
	interval := q.cfg.flushInterval()
	if interval <= 0 {
		return func() {} // synchronous mode (legacy)
	}

	q.flushMu.Lock()
	if q.flushStop != nil {
		q.flushMu.Unlock()
		return func() {} // already running
	}
	done := make(chan struct{})
	q.flushStop = done
	q.flushMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				q.flushOnce()
			}
		}
	}()

	return func() {
		q.flushMu.Lock()
		select {
		case <-done:
		default:
			close(done)
		}
		q.flushMu.Unlock()
		// Final drain: persist whatever is left dirty at shutdown.
		q.flushOnce()
	}
}

// flushOnce drains the dirty set: pops the batch under q.mu, writes
// outside the lock. Failures surface as EventPersistFailed (never
// block the queue) and the failed thoughts re-mark dirty for the next
// tick.
func (q *Queue) flushOnce() {
	q.mu.Lock()
	if len(q.dirty) == 0 {
		q.mu.Unlock()
		return
	}
	batch := q.dirty
	q.dirty = make(map[string]*Thought)
	q.dirtyIDs = make(map[string]*Thought)
	q.mu.Unlock()

	var failed []*Thought
	for _, t := range batch {
		if err := q.enqueue(t); err != nil {
			// Re-mark for the next tick (in-memory, no lock needed —
			// markDirty takes it) and surface the failure.
			failed = append(failed, t)
		}
	}
	if len(failed) > 0 {
		evts := make([]Event, 0, len(failed))
		for _, t := range failed {
			evts = append(evts, Event{
				Kind:      EventPersistFailed,
				ThoughtID: t.ID,
				Detail:    "flush failed (will retry next tick)",
				At:        time.Now().UTC(),
			})
			q.mu.Lock()
			q.dirty[t.ID] = t
			q.dirtyIDs[t.ID] = t
			q.mu.Unlock()
		}
		q.emit(evts)
	}
}

// enqueue is the flusher's write path. It prefers the store's
// non-blocking EnqueueThought capability — a store backed by the
// datastore's ordered writer implements it — and falls back to the
// synchronous SaveThought for stores that do not.
func (q *Queue) enqueue(t *Thought) error {
	if s, ok := q.store.(interface{ EnqueueThought(*Thought) error }); ok {
		return s.EnqueueThought(t)
	}
	return q.save(t)
}

// syncSave is the durability-critical write path: persist THIS thought
// immediately (used by park-with-checkpoint, adoption claims, Delete —
// the points where the row must be on disk now). The thought is also
// cleared from the dirty set if present (the sync write supersedes it).
// NOTE: called WITH q.mu held by the mutation paths (Pause/Delete) —
// the dirty-set clear must not re-lock; the mutation of the maps here
// is under the caller's lock. Direct map access, no markDirty.
func (q *Queue) syncSave(t *Thought) error {
	err := q.save(t)
	delete(q.dirty, t.ID)
	delete(q.dirtyIDs, t.ID)
	return err
}

// markDirtyIfFlusher reports whether the flusher runs (test seam:
// mutations assert zero-I/O-in-critical-section via this).
func (q *Queue) flusherRunning() bool {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()
	return q.flushStop != nil
}

// dirtyCount exposes the pending-dirty size (diagnostics/tests).
func (q *Queue) dirtyCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.dirty)
}

// waitFlushed waits until the dirty set drains (test seam).
func (q *Queue) waitFlushed(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		if q.dirtyCount() == 0 {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
