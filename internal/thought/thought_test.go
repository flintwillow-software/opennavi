package thought

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newThought(id string, prio int) *Thought {
	return &Thought{ID: id, Prompt: "task " + id, Priority: prio, Status: ThoughtPending}
}

func TestTransitionMatrix(t *testing.T) {
	valid := map[ThoughtStatus][]ThoughtStatus{
		ThoughtPending:   {ThoughtRunning, ThoughtCancelled},
		ThoughtRunning:   {ThoughtPaused, ThoughtComplete, ThoughtFailed},
		ThoughtPaused:    {ThoughtRunning, ThoughtCancelled},
		ThoughtFailed:    {ThoughtPending},
		ThoughtComplete:  {},
		ThoughtCancelled: {},
	}
	for from, tos := range valid {
		for _, to := range tos {
			tt := &Thought{ID: "x", Status: from}
			if err := tt.Transition(to); err != nil {
				t.Errorf("%s -> %s: unexpected error %v", from, to, err)
			}
		}
	}
	// Invalid transitions must fail.
	invalid := []struct{ from, to ThoughtStatus }{
		{ThoughtPending, ThoughtComplete},
		{ThoughtPending, ThoughtPaused},
		{ThoughtComplete, ThoughtPending},
		{ThoughtCancelled, ThoughtPending},
		{ThoughtRunning, ThoughtPending}, // retry only via Retry()
	}
	for _, c := range invalid {
		tt := &Thought{ID: "x", Status: c.from}
		if err := tt.Transition(c.to); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s -> %s: error = %v, want ErrInvalidTransition", c.from, c.to, err)
		}
	}
}

func TestRetry(t *testing.T) {
	tt := &Thought{ID: "x", Status: ThoughtRunning, SlotID: "slot-1", WorkerID: "worker-1"}
	if tt.CanRetry() {
		t.Fatal("running thought cannot retry")
	}
	tt.Status = ThoughtFailed
	if !tt.CanRetry() {
		t.Fatal("failed thought should retry")
	}
	if err := tt.Retry(); err != nil {
		t.Fatal(err)
	}
	if tt.Status != ThoughtPending || tt.SlotID != "" || tt.WorkerID != "" {
		t.Fatalf("after retry: %+v", tt)
	}
	if err := tt.Retry(); err == nil {
		t.Fatal("pending thought cannot retry again")
	}
}

func TestEffectivePriorityClamps(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 1}, {-5, 1}, {5, 5}, {11, 10}, {99, 10}} {
		tt := &Thought{Priority: c.in}
		if got := tt.EffectivePriority(); got != c.want {
			t.Errorf("priority %d: got %d, want %d", c.in, got, c.want)
		}
	}
}

// ── Queue ────────────────────────────────────────────────────────────────

func TestQueueDequeuePriorityOrder(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 5, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{4, 1, 3, 2} {
		if err := q.Enqueue(newThought(fmt.Sprintf("t%d", p), p)); err != nil {
			t.Fatal(err)
		}
	}
	// Dequeue returns thoughts in priority order.
	want := []int{1, 2, 3, 4}
	for _, w := range want {
		tk, ok := q.Dequeue()
		if !ok {
			t.Fatalf("expected a thought for priority %d", w)
		}
		if tk.EffectivePriority() != w {
			t.Fatalf("got priority %d, want %d", tk.EffectivePriority(), w)
		}
		if tk.Status != ThoughtRunning || tk.SlotID == "" || tk.WorkerID == "" {
			t.Fatalf("claimed thought not running with slot: %+v", tk)
		}
	}
	if _, ok := q.Dequeue(); ok {
		t.Fatal("queue should be empty")
	}
}

func TestQueueSlotLimit(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	for i := 0; i < 5; i++ {
		if err := q.Enqueue(newThought(fmt.Sprintf("t%d", i), 4)); err != nil {
			t.Fatal(err)
		}
	}
	claimed := 0
	for {
		if _, ok := q.Dequeue(); !ok {
			break
		}
		claimed++
	}
	if claimed != 2 {
		t.Fatalf("claimed %d thoughts, want 2 (max_concurrent)", claimed)
	}
}

func TestQueuePreemption(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	low := newThought("low", 5)
	if err := q.Enqueue(low); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("low should be claimable")
	}

	// High-priority arrival with all slots full: preempts the low one.
	high := newThought("high", 1)
	if err := q.Enqueue(high); err != nil {
		t.Fatal(err)
	}
	got, ok := q.Dequeue()
	if !ok || got.ID != "high" {
		t.Fatalf("dequeue = %v, %v; want high", got, ok)
	}

	// The preempted victim is paused, not lost.
	victim, err := q.GetThought("low")
	if err != nil {
		t.Fatal(err)
	}
	if victim.Status != ThoughtPaused {
		t.Fatalf("victim status = %q, want paused", victim.Status)
	}
}

func TestQueueRejectPolicy(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyReject}, NewMemoryStore())
	if err := q.Enqueue(newThought("first", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("first should be claimable")
	}
	// All slots full, cannot preempt (equal priority): rejected.
	if err := q.Enqueue(newThought("second", 4)); err == nil {
		t.Fatal("reject policy should refuse when slots are full")
	}
	// But a high-priority thought can preempt.
	if err := q.Enqueue(newThought("urgent", 1)); err != nil {
		t.Fatalf("high-priority enqueue under reject policy: %v", err)
	}
}

func TestQueuePauseResume(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	a := newThought("a", 4)
	if err := q.Enqueue(a); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("a should be claimable")
	}

	if err := q.Pause("a", []byte("checkpoint-data")); err != nil {
		t.Fatal(err)
	}
	// Slot freed: a new thought can now be claimed.
	if err := q.Enqueue(newThought("b", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("b should be claimable after pause freed the slot")
	}

	// Resume re-queues a for claiming; the single slot is busy with b,
	// so a waits, and becomes claimable as soon as b completes.
	if err := q.Resume("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); ok {
		t.Fatal("a must wait: the only slot is busy with b")
	}
	if err := q.Complete("b"); err != nil {
		t.Fatal(err)
	}
	got, ok := q.Dequeue()
	if !ok || got.ID != "a" {
		t.Fatalf("resumed thought not claimable: %v, %v", got, ok)
	}
	if string(got.Checkpoint) != "checkpoint-data" {
		t.Fatalf("checkpoint lost on resume: %q", got.Checkpoint)
	}
}

func TestQueueCancelLifecycle(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	pending := newThought("p", 4)
	running := newThought("r", 1) // high priority: claimed first
	if err := q.Enqueue(running); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(pending); err != nil {
		t.Fatal(err)
	}
	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != "r" {
		t.Fatalf("claimed %v, want r running", claimed)
	}

	// Cancelling a running thought requires pause first (spec §2.2).
	if err := q.Cancel("r"); err == nil {
		t.Fatal("cancel of running thought should fail")
	}
	if err := q.Pause("r", nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("r"); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("p"); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetThought("p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ThoughtCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
}

func TestQueueCompleteFailRetry(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err := q.Enqueue(newThought("t", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Fail("t", "boom"); err != nil {
		t.Fatal(err)
	}
	failed, _ := q.GetThought("t")
	if failed.Status != ThoughtFailed {
		t.Fatalf("status = %q", failed.Status)
	}
	if err := q.Retry("t"); err != nil {
		t.Fatal(err)
	}
	got, ok := q.Dequeue()
	if !ok || got.ID != "t" {
		t.Fatalf("retried thought not claimable: %v, %v", got, ok)
	}
	if err := q.Complete("t"); err != nil {
		t.Fatal(err)
	}
	done, _ := q.GetThought("t")
	if done.Status != ThoughtComplete {
		t.Fatalf("status = %q, want complete", done.Status)
	}
}

func TestQueueGetThoughts(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(newThought(fmt.Sprintf("t%d", i), 4)); err != nil {
			t.Fatal(err)
		}
	}
	q.Dequeue()
	all := q.GetThoughts()
	if len(all) != 3 {
		t.Fatalf("GetThoughts = %d, want 3", len(all))
	}
}

func TestQueueDuplicateIDRejected(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err := q.Enqueue(newThought("dup", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(newThought("dup", 4)); err == nil {
		t.Fatal("duplicate ID should be rejected")
	}
}

// ── Pool ─────────────────────────────────────────────────────────────────

func TestPoolRunsEnqueuedThoughts(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 3, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	var mu sync.Mutex
	var ran []string
	p := NewPool(q, RunnerFunc(func(_ context0, t *Thought) error {
		mu.Lock()
		ran = append(ran, t.ID)
		mu.Unlock()
		return nil
	}), 3)
	p.Start()

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := q.Enqueue(newThought(id, 4)); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ran) == 5
	}, "all 5 thoughts should run")
	p.StopAndWait()

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		got, _ := q.GetThought(id)
		if got.Status != ThoughtComplete {
			t.Fatalf("thought %s status = %q, want complete", id, got.Status)
		}
	}
}

func TestPoolFailureMarksFailed(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	runner := RunnerFunc(func(_ context0, t *Thought) error {
		return fmt.Errorf("boom")
	})
	p := NewPool(q, runner, 1)
	p.Start()
	if err := q.Enqueue(newThought("t", 4)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, err := q.GetThought("t")
		return err == nil && got.Status == ThoughtFailed
	}, "thought should end failed")
	p.StopAndWait()
}

func TestPoolRespectsMaxConcurrent(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	var mu sync.Mutex
	var concurrent, peak int
	release := make(chan struct{})
	p := NewPool(q, RunnerFunc(func(_ context0, t *Thought) error {
		mu.Lock()
		concurrent++
		if concurrent > peak {
			peak = concurrent
		}
		mu.Unlock()
		<-release
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}), 4)
	p.Start()
	for i := 0; i < 6; i++ {
		if err := q.Enqueue(newThought(fmt.Sprintf("t%d", i), 4)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return concurrent == 2
	}, "2 workers should be running (slot cap)")
	mu.Lock()
	if peak > 2 {
		t.Fatalf("peak concurrency %d exceeds max_concurrent 2", peak)
	}
	mu.Unlock()
	close(release)
	p.StopAndWait()
}

// ── helpers ──────────────────────────────────────────────────────────────

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
