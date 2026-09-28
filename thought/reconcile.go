package thought

import (
	"time"
)

// ── Store-reconcile loop (external-writer reconciliation,
// NAVI-336) ───────────────────────────────────────────────────────────
//
// The queue is the runtime's main loop, but the store is the
// cross-process channel: `navi enqueue` (and cron-from-another-
// process, and any external tool) persists thoughts directly to
// SQLite with no in-process event. recover() reads the store ONCE at
// startup; a running queue would never learn about externally
// persisted work — it would neither appear in observers nor be
// claimed by the pool.
//
// The reconcile loop closes that gap: on a ticker it LoadThoughts and
// adopts unseen non-terminal thoughts exactly as an in-process
// enqueue would — join q.pending (slot policy via the normal sort),
// emit EventEnqueued after unlock (the deferred collect/emit
// discipline — observers, TUI, and reactive doors see an identical
// event), honor DeferUntil (the adopted thought joins pending and
// Dequeue skips it until due — existing machinery).
//
// Adopt-vs-own: a thought id already known to the queue is SKIPPED —
// its store row is the queue's own write (Enqueue/Pause/Complete all
// persist). Terminal thoughts are skipped (crash recovery already
// parked them in q.terminal for inspection). This is what makes
// restart + reconcile idempotent: after recovery the queue knows its
// recovered ids and adopts nothing.
//
// SINGLE-NAVI-PER-DB ASSUMPTION (documented, NAVI-325 review): the
// thoughts table has NO navi_id (id TEXT PRIMARY KEY) — unlike the
// stateful tool stores, which key (navi_id, id). Two running navis
// pointing at one DB would BOTH adopt the same external thought and
// race on it (a pre-existing hazard: they already recover the same
// pending rows at startup). The supported deployment is one navi per
// DB path (distinct DB paths per navi today); cross-navi adoption
// exclusion (adopted-by marker) and full per-navi scoping of the
// thoughts table are tracked as a future issue — out of scope here.

// DefaultReconcileInterval is the reconcile cadence default.
const DefaultReconcileInterval = time.Second

// Config knob: ReconcileInterval (NAVI-336). Zero → default (1s);
// negative → the loop does not start (opt-out for embedded/tested
// queues where no external writers exist).
func (c Config) reconcileInterval() time.Duration {
	if c.ReconcileInterval < 0 {
		return 0 // disabled
	}
	if c.ReconcileInterval == 0 {
		return DefaultReconcileInterval
	}
	return c.ReconcileInterval
}

// StartReconcile launches the store-reconcile loop on a goroutine
// (one per queue; safe to call once — subsequent calls are no-ops).
// The loop stops when the returned stop function is invoked (the
// runtime calls it on shutdown) or when the cadence is disabled.
// Reconcile ticks never block the queue: LoadThoughts and emit run
// outside q.mu per the discipline.
func (q *Queue) StartReconcile() func() {
	interval := q.cfg.reconcileInterval()
	if interval <= 0 {
		return func() {}
	}

	q.reconcileOnce()

	q.reconcileMu.Lock()
	if q.reconcileStop != nil {
		q.reconcileMu.Unlock()
		return func() {} // already running
	}
	done := make(chan struct{})
	q.reconcileStop = done
	q.reconcileMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				q.reconcileOnce()
			}
		}
	}()

	return func() {
		q.reconcileMu.Lock()
		select {
		case <-done:
		default:
			close(done)
		}
		q.reconcileMu.Unlock()
	}
}

// reconcileOnce performs one adoption pass. State mutations happen
// under q.mu; LoadThoughts and emit run outside it (the store may
// take its own locks; emit fanout handlers must never find q.mu held
// by an event source).
func (q *Queue) reconcileOnce() {
	loaded, err := q.store.LoadThoughts()
	if err != nil {
		return // transient store error — next tick retries
	}

	q.mu.Lock()
	var evts []Event
	adopted := 0
	for _, t := range loaded {
		if _, known := q.findByID(t.ID); known {
			continue // queue-owned row (or already adopted)
		}
		switch t.Status {
		case ThoughtPending:
			q.pending = append(q.pending, t)
			q.sortPending()
			evts = append(evts, Event{
				Kind:      EventEnqueued,
				ThoughtID: t.ID,
				Detail:    "external-adopted",
				At:        time.Now().UTC(),
			})
			adopted++
		case ThoughtPaused:
			// Externally paused work stays paused (not claimable);
			// record it so Resume can find it, no event.
			q.paused = append(q.paused, t)
			adopted++
		case ThoughtFailed:
			// Failed joins pending awaiting Retry (retry-in-place):
			// the row stays Status=failed, so Dequeue's transition gate
			// skips it — adoption just makes it visible to Retry/observers
			// without dodging the failure record.
		case ThoughtComplete, ThoughtCancelled:
			q.terminal = append(q.terminal, t)
			adopted++
		default:
			// Running: the store row says a worker claimed it, but a
			// running navi's own claims are already in-memory (queue-
			// owned). An externally-written running row is a crash
			// artifact for THIS queue — treat as pending for recovery
			// (same transition recover() makes).
			t.Status = ThoughtPending
			t.SlotID = ""
			t.WorkerID = ""
			q.pending = append(q.pending, t)
			q.sortPending()
			evts = append(evts, Event{
				Kind:      EventEnqueued,
				ThoughtID: t.ID,
				Detail:    "external-adopted",
				At:        time.Now().UTC(),
			})
			adopted++
		}
	}
	q.mu.Unlock()

	if adopted > 0 {
		q.emit(evts)
	}
}
