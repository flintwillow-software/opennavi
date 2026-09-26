package thought

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── NAVI-338: memory-first dirty-set flusher ─────────────────────────

// flushStore counts synchronous writes (the test's observability of
// the I/O boundary).
type flushStore struct {
	mu     sync.Mutex
	rows   map[string]*Thought
	writes int
}

func newFlushStore() *flushStore { return &flushStore{rows: map[string]*Thought{}} }

func (s *flushStore) SaveThought(t *Thought) error {
	s.mu.Lock()
	s.writes++
	s.rows[t.ID] = t.Clone()
	s.mu.Unlock()
	return nil
}

func (s *flushStore) LoadThoughts() ([]*Thought, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Thought, 0, len(s.rows))
	for _, t := range s.rows {
		out = append(out, t.Clone())
	}
	return out, nil
}

func (s *flushStore) LoadThought(id string) (*Thought, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rows[id]
	if !ok {
		return nil, ErrThoughtNotFound
	}
	return t.Clone(), nil
}

func (s *flushStore) DeleteThought(id string) error {
	s.mu.Lock()
	delete(s.rows, id)
	s.mu.Unlock()
	return nil
}

func (s *flushStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func flusherQueue(t *testing.T, interval time.Duration) (*Queue, *flushStore) {
	t.Helper()
	st := newFlushStore()
	q, err := NewQueue(Config{MaxConcurrent: 2, FlushInterval: interval}, st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s := q.StartFlusher(); s != nil {
			// no-op when already running; drain semantics either way
		}
	})
	return q, st
}

// TestFlusherPersistsOnCadence: mutations are dirty in-memory (zero
// synchronous I/O), the flusher persists them on the cadence.
func TestFlusherPersistsOnCadence(t *testing.T) {
	st := newFlushStore()
	q, err := NewQueue(Config{MaxConcurrent: 2, FlushInterval: 20 * time.Millisecond}, st)
	if err != nil {
		t.Fatal(err)
	}
	// Enqueue WITHOUT starting the flusher — the mutation must be
	// zero-I/O (no synchronous write), sitting dirty in memory.
	if err := q.Enqueue(&Thought{ID: "f1", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if writes := st.writeCount(); writes != 0 {
		t.Fatalf("critical section performed %d sync writes, want 0 (memory-first)", writes)
	}
	if q.dirtyCount() != 1 {
		t.Fatalf("dirty set = %d, want 1", q.dirtyCount())
	}

	// Start the flusher: it drains the dirty set on cadence.
	stop := q.StartFlusher()
	defer stop()
	waitFor(t, func() bool { return st.writeCount() >= 1 }, "flusher persisted the dirty thought")
	if q.dirtyCount() != 0 {
		t.Fatalf("dirty set not drained: %d", q.dirtyCount())
	}
}

// TestSyncPointsFlushImmediately: durability-critical writes (park
// with checkpoint, delete) are synchronous — on disk at call time.
func TestSyncPointsFlushImmediately(t *testing.T) {
	st := newFlushStore()
	q, err := NewQueue(Config{MaxConcurrent: 1, FlushInterval: 10 * time.Second}, st)
	if err != nil {
		t.Fatal(err)
	}
	stop := q.StartFlusher() // long cadence: sync paths must not wait for it
	defer stop()

	_ = q.Enqueue(&Thought{ID: "pk", Prompt: "x", Priority: 4, Status: ThoughtPending})
	writesAtEnqueue := st.writeCount()
	if writesAtEnqueue == 0 {
		// dirty (enqueue marks dirty); the 10s flusher hasn't ticked.
	} else {
		t.Fatalf("enqueue wrote synchronously (%d writes) — critical section violated", writesAtEnqueue)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}

	// Park (Pause with checkpoint) — SYNCHRONOUS: on disk at call time.
	if err := q.PauseWithCheckpoint("pk", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if st.writeCount() <= writesAtEnqueue {
		t.Fatal("park did not write synchronously (checkpoint not on disk)")
	}
	got, _ := q.GetThought("pk")
	if string(got.Checkpoint) != "checkpoint" {
		t.Fatalf("checkpoint lost: %q", got.Checkpoint)
	}
}

// TestFlushFailureSurfacesWithoutBlocking: a failing store surfaces
// EventPersistFailed and keeps the thoughts dirty for the next tick —
// the queue keeps running.
func TestFlushFailureSurfacesWithoutBlocking(t *testing.T) {
	// failingStore errors on save.
	st := &failingStore{flushStore: *newFlushStore(), fail: true}
	q, err := NewQueue(Config{MaxConcurrent: 2, FlushInterval: 20 * time.Millisecond}, st)
	if err != nil {
		t.Fatal(err)
	}
	var failed []Event
	done := make(chan struct{})
	q.OnEvent(func(ev Event) {
		if ev.Kind == EventPersistFailed {
			failed = append(failed, ev)
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	if err := q.Enqueue(&Thought{ID: "ff", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	stop := q.StartFlusher()
	defer stop()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flush failure never surfaced")
	}
	if len(failed) != 1 || !strings.Contains(failed[0].Detail, "flush failed") {
		t.Fatalf("persist-failed events = %+v", failed)
	}
	// Queue keeps running: the thought stays pending in memory.
	if q.dirtyCount() == 0 {
		t.Fatal("failed flush dropped the thought — not re-marked dirty")
	}
}

type failingStore struct {
	flushStore
	fail bool
}

func (s *failingStore) SaveThought(t *Thought) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	if s.fail {
		return errors.New("simulated flush failure")
	}
	return s.flushStore.SaveThought(t)
}

// TestFlushIntervalLegacySynchronousMode: negative FlushInterval keeps
// the legacy write-through behavior (no flusher; mutations persist
// synchronously).
func TestFlushIntervalLegacySynchronousMode(t *testing.T) {
	st := newFlushStore()
	q, err := NewQueue(Config{MaxConcurrent: 2, FlushInterval: -1}, st)
	if err != nil {
		t.Fatal(err)
	}
	if q.flusherRunning() {
		t.Fatal("flusher started in legacy synchronous mode")
	}
	if err := q.Enqueue(&Thought{ID: "leg", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if st.writeCount() == 0 {
		t.Fatal("legacy mode must write synchronously")
	}
	if q.dirtyCount() != 0 {
		t.Fatalf("legacy mode left dirty entries: %d", q.dirtyCount())
	}
}

// TestShutdownFinalDrain: the flusher's stop closure performs a final
// drain — dirty thoughts at shutdown are persisted (the park path may
// have marked things after the last tick).
func TestShutdownFinalDrain(t *testing.T) {
	st := newFlushStore()
	q, err := NewQueue(Config{MaxConcurrent: 2, FlushInterval: 10 * time.Second}, st)
	if err != nil {
		t.Fatal(err)
	}
	stop := q.StartFlusher()
	if err := q.Enqueue(&Thought{ID: "final", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	// Stop before the first tick: the final drain must persist.
	stop()
	if st.writeCount() == 0 {
		t.Fatal("final drain did not persist the dirty thought")
	}
	if q.dirtyCount() != 0 {
		t.Fatalf("final drain left dirty entries: %d", q.dirtyCount())
	}
}
