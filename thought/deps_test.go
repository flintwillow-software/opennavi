package thought

import (
	"slices"
	"strings"
	"testing"
)

// ── thought dependencies (NAVI-378) ─────────────────────────────────────
//
// A thought may name the thoughts it waits for; the scheduler refuses to
// claim it until they complete. These tests pin the gate, the failure
// propagation, the door validation, and durability of the graph.

// depQueue builds a two-slot queue over the in-memory store (no reconcile
// loop — these tests drive the queue directly).
func depQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := NewQueue(Config{
		MaxConcurrent:     2,
		SlotExhaustion:    PolicyQueue,
		ReconcileInterval: -1,
	}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func withDeps(t *Thought, deps ...string) *Thought {
	t.DependsOn = deps
	return t
}

func statusOf(t *testing.T, q *Queue, id string) ThoughtStatus {
	t.Helper()
	got, err := q.GetThought(id)
	if err != nil {
		t.Fatalf("GetThought(%s): %v", id, err)
	}
	return got.Status
}

// TestDependentNotClaimableUntilDependencyCompletes is the scheduler gate:
// t2 waits for t1, t3 waits for t2, and no stage can jump the queue.
func TestDependentNotClaimableUntilDependencyCompletes(t *testing.T) {
	q := depQueue(t)
	for _, tk := range []*Thought{
		newThought("t1", 4),
		withDeps(newThought("t2", 4), "t1"),
		withDeps(newThought("t3", 4), "t2"),
	} {
		if err := q.Enqueue(tk); err != nil {
			t.Fatal(err)
		}
	}

	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != "t1" {
		t.Fatalf("first claim = %v (ok=%v), want t1", claimed, ok)
	}
	if extra, ok := q.Dequeue(); ok {
		t.Fatalf("dependent %s claimable before its dependency completed", extra.ID)
	}

	if err := q.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	claimed, ok = q.Dequeue()
	if !ok || claimed.ID != "t2" {
		t.Fatalf("after t1 completes, claim = %v (ok=%v), want t2", claimed, ok)
	}
	if extra, ok := q.Dequeue(); ok {
		t.Fatalf("t3 claimable before t2 completed: %s", extra.ID)
	}

	if err := q.Complete("t2", "done"); err != nil {
		t.Fatal(err)
	}
	claimed, ok = q.Dequeue()
	if !ok || claimed.ID != "t3" {
		t.Fatalf("after t2 completes, claim = %v (ok=%v), want t3", claimed, ok)
	}
}

// TestPausedDependencyKeepsDependentsBlocked: paused is not complete, so
// parking the head of a chain parks the whole chain.
func TestPausedDependencyKeepsDependentsBlocked(t *testing.T) {
	q := depQueue(t)
	if err := q.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(withDeps(newThought("t2", 4), "t1")); err != nil {
		t.Fatal(err)
	}

	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t1 not claimable")
	}
	if err := q.Pause("t1"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); ok {
		t.Fatalf("dependent %s claimable while its dependency is paused", claimed.ID)
	}

	if err := q.Resume("t1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("resumed t1 not claimable")
	}
	if err := q.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "t2" {
		t.Fatalf("dependent not released after completion: %v (ok=%v)", claimed, ok)
	}
}

// TestPreemptionOfChainHeadKeepsDependentsBlocked: with preemption in
// play the head parks, and the chain stays blocked until it resumes and
// completes.
func TestPreemptionOfChainHeadKeepsDependentsBlocked(t *testing.T) {
	q, err := NewQueue(Config{
		MaxConcurrent:     1,
		PrioritySlots:     1,
		SlotExhaustion:    PolicyQueue,
		ReconcileInterval: -1,
	}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(newThought("t1", 3)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(withDeps(newThought("t2", 3), "t1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t1 not claimable")
	}

	// Urgent external work preempts the running head (strictly higher
	// priority: 0 < 3). p0 is the cancel band (ADR 0035 decision 3), so the
	// head is marked for cooperative cancellation.
	if err := q.Enqueue(newThought("urgent", 0)); err != nil {
		t.Fatal(err)
	}
	head, _ := q.GetThought("t1")
	if !head.PreemptRequested {
		t.Fatal("head was not marked for preemption")
	}
	// The worker honours the request by parking the head.
	if err := q.PauseWithCheckpoint("t1", nil); err != nil {
		t.Fatal(err)
	}

	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "urgent" {
		t.Fatalf("urgent work did not claim the freed slot: %v (ok=%v)", claimed, ok)
	}
	if claimed, ok := q.Dequeue(); ok {
		t.Fatalf("dependent %s claimable while the chain head is parked", claimed.ID)
	}

	// The preemptor finishing releases the chain head automatically
	// (ADR 0035 decision 7) — no operator resume — and the chain continues
	// where it left off (ADR 0015).
	if err := q.Complete("urgent", "done"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "t1" {
		t.Fatalf("resumed head did not reclaim its slot: %v (ok=%v)", claimed, ok)
	}
	if err := q.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "t2" {
		t.Fatalf("dependent not released after the head completed: %v (ok=%v)", claimed, ok)
	}
}

// TestFailedDependencyFailsDependentsTransitively: a dead dependency
// cannot hang its dependents, and the detail names the dependency that
// killed each one.
func TestFailedDependencyFailsDependentsTransitively(t *testing.T) {
	q := depQueue(t)
	var details []string
	q.OnEvent(func(ev Event) {
		if ev.Kind == EventFailed && ev.ThoughtID != "t1" {
			details = append(details, ev.ThoughtID+": "+ev.Detail)
		}
	})
	for _, tk := range []*Thought{
		newThought("t1", 4),
		withDeps(newThought("t2", 4), "t1"),
		withDeps(newThought("t3", 4), "t2"),
	} {
		if err := q.Enqueue(tk); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t1 not claimable")
	}
	if err := q.Fail("t1", "boom"); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"t2", "t3"} {
		if got := statusOf(t, q, id); got != ThoughtFailed {
			t.Fatalf("%s status = %s, want failed (dependency failed)", id, got)
		}
	}
	if len(details) != 2 {
		t.Fatalf("failure events = %v, want one per dependent", details)
	}
	if !strings.Contains(details[0], "t1") {
		t.Fatalf("t2's failure detail does not name t1: %q", details[0])
	}
	if !strings.Contains(details[1], "t2") {
		t.Fatalf("t3's failure detail does not name its own dependency t2: %q", details[1])
	}
}

// TestCancelledDependencyFailsDependents: cancellation is as terminal for
// the chain as failure.
func TestCancelledDependencyFailsDependents(t *testing.T) {
	q := depQueue(t)
	if err := q.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(withDeps(newThought("t2", 4), "t1")); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("t1"); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, q, "t2"); got != ThoughtFailed {
		t.Fatalf("dependent of a cancelled thought = %s, want failed", got)
	}
}

// TestDeletedDependencyFailsDependents: removing a record must not leave
// dependents blocked on an id that no longer exists.
func TestDeletedDependencyFailsDependents(t *testing.T) {
	q := depQueue(t)
	if err := q.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(withDeps(newThought("t2", 4), "t1")); err != nil {
		t.Fatal(err)
	}
	if err := q.Delete("t1"); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, q, "t2"); got != ThoughtFailed {
		t.Fatalf("dependent of a deleted thought = %s, want failed", got)
	}
}

// TestRetryOfDependentStaysBlockedUntilDependencySucceeds: retry re-applies
// the gate, so the documented recovery is retry-the-dependency-first.
func TestRetryOfDependentStaysBlockedUntilDependencySucceeds(t *testing.T) {
	q := depQueue(t)
	if err := q.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(withDeps(newThought("t2", 4), "t1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t1 not claimable")
	}
	if err := q.Fail("t1", "boom"); err != nil {
		t.Fatal(err)
	}

	if err := q.Retry("t2"); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, q, "t2"); got != ThoughtPending {
		t.Fatalf("retried dependent = %s, want pending", got)
	}
	if claimed, ok := q.Dequeue(); ok {
		t.Fatalf("retried dependent %s claimable while its dependency is failed", claimed.ID)
	}

	if err := q.Retry("t1"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "t1" {
		t.Fatalf("retried dependency not claimable: %v (ok=%v)", claimed, ok)
	}
	if err := q.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := q.Dequeue(); !ok || claimed.ID != "t2" {
		t.Fatalf("dependent not released after the dependency succeeded: %v (ok=%v)", claimed, ok)
	}
}

// TestEnqueueRejectsInvalidDependencies is the door: unknown ids and
// self-dependency are refused with an error naming the problem.
func TestEnqueueRejectsInvalidDependencies(t *testing.T) {
	q := depQueue(t)
	if err := q.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		tk   *Thought
		want string
	}{
		{"unknown", withDeps(newThought("t2", 4), "ghost"), "ghost"},
		{"self", withDeps(newThought("t3", 4), "t3"), "itself"},
	}
	for _, tc := range cases {
		err := q.Enqueue(tc.tk)
		if err == nil {
			t.Fatalf("%s dependency accepted at the door", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
		if _, gerr := q.GetThought(tc.tk.ID); gerr == nil {
			t.Fatalf("%s: rejected thought %s was enqueued anyway", tc.name, tc.tk.ID)
		}
	}
}

// TestEnqueueRejectsClosingDependencyCycle: the door rule (ids must
// already exist) makes a cycle unreachable for in-process enqueues, but a
// graph adopted from an external writer can already point at an id, and
// the closing edge must be refused rather than wedging the chain.
func TestEnqueueRejectsClosingDependencyCycle(t *testing.T) {
	q := depQueue(t)

	// Simulate a row that already depends on "t" (only reachable off the
	// door path — an external row written before its predecessor exists).
	q.mu.Lock()
	q.pending = append(q.pending, withDeps(newThought("elsewhere", 4), "t"))
	q.mu.Unlock()

	err := q.Enqueue(withDeps(newThought("t", 4), "elsewhere"))
	if err == nil {
		t.Fatal("edge closing a dependency cycle accepted at the door")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %v, want a cycle rejection", err)
	}
}

// TestRecoveryPreservesDependencies: the graph is durable, and a
// dependency recovered as complete satisfies its dependents.
func TestRecoveryPreservesDependencies(t *testing.T) {
	store := NewMemoryStore()
	// Legacy synchronous writes: the restart test reads rows back.
	cfg := Config{MaxConcurrent: 2, SlotExhaustion: PolicyQueue, ReconcileInterval: -1, FlushInterval: -1}

	q1, err := NewQueue(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := q1.Enqueue(newThought("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := q1.Enqueue(withDeps(newThought("t2", 4), "t1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := q1.Dequeue(); !ok {
		t.Fatal("t1 not claimable")
	}
	if err := q1.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}

	q2, err := NewQueue(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := q2.GetThought("t2")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.DependsOn, []string{"t1"}) {
		t.Fatalf("recovered dependencies = %v, want [t1]", got.DependsOn)
	}
	claimed, ok := q2.Dequeue()
	if !ok || claimed.ID != "t2" {
		t.Fatalf("dependent not claimable on a completed dependency after recovery: %v (ok=%v)", claimed, ok)
	}
}

// TestRecoveryFailsDependentsOfADeadDependency: a dependency that is
// already failed when the graph loads can never release its dependents,
// so recovery fails them instead of leaving them blocked forever.
func TestRecoveryFailsDependentsOfADeadDependency(t *testing.T) {
	store := NewMemoryStore()
	if err := store.SaveThought(&Thought{ID: "t1", Prompt: "dead", Priority: 4, Status: ThoughtFailed}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThought(&Thought{ID: "t2", Prompt: "blocked", Priority: 4, Status: ThoughtPending, DependsOn: []string{"t1"}}); err != nil {
		t.Fatal(err)
	}

	q, err := NewQueue(Config{MaxConcurrent: 2, ReconcileInterval: -1, FlushInterval: -1}, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, q, "t2"); got != ThoughtFailed {
		t.Fatalf("dependent of a recovered failed dependency = %s, want failed", got)
	}
}

// TestUnmetDependencies pins the display derivation: only complete
// satisfies, and an id that cannot be resolved stays unmet.
func TestUnmetDependencies(t *testing.T) {
	all := []*Thought{
		{ID: "a", Status: ThoughtComplete},
		{ID: "b", Status: ThoughtRunning},
		{ID: "c", Status: ThoughtFailed},
	}
	tk := &Thought{ID: "x", Status: ThoughtPending, DependsOn: []string{"a", "b", "c", "ghost"}}
	got := UnmetDependencies(tk, all)
	if want := []string{"b", "c", "ghost"}; !slices.Equal(got, want) {
		t.Fatalf("UnmetDependencies = %v, want %v", got, want)
	}
	if got := UnmetDependencies(&Thought{ID: "y"}, all); got != nil {
		t.Fatalf("thought without dependencies = %v, want nil", got)
	}
}

// TestThoughtCloneCopiesDependencies: the queue hands out clones, so a
// shared slice would let a reader mutate scheduler state.
func TestThoughtCloneCopiesDependencies(t *testing.T) {
	orig := withDeps(newThought("t1", 4), "dep")
	cp := orig.Clone()
	cp.DependsOn[0] = "mutated"
	if orig.DependsOn[0] != "dep" {
		t.Fatalf("Clone aliased the dependency slice: original = %v", orig.DependsOn)
	}
}
