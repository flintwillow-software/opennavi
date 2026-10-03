package thought

import (
	"sync"
	"testing"
	"time"
)

// ── the emit contract (NAVI-67) ─────────────────────────────────────────
//
// `emit` MUST run without q.mu held, because an observer is allowed to
// re-enter the queue (GetThought/GetThoughts). Every lifecycle transition
// that emits is exercised here against exactly such an observer, each on
// its own queue so the claim is unambiguous: a site that regresses to
// emitting under the lock deadlocks, and the test names the transition
// instead of hanging the suite.

// noHang runs fn on its own goroutine and fails the test if it has not
// returned in time, naming the transition that deadlocked.
func noHang(t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s deadlocked: emit ran while q.mu was held (NAVI-67)", what)
	}
}

func TestReentrantObserversDoNotDeadlockOnAnyTransition(t *testing.T) {
	var mu sync.Mutex
	kinds := map[EventKind]int{}
	retryEvents := 0

	seen := func(kind EventKind) bool {
		mu.Lock()
		defer mu.Unlock()
		return kinds[kind] > 0
	}
	sawRetry := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return retryEvents > 0
	}

	// newQ builds a queue whose observer re-enters it the way a portal, a
	// gateway, or a connector handler legitimately does.
	newQ := func(t *testing.T) *Queue {
		t.Helper()
		q, err := NewQueue(Config{
			MaxConcurrent:     2,
			SlotExhaustion:    PolicyQueue,
			ReconcileInterval: -1,
			FlushInterval:     -1,
		}, NewMemoryStore())
		if err != nil {
			t.Fatal(err)
		}
		q.OnEvent(func(ev Event) {
			_ = q.GetThoughts()
			_, _ = q.GetThought(ev.ThoughtID)
			mu.Lock()
			kinds[ev.Kind]++
			if ev.Kind == EventEnqueued && ev.Detail == "retry" {
				retryEvents++
			}
			mu.Unlock()
		})
		return q
	}
	// enqueue and claim also assert their own events fired, so a subtest
	// run on its own still proves something.
	enqueue := func(t *testing.T, q *Queue, id string) {
		t.Helper()
		if err := q.Enqueue(newThought(id, 4)); err != nil {
			t.Fatal(err)
		}
		if !seen(EventEnqueued) {
			t.Error("Enqueue did not emit")
		}
	}
	claim := func(t *testing.T, q *Queue, id string) {
		t.Helper()
		got, ok := q.Dequeue()
		if !ok || got.ID != id {
			t.Fatalf("Dequeue = %v (ok=%v), want %s", got, ok, id)
		}
		if !seen(EventStarted) {
			t.Error("Dequeue did not emit")
		}
	}

	t.Run("Complete", func(t *testing.T) {
		q := newQ(t)
		enqueue(t, q, "complete-me")
		claim(t, q, "complete-me")
		noHang(t, "Complete", func() error { return q.Complete("complete-me", "done") })
		if !seen(EventCompleted) {
			t.Error("Complete did not emit")
		}
	})

	t.Run("PauseAndResume", func(t *testing.T) {
		q := newQ(t)
		enqueue(t, q, "park-me")
		claim(t, q, "park-me")
		noHang(t, "PauseWithCheckpoint", func() error {
			return q.PauseWithCheckpoint("park-me", []byte("navicp1:[]"))
		})
		if !seen(EventPaused) {
			t.Error("Pause did not emit")
		}
		noHang(t, "Resume", func() error { return q.Resume("park-me") })
		if !seen(EventResumed) {
			t.Error("Resume did not emit")
		}
	})

	t.Run("FailAndRetry", func(t *testing.T) {
		q := newQ(t)
		enqueue(t, q, "retry-me")
		claim(t, q, "retry-me")
		noHang(t, "Fail", func() error { return q.Fail("retry-me", "boom") })
		if !seen(EventFailed) {
			t.Error("Fail did not emit")
		}
		noHang(t, "Retry", func() error { return q.Retry("retry-me") })
		if !sawRetry() {
			t.Error("Retry did not emit its re-queue event")
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		q := newQ(t)
		enqueue(t, q, "cancel-me")
		noHang(t, "Cancel", func() error { return q.Cancel("cancel-me") })
		if !seen(EventCancelled) {
			t.Error("Cancel did not emit")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		q := newQ(t)
		enqueue(t, q, "delete-me")
		noHang(t, "Delete", func() error { return q.Delete("delete-me") })
		if !seen(EventDeleted) {
			t.Error("Delete did not emit")
		}
	})
}
