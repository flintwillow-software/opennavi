package thought

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newThought(id string, prio int) *Thought {
	return &Thought{ID: id, Prompt: "task " + id, Priority: prio, Status: ThoughtPending}
}

func TestEffectiveSelfPriority(t *testing.T) {
	// Clamped into the conservative band from either side.
	low := &Thought{Priority: 1}
	if low.EffectiveSelfPriority() != SelfScheduledHighBound {
		t.Errorf("prio 1 self-clamped to %d, want %d", low.EffectiveSelfPriority(), SelfScheduledHighBound)
	}
	if low.EffectivePriority() != 1 {
		t.Errorf("plain EffectivePriority must be untouched: %d", low.EffectivePriority())
	}
	band := &Thought{Priority: SelfScheduledHighBound}
	if band.EffectiveSelfPriority() != SelfScheduledHighBound {
		t.Errorf("band boundary moved: %d", band.EffectiveSelfPriority())
	}
	high := &Thought{Priority: 9}
	if high.EffectiveSelfPriority() != 9 || high.EffectiveSelfPriority() != high.EffectivePriority() {
		t.Errorf("below-band priority must pass through: %d", high.EffectiveSelfPriority())
	}
	// The clamp never produces something above the band (numerically
	// lower than SelfScheduledHighBound).
	for _, p := range []int{1, 2, 3, 4, 5, 6, 10} {
		tt := &Thought{Priority: p}
		c := tt.EffectiveSelfPriority()
		if c < SelfScheduledHighBound {
			t.Errorf("prio %d self clamped to %d — outside band", p, c)
		}
	}
}

func TestDue(t *testing.T) {
	now := time.Now().UTC()

	if !(&Thought{}).Due(now) {
		t.Error("no deferral must be due now")
	}
	past := &Thought{DeferUntil: now.Add(-(10 * time.Second))}
	if !past.Due(now) {
		t.Error("past deferral must be due")
	}
	future := &Thought{DeferUntil: now.Add(10 * time.Second)}
	if future.Due(now) {
		t.Error("future deferral must not be due")
	}
}

// ── defer_until queue mechanism ───────────────────────────

func TestQueueDefersUntilDue(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}

	// The deferral is far enough out that the assertions below cannot race a
	// slow runner. This used now+400ms, which a loaded CI runner under -race
	// can exceed between the enqueue and the dequeue.
	deferred := newThought("deferred", 1) // highest priority, but not due
	deferred.DeferUntil = time.Now().UTC().Add(time.Hour)
	if err := q.Enqueue(deferred); err != nil {
		t.Fatal(err)
	}
	immediate := newThought("immediate", 4)
	if err := q.Enqueue(immediate); err != nil {
		t.Fatal(err)
	}

	// The due thought is claimable even though the deferred one has
	// higher priority; the deferred one is skipped.
	tk, ok := q.Dequeue()
	if !ok {
		t.Fatal("due thought not claimable")
	}
	if tk.ID != "immediate" {
		t.Errorf("claimed %q, want the due thought", tk.ID)
	}
	if _, ok := q.Dequeue(); ok {
		t.Fatal("deferred thought claimed before its time")
	}
}

// TestQueueClaimsOnceDue is the other half of the deferral mechanism: once the
// not-before time has passed, the thought is claimable and back in priority
// order. It uses an already-past deferral rather than waiting on a wall clock,
// so it is deterministic on any runner.
func TestQueueClaimsOnceDue(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}

	due := newThought("deferred", 1)
	due.DeferUntil = time.Now().UTC().Add(-time.Second)
	if err := q.Enqueue(due); err != nil {
		t.Fatal(err)
	}
	lower := newThought("immediate", 4)
	if err := q.Enqueue(lower); err != nil {
		t.Fatal(err)
	}

	// Its not-before time has passed, so it is claimable and back in priority
	// order — ahead of the lower-priority immediate thought.
	tk, ok := q.Dequeue()
	if !ok {
		t.Fatal("thought not claimable once its not-before time has passed")
	}
	if tk.ID != "deferred" {
		t.Errorf("claimed %q, want the now-due deferred thought", tk.ID)
	}
}
func TestQueueDeferralSurvivesRestart(t *testing.T) {
	// Durability: the deferral is persisted, so a rebuilt queue over the same
	// store still skips a thought until due — and claims one whose time has
	// already passed. The deferrals are far-future and already-past rather
	// than a short near-future window plus a sleep, so no runner can race
	// them.
	store := NewMemoryStore()
	cfg := Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue, FlushInterval: -1} // legacy sync: the restart test needs persisted rows

	q, err := NewQueue(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	notYet := newThought("not-yet", 2)
	notYet.DeferUntil = time.Now().UTC().Add(time.Hour)
	notYet.Metadata = map[string]string{"origin": OriginTool}
	if err := q.Enqueue(notYet); err != nil {
		t.Fatal(err)
	}
	alreadyDue := newThought("already-due", 3)
	alreadyDue.DeferUntil = time.Now().UTC().Add(-time.Second)
	if err := q.Enqueue(alreadyDue); err != nil {
		t.Fatal(err)
	}

	// "Restart": a fresh queue over the same store performs crash
	// recovery and reloads both thoughts with their deferrals.
	q2, err := NewQueue(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := q2.GetThought("not-yet")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeferUntil.IsZero() {
		t.Fatal("defer_until lost across restart")
	}

	// The one whose time has passed is claimable; the other is still skipped.
	tk, ok := q2.Dequeue()
	if !ok || tk.ID != "already-due" {
		t.Fatalf("claimed %v (%v), want the thought whose not-before time has passed", tk, ok)
	}
	if _, ok := q2.Dequeue(); ok {
		t.Fatal("deferred thought claimable before its not-before time")
	}
}
func TestEnqueueClampsSelfScheduledPriority(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}

	// Self-scheduled (origin: tool) at max priority is clamped into
	// the conservative band.
	self := newThought("self", 1)
	self.Metadata = map[string]string{"origin": OriginTool}
	if err := q.Enqueue(self); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetThought("self")
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectivePriority() != SelfScheduledHighBound {
		t.Errorf("self priority = %d, want clamp band %d", got.EffectivePriority(), SelfScheduledHighBound)
	}

	// The `origin: self` spelling clamps the same way.
	self2 := newThought("self2", 1)
	self2.Metadata = map[string]string{"origin": OriginSelf}
	if err := q.Enqueue(self2); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetThought("self2")
	if got.EffectivePriority() != SelfScheduledHighBound {
		t.Errorf("origin:self priority = %d, want %d", got.EffectivePriority(), SelfScheduledHighBound)
	}

	// Untagged work keeps its priority — the clamp is scoped to
	// self-scheduled thoughts.
	external := newThought("ext", 1)
	if err := q.Enqueue(external); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetThought("ext")
	if got.EffectivePriority() != 1 {
		t.Errorf("external priority = %d, want 1 untouched", got.EffectivePriority())
	}
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
	// The floor is 0, not 1 (ADR 0035 decision 1: 0 is the urgent band).
	for _, c := range []struct{ in, want int }{{0, 0}, {-5, 0}, {5, 5}, {11, 10}, {99, 10}} {
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

	// p0 arrival with all slots full: the low thought is marked
	// preempt-requested (cooperative cancellation — it keeps running until
	// its runner yields). ADR 0035 decision 3: p0 is the cancel band.
	high := newThought("high", 0)
	if err := q.Enqueue(high); err != nil {
		t.Fatal(err)
	}
	lowCopy, err := q.GetThought("low")
	if err != nil {
		t.Fatal(err)
	}
	if !lowCopy.PreemptRequested {
		t.Fatal("victim not marked preempt-requested")
	}
	// Until the victim's worker cooperates, the slot is occupied: high
	// must NOT be claimable yet.
	if _, ok := q.Dequeue(); ok {
		t.Fatal("high claimed while victim still running")
	}

	// The worker honours the request: Pause with the runner's snapshot
	// frees the slot, and high becomes claimable.
	if err := q.PauseWithCheckpoint("low", []byte("low-checkpoint")); err != nil {
		t.Fatal(err)
	}
	got, ok := q.Dequeue()
	if !ok || got.ID != "high" {
		t.Fatalf("dequeue = %v, %v; want high", got, ok)
	}

	// The victim is paused with its checkpoint, not lost.
	victim, err := q.GetThought("low")
	if err != nil {
		t.Fatal(err)
	}
	if victim.Status != ThoughtPaused {
		t.Fatalf("victim status = %q, want paused", victim.Status)
	}
	if string(victim.Checkpoint) != "low-checkpoint" {
		t.Fatalf("victim checkpoint = %q", victim.Checkpoint)
	}
	// The flag is cleared once the pause lands.
	if victim.PreemptRequested {
		t.Fatal("PreemptRequested not cleared after pause")
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
	// But a strictly-higher-priority thought can preempt.
	if err := q.Enqueue(newThought("urgent", 0)); err != nil {
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

	if err := q.PauseWithCheckpoint("a", []byte("checkpoint-data")); err != nil {
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
	if err := q.Complete("b", ""); err != nil {
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

	// Cancelling a running thought requires pause first.
	if err := q.Cancel("r"); err == nil {
		t.Fatal("cancel of running thought should fail")
	}
	if err := q.PauseWithCheckpoint("r", nil); err != nil {
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
	if err := q.Complete("t", ""); err != nil {
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

func TestPoolCooperativePreemption(t *testing.T) {
	// End-to-end cooperative preemption: the low-priority runner blocks
	// until its ctx is cancelled, snapshots its checkpoint, and returns;
	// the pool parks it (paused, checkpoint kept) and the high-priority
	// thought runs. No work is discarded.
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())

	lowCtxErr := make(chan error, 1) // low's cancellation observation
	highGate := make(chan struct{})  // test releases high once the park is seen
	lowRuns := 0
	var lowMu sync.Mutex
	runner := RunnerFunc(func(ctx context.Context, t *Thought) error {
		if t.ID == "low" {
			lowMu.Lock()
			run := lowRuns
			lowRuns++
			lowMu.Unlock()
			if run == 0 {
				// First run: block until preempted, snapshot, return.
				<-ctx.Done()
				t.Checkpoint = []byte("low-progress")
				lowCtxErr <- ctx.Err()
				return ctx.Err()
			}
			return nil // resumed run completes normally
		}
		// high holds the slot until the test has observed low parked, so the
		// paused state is deterministic rather than racing the auto-resume
		// that high's completion triggers.
		<-highGate
		return nil
	})
	p := NewPool(q, runner, 1)
	p.Start()

	if err := q.Enqueue(newThought("low", 5)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, _ := q.GetThought("low")
		return got != nil && got.Status == ThoughtRunning
	}, "low should start")

	// p0: the cancel band (ADR 0035 decision 3). The victim is stopped via
	// ctx cancellation, not steered.
	if err := q.Enqueue(newThought("high", 0)); err != nil {
		t.Fatal(err)
	}

	// The victim parks with its checkpoint before high can complete (high is
	// blocked on the single slot the park frees), so this snapshot is stable.
	waitFor(t, func() bool {
		got, _ := q.GetThought("low")
		return got != nil && got.Status == ThoughtPaused
	}, "low should park after cancellation")
	paused, _ := q.GetThought("low")
	if string(paused.Checkpoint) != "low-progress" {
		t.Fatalf("checkpoint = %q, want runner snapshot", paused.Checkpoint)
	}
	select {
	case err := <-lowCtxErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("low runner error = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("low runner never observed cancellation")
	}
	// The cancelled park is a PREEMPTION park (auto-resumable) and carries
	// the runtime-synthesised handoff floor (ADR 0035 decision 3).
	if paused.ParkReason != ParkPreempted {
		t.Fatalf("park reason = %q, want preempted", paused.ParkReason)
	}
	if paused.HandoffNote == "" {
		t.Fatal("cancelled park has no handoff note")
	}

	// Auto-resume (ADR 0035 decision 7): let the preemptor finish, and the
	// victim is released with no operator action.
	close(highGate)
	waitFor(t, func() bool {
		got, _ := q.GetThought("low")
		return got != nil && got.Status == ThoughtComplete
	}, "auto-resumed low should complete")
	p.StopAndWait()
}

func TestPoolStopTwiceNoPanic(t *testing.T) {
	// Regression: Stop closed the done channel unguarded; a second
	// Stop panicked with 'close of closed channel'.
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	p := NewPool(q, RunnerFunc(func(_ context.Context, _ *Thought) error { return nil }), 1)
	p.Start()
	p.Stop()
	p.Stop() // must not panic
	p.StopAndWait()
	p.StopAndWait() // must not panic
}

func TestGetThoughtReturnsCopies(t *testing.T) {
	// Regression: GetThought/GetThoughts returned live internal
	// pointers — any external read raced with queue mutations.
	q, _ := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err := q.Enqueue(newThought("t", 4)); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetThought("t")
	if err != nil {
		t.Fatal(err)
	}
	got.Metadata = map[string]string{"mutated": "yes"}
	got.Status = ThoughtComplete

	fresh, _ := q.GetThought("t")
	if fresh.Status != ThoughtPending {
		t.Fatalf("status = %q after mutating the copy, want pending", fresh.Status)
	}
	if _, ok := fresh.Metadata["mutated"]; ok {
		t.Fatal("metadata mutated through copy")
	}
	// GetThoughts too.
	list := q.GetThoughts()
	if len(list) != 1 || list[0].Status != ThoughtPending {
		t.Fatalf("GetThoughts = %+v", list)
	}
	list[0].Status = ThoughtComplete
	again := q.GetThoughts()
	if again[0].Status != ThoughtPending {
		t.Fatal("GetThoughts list not copied")
	}
}

func TestTerminalThoughtsNotScheduled(t *testing.T) {
	// Terminal thoughts leave the schedulable lists but remain
	// queryable (in memory for terminal slice, in the store).
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err := q.Enqueue(newThought("t", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Complete("t", ""); err != nil {
		t.Fatal(err)
	}
	// Dequeue must not re-claim the terminal thought.
	if _, ok := q.Dequeue(); ok {
		t.Fatal("terminal thought re-claimed")
	}
	got, err := q.GetThought("t")
	if err != nil || got.Status != ThoughtComplete {
		t.Fatalf("terminal thought query: %v, %+v", err, got)
	}
}

func TestRetryNoDuplicatePendingEntry(t *testing.T) {
	// Regression: Fail parks the failed thought in q.pending; Retry
	// appended it again, leaving a ghost entry that persisted forever.
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
	if err := q.Retry("t"); err != nil {
		t.Fatal(err)
	}

	// Exactly one entry — no ghost.
	count := 0
	for _, tt := range q.GetThoughts() {
		if tt.ID == "t" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("thought t appears %d times in GetThoughts, want 1", count)
	}
	// And it's claimable.
	got, ok := q.Dequeue()
	if !ok || got.ID != "t" {
		t.Fatalf("retried thought not claimable: %v, %v", got, ok)
	}
}

func TestPoolCopyInCopyOutIsolatesRunner(t *testing.T) {
	// The runner receives a Clone: its mutations (Checkpoint AND other
	// fields) must not race with GetThought readers, and non-checkpoint
	// mutations are isolated (discarded) while the checkpoint merges
	// back through Pause.
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	runner := RunnerFunc(func(ctx context.Context, t *Thought) error {
		t.LogPath = "runner-set-this"      // isolated: must NOT leak
		t.Checkpoint = []byte("runner-cp") // merges back via Pause
		<-ctx.Done()                       // cancelled by Pool.Cancel
		return ctx.Err()
	})
	p := NewPool(q, runner, 1)
	p.Start()
	if err := q.Enqueue(newThought("low", 5)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, _ := q.GetThought("low")
		return got != nil && got.Status == ThoughtRunning
	}, "low should start")

	// Concurrent inspection while the runner mutates its clone: must be
	// race-free (run under -race on the reviewer's machine).
	p.Cancel("low")
	waitFor(t, func() bool {
		got, _ := q.GetThought("low")
		return got != nil && got.Status == ThoughtPaused
	}, "low should be parked after cancel")

	paused, _ := q.GetThought("low")
	if string(paused.Checkpoint) != "runner-cp" {
		t.Fatalf("checkpoint = %q, want runner snapshot merged back", paused.Checkpoint)
	}
	if paused.LogPath == "runner-set-this" {
		t.Fatal("runner LogPath mutation leaked into queue state")
	}
	p.StopAndWait()
}

// TestCompleteRecordsTheResult covers the completion message at the queue
// level: the worker hands the runner's reported outcome to Complete, and
// that outcome must be readable from the queue afterwards — it is the
// only place a thought's result lives apart from its session transcript.
func TestCompleteRecordsTheResult(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Thought{ID: "t-result", Prompt: "p", Priority: 5, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	claimed, ok := q.Dequeue()
	if !ok {
		t.Fatal("thought was not claimable")
	}
	if err := q.Complete(claimed.ID, "the answer"); err != nil {
		t.Fatal(err)
	}

	got, err := q.GetThought("t-result")
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != "the answer" {
		t.Fatalf("result = %q, want %q", got.Result, "the answer")
	}
	if got.Status != ThoughtComplete {
		t.Fatalf("status = %q", got.Status)
	}

	// A thought that fails carries no completion message — its detail
	// lives on the failure, and conflating the two would make "what did
	// this produce?" answerable for work that produced nothing.
	if err := q.Enqueue(&Thought{ID: "t-fail", Prompt: "p", Priority: 5, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	failed, ok := q.Dequeue()
	if !ok {
		t.Fatal("second thought was not claimable")
	}
	if err := q.Fail(failed.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	f, err := q.GetThought("t-fail")
	if err != nil {
		t.Fatal(err)
	}
	if f.Result != "" {
		t.Fatalf("a failed thought must carry no result, got %q", f.Result)
	}
}
