package thought

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── delivery scenarios ──────────────────────────────────────

func TestRouteMessageToPending(t *testing.T) {
	q, st := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "p1", Prompt: "work", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	rec := watchRouted(q)

	path, err := q.RouteMessageWithPath("p1", "extra context line")
	if err != nil {
		t.Fatalf("RouteMessage(pending): %v", err)
	}
	if path != PathContextAppend {
		t.Fatalf("path = %q, want context-append", path)
	}
	got, _ := q.GetThought("p1")
	if !strings.Contains(got.Context, "extra context line") {
		t.Fatalf("message not appended to Context: %q", got.Context)
	}
	ack := lastRouted(t, rec.list())
	if ack.ThoughtID != "p1" || ack.Delivery != string(PathContextAppend) || ack.Detail != Delivered {
		t.Fatalf("ack = %+v, want context-append/delivered", ack)
	}
	// Persisted state carries the message (survives restarts).
	persisted, _ := st.LoadThought("p1")
	if !strings.Contains(persisted.Context, "extra context line") {
		t.Fatalf("persisted Context lacks message: %q", persisted.Context)
	}
}

func TestRouteMessageToPausedResumesWithMessage(t *testing.T) {
	q, _ := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "pz", Prompt: "long task", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.PauseWithCheckpoint("pz", []byte("checkpoint")); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	path, err := q.RouteMessageWithPath("pz", "user follow-up")
	if err != nil {
		t.Fatalf("RouteMessage(paused): %v", err)
	}
	if path != PathResume {
		t.Fatalf("path = %q, want resume-with-message", path)
	}
	got, _ := q.GetThought("pz")
	if got.Status != ThoughtPending {
		t.Fatalf("status after resume = %s, want pending", got.Status)
	}
	if !strings.Contains(got.Context, "user follow-up") {
		t.Fatalf("message not attached on resume: %q", got.Context)
	}
}

func TestRouteMessageToRunningViaSteering(t *testing.T) {
	q, _ := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "r1", Prompt: "long", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}

	// Bridge records the injection; it re-enters the queue via a
	// snapshot read to prove the bridge is invoked outside q.mu.
	var bridged []string
	var mu sync.Mutex
	q.SetSteeringRouter(func(thoughtID, message string) error {
		mu.Lock()
		bridged = append(bridged, thoughtID+"|"+message)
		mu.Unlock()
		// Would deadlock if the router ran under q.mu.
		if _, err := q.GetThought(thoughtID); err != nil {
			return fmt.Errorf("snapshot failed: %w", err)
		}
		return nil
	})

	path, err := q.RouteMessageWithPath("r1", "steer: focus on tests")
	if err != nil {
		t.Fatalf("RouteMessage(running): %v", err)
	}
	if path != PathSteering {
		t.Fatalf("path = %q, want steering", path)
	}
	if len(bridged) != 1 || bridged[0] != "r1|steer: focus on tests" {
		t.Fatalf("bridge calls = %v", bridged)
	}
}

// TestRouteMessageSteeringNotUnderQueueLock proves the bridge is
// invoked outside q.mu: a bridge that re-enters the queue (GetThought)
// would deadlock if the routing call ran under the queue's mutex. The
// test fails fast on deadlock instead of hanging.
func TestRouteMessageSteeringNotUnderQueueLock(t *testing.T) {
	q, _ := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "r1", Prompt: "long", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	q.SetSteeringRouter(func(thoughtID, message string) error {
		// Re-entry attempt: deadlocks if the router is invoked while
		// the routing path holds q.mu.
		if _, err := q.GetThought(thoughtID); err != nil {
			return err
		}
		return nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := q.RouteMessageWithPath("r1", "message")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RouteMessage(running): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RouteMessage deadlocked — steering bridge ran under q.mu")
	}
}

func TestRouteMessageRunningWithoutBridge(t *testing.T) {
	q, _ := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "r1", Prompt: "long", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	rec := watchRouted(q)

	_, err := q.RouteMessageWithPath("r1", "hello")
	if !errors.Is(err, ErrSteeringUnavailable) {
		t.Fatalf("err = %v, want ErrSteeringUnavailable", err)
	}
	ack := lastRouted(t, rec.list())
	if !strings.Contains(ack.Detail, ErrSteeringUnavailable.Error()) {
		t.Fatalf("rejection ack detail = %q", ack.Detail)
	}
}

func TestRouteMessageTerminalRejections(t *testing.T) {
	q, _ := routeQueue(t)

	// Complete and cancel two thoughts.
	if err := q.Enqueue(&Thought{ID: "t1", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Complete("t1", ""); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Thought{ID: "t2", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("t2"); err != nil {
		t.Fatal(err)
	}
	rec := watchRouted(q)

	for _, id := range []string{"t1", "t2"} {
		path, err := q.RouteMessageWithPath(id, "late message")
		if err == nil {
			t.Fatalf("RouteMessage(%s) should be rejected", id)
		}
		if !errors.Is(err, ErrDeliveryRejected) {
			t.Fatalf("rejection must wrap ErrDeliveryRejected: %v", err)
		}
		if !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("rejection must name the terminal state: %v", err)
		}
		_ = path
	}

	// Failed thoughts are rejected with the retry hint.
	if err := q.Enqueue(&Thought{ID: "f1", Prompt: "flaky", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Fail("f1", "provider error"); err != nil {
		t.Fatal(err)
	}
	path, err := q.RouteMessageWithPath("f1", "nudge")
	if err == nil || !strings.Contains(err.Error(), "Retry") {
		t.Fatalf("failed-thought rejection = %v, want retry hint", err)
	}
	if path != "" {
		t.Fatalf("rejected delivery returned path %q, want empty", path)
	}

	// Every rejection emitted an ack naming the outcome.
	acks := rec.list()
	if len(acks) != 3 {
		t.Fatalf("expected 3 rejection acks, got %d", len(acks))
	}
	for _, ev := range acks {
		if !strings.HasPrefix(ev.Detail, Rejected) {
			t.Fatalf("ack detail %q, want rejected prefix", ev.Detail)
		}
	}
}

func TestRouteMessageUnknownID(t *testing.T) {
	q, _ := routeQueue(t)
	rec := watchRouted(q)
	_, err := q.RouteMessageWithPath("ghost", "hello")
	if !errors.Is(err, ErrThoughtNotFound) {
		t.Fatalf("err = %v, want ErrThoughtNotFound", err)
	}
	// The attempt itself is observable via the ack fanout.
	acks := rec.list()
	if len(acks) != 1 || acks[0].Delivery != "" || !strings.Contains(acks[0].Detail, Rejected) {
		t.Fatalf("unknown-id ack = %+v", acks)
	}
}

// TestEveryRoutedAttemptEmitsAck: delivered and rejected attempts both
// surface on the ack fanout (acknowledgment requirement).
func TestEveryRoutedAttemptEmitsAck(t *testing.T) {
	q, _ := routeQueue(t)
	// t1 enqueued first so the claim takes it (priority tie → stable
	// order); a1 stays pending for the delivery test.
	if err := q.Enqueue(&Thought{ID: "t1", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Thought{ID: "a1", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok { // claims t1
		t.Fatal("claim failed")
	}
	if err := q.Complete("t1", ""); err != nil {
		t.Fatal(err)
	}
	rec := watchRouted(q)

	// Delivered: pending path.
	if _, err := q.RouteMessageWithPath("a1", "msg"); err != nil {
		t.Fatalf("pending route: %v", err)
	}
	// Rejected: terminal.
	if _, err := q.RouteMessageWithPath("t1", "late"); err == nil {
		t.Fatal("terminal delivery should reject")
	}
	// Rejected: unknown.
	if _, err := q.RouteMessageWithPath("ghost", "msg"); err == nil {
		t.Fatal("unknown id should reject")
	}

	acks := rec.list()
	if len(acks) != 3 {
		t.Fatalf("expected 3 ack events (delivered + terminal-rejected + unknown-rejected), got %d: %+v", len(acks), acks)
	}
	if acks[0].Delivery != string(PathContextAppend) || acks[0].Detail != Delivered {
		t.Fatalf("pending ack = %+v", acks[0])
	}
	if acks[1].Delivery != "" || !strings.HasPrefix(acks[1].Detail, Rejected) {
		t.Fatalf("unknown-id ack = %+v", acks[1])
	}
}

// ── foreground slot policy ─────────────────────────────────

func TestForegroundSlotClaimedByInteractiveThought(t *testing.T) {
	// 3 slots: 1 foreground-reserved, 2 open.
	q, _ := NewQueue(Config{MaxConcurrent: 3, ForegroundSlots: 1}, NewMemoryStore())
	fg := &Thought{ID: "fg", Prompt: "interactive", Priority: 4, Status: ThoughtPending, Foreground: true}
	daemon1 := &Thought{ID: "d1", Prompt: "daemon", Priority: 4, Status: ThoughtPending}
	daemon2 := &Thought{ID: "d2", Prompt: "more", Priority: 4, Status: ThoughtPending}
	for _, th := range []*Thought{fg, daemon1, daemon2} {
		if err := q.Enqueue(th); err != nil {
			t.Fatalf("Enqueue %s: %v", th.ID, err)
		}
	}

	// Claims: the foreground thought takes the foreground slot; the
	// daemon thoughts fill the two open slots.
	for i, want := range []string{"fg", "d1", "d2"} {
		claimed, ok := q.Dequeue()
		if !ok {
			t.Fatalf("claim %d: nothing claimable", i)
		}
		if claimed.ID != want {
			t.Fatalf("claim %d = %s, want %s (slot %s)", i, claimed.ID, want, claimed.SlotID)
		}
	}

	// A fourth normal thought queues but must NOT claim the
	// foreground slot while the interactive slot is free.
	if err := q.Enqueue(&Thought{ID: "d3", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); ok {
		t.Fatal("normal thought claimed the foreground-reserved slot")
	}
}

func TestForegroundThoughtFallsBackToNormalSlot(t *testing.T) {
	// Foreground slot busy → the interactive thought claims a normal
	// slot (foreground work is schedulable like anything else).
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	if err := q.Enqueue(&Thought{ID: "fg1", Prompt: "x", Priority: 4, Status: ThoughtPending, Foreground: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok { // fg1 takes the foreground slot
		t.Fatal("first claim failed")
	}
	fg2 := &Thought{ID: "fg2", Prompt: "interactive 2", Priority: 4, Status: ThoughtPending, Foreground: true}
	if err := q.Enqueue(fg2); err != nil {
		t.Fatal(err)
	}
	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != "fg2" || claimed.SlotID == "" {
		t.Fatalf("foreground thought must fall back to the open normal slot (got %+v)", claimed)
	}
}

func TestNormalThoughtCannotTakeForegroundSlot(t *testing.T) {
	// 2 slots: 1 normal + 1 foreground. The normal thought fills the
	// normal slot; a second normal thought must NOT claim the
	// foreground slot.
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	if err := q.Enqueue(&Thought{ID: "d1", Prompt: "x", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("first claim failed")
	}
	_ = q.Enqueue(&Thought{ID: "d2", Prompt: "x", Priority: 4, Status: ThoughtPending})
	if _, ok := q.Dequeue(); ok {
		t.Fatal("foreground slot must stay reserved for interactive work")
	}

	// The interactive thought claims it even when normal slots are full.
	q2, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	_ = q2.Enqueue(&Thought{ID: "fill", Prompt: "x", Priority: 4, Status: ThoughtPending})
	if _, ok := q2.Dequeue(); !ok {
		t.Fatal("fill claim failed")
	}
	if err := q2.Enqueue(&Thought{ID: "fg", Prompt: "interactive", Priority: 4, Status: ThoughtPending, Foreground: true}); err != nil {
		t.Fatal(err)
	}
	claimed, ok := q2.Dequeue()
	if !ok || claimed.ID != "fg" {
		t.Fatalf("foreground thought must claim the reserved slot (claimed=%+v)", claimed)
	}
}

func TestForegroundPreemptionUnchanged(t *testing.T) {
	// Foreground work competes by the same priority/preemption rules:
	// a high-priority daemon thought preempts a low-priority running
	// foreground thought.
	// 1 slot: the running foreground thought occupies it — a
	// high-priority daemon thought must preempt it (same rules).
	q, _ := NewQueue(Config{MaxConcurrent: 1}, NewMemoryStore())
	_ = q.Enqueue(&Thought{ID: "fg-low", Prompt: "interactive", Priority: 5, Status: ThoughtPending, Foreground: true})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("foreground claim failed")
	}
	if err := q.Enqueue(&Thought{ID: "urgent", Prompt: "urgent", Priority: 1, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("fg-low")
	if !got.PreemptRequested {
		t.Fatalf("foreground thought was not preemptable: %+v", got)
	}
}

func TestForegroundSlotValidation(t *testing.T) {
	if _, err := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 3}, NewMemoryStore()); err == nil {
		t.Fatal("foreground_slots > max_concurrent must fail")
	}
	if _, err := NewQueue(Config{MaxConcurrent: 2, PrioritySlots: 1, ForegroundSlots: 2}, NewMemoryStore()); err == nil {
		t.Fatal("foreground+priority > max_concurrent must fail")
	}
	if _, err := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: -1}, NewMemoryStore()); err == nil {
		t.Fatal("negative foreground_slots must fail")
	}
}

func TestRouteMessagePauseResumeRoundTrip(t *testing.T) {
	// Full paused scenario: paused thought with checkpoint,
	// message arrives, thought resumes with the message attached.
	q, _ := routeQueue(t)
	if err := q.Enqueue(&Thought{ID: "w1", Prompt: "long task", Priority: 4, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.PauseWithCheckpoint("w1", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}

	rec := watchRouted(q)
	path, err := q.RouteMessageWithPath("w1", "check the logs first")
	if err != nil {
		t.Fatalf("RouteMessage(paused): %v", err)
	}
	if path != PathResume {
		t.Fatalf("path = %q, want resume-with-message", path)
	}
	got, _ := q.GetThought("w1")
	if got.Status != ThoughtPending {
		t.Fatalf("resumed status = %s, want pending", got.Status)
	}
	if !strings.Contains(got.Context, "check the logs first") {
		t.Fatalf("message not attached on resume: %q", got.Context)
	}
	if string(got.Checkpoint) != "checkpoint" {
		t.Fatalf("checkpoint lost on routed resume: %q", got.Checkpoint)
	}

	acks := rec.list()
	if len(acks) != 1 || acks[0].Delivery != string(PathResume) || acks[0].Detail != Delivered {
		t.Fatalf("pause acks = %+v", acks)
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// routeQueue builds a queue over an in-memory store with neutral
// defaults for the routing tests.
func routeQueue(t *testing.T) (*Queue, *MemoryStore) {
	t.Helper()
	st := NewMemoryStore()
	q, err := NewQueue(Config{MaxConcurrent: 4, SlotExhaustion: PolicyQueue}, st)
	if err != nil {
		t.Fatal(err)
	}
	return q, st
}

func watchRouted(q *Queue) *routeRecorder {
	rec := &routeRecorder{}
	q.OnEvent(func(ev Event) {
		if ev.Kind != EventMessageRouted {
			return
		}
		rec.mu.Lock()
		rec.acks = append(rec.acks, ev)
		rec.mu.Unlock()
	})
	return rec
}

func lastRouted(t *testing.T, acks []Event) Event {
	t.Helper()
	if len(acks) == 0 {
		t.Fatal("no routed ack events recorded")
	}
	return acks[len(acks)-1]
}

// routeRecorder captures queue events for assertions.
type routeRecorder struct {
	mu   sync.Mutex
	acks []Event
	all  []Event
}

func (r *routeRecorder) list() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.acks...)
}

// ── preemption across the foreground boundary (review #288) ─────────

func TestPreemptSkipsForegroundSlotForNonForegroundPreemptor(t *testing.T) {
	// Reviewer trace: MaxConcurrent=2, ForegroundSlots=1. Daemon prio 4
	// holds the normal slot; interactive prio 5 holds the foreground
	// slot. A high-priority NON-foreground thought must not preempt the
	// foreground victim — it can never claim the freed slot, so the
	// interactive thought would pause for nothing.
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	_ = q.Enqueue(&Thought{ID: "d1", Prompt: "daemon", Priority: 4, Status: ThoughtPending})
	_ = q.Enqueue(&Thought{ID: "fg-low", Prompt: "interactive", Priority: 5, Status: ThoughtPending, Foreground: true})
	if _, ok := q.Dequeue(); !ok { // daemon → normal slot
		t.Fatal("first claim failed")
	}
	if _, ok := q.Dequeue(); !ok { // interactive → foreground slot
		t.Fatal("second claim failed")
	}

	// Direct probe: the foreground victim is invisible to a
	// non-foreground preemptor — it picks the normal-slot victim
	// instead (prio 4), whose freed slot it can actually use.
	urgent := &Thought{ID: "urgent", Prompt: "urgent", Priority: 1, Status: ThoughtPending}
	if victim := q.preemptCandidate(urgent); victim != "d1" {
		t.Fatalf("non-foreground preemptor picked %q, want d1 (normal slot; foreground slot is unusable to it)", victim)
	}

	// A foreground preemptor of the same priority CAN take it (fg → fg
	// is type-compatible).
	urgentFG := &Thought{ID: "urgent-fg", Prompt: "interactive urgent", Priority: 1, Status: ThoughtPending, Foreground: true}
	if victim := q.preemptCandidate(urgentFG); victim != "fg-low" {
		t.Fatalf("foreground preemptor picked %q, want fg-low", victim)
	}
}

func TestPreemptNormalSlotVictimUnchanged(t *testing.T) {
	// Non-foreground preemptor still preempts normal-slot victims
	// (same priority/preemption rules; the reservation only guards
	// foreground slots).
	q, _ := NewQueue(Config{MaxConcurrent: 1}, NewMemoryStore())
	_ = q.Enqueue(&Thought{ID: "victim", Prompt: "long", Priority: 5, Status: ThoughtPending})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Enqueue(&Thought{ID: "urgent", Prompt: "urgent", Priority: 1, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("victim")
	if !got.PreemptRequested {
		t.Fatal("normal-slot victim must stay preemptable by high-priority work")
	}
}

func TestPreemptForegroundTakesFreedSlot(t *testing.T) {
	// Foreground preemptor frees the foreground slot and claims it:
	// end-to-end park → re-claim without a stray non-foreground claim.
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	_ = q.Enqueue(&Thought{ID: "d1", Prompt: "daemon", Priority: 4, Status: ThoughtPending})
	_ = q.Enqueue(&Thought{ID: "fg-low", Prompt: "interactive", Priority: 5, Status: ThoughtPending, Foreground: true})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("first claim failed")
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("second claim failed")
	}
	urgentFG := &Thought{ID: "urgent-fg", Prompt: "interactive urgent", Priority: 1, Status: ThoughtPending, Foreground: true}
	if err := q.Enqueue(urgentFG); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("fg-low")
	if !got.PreemptRequested {
		t.Fatal("foreground victim not preempted by foreground urgent work")
	}
}

// TestPreemptNoNormalCapacityStaysPending: a non-foreground
// high-priority thought does NOT preempt a foreground thought
// occupying its reserved slot when no normal capacity is free — the
// capacity genuinely belongs to the interactive session, so urgent
// work stays pending rather than parking the interactive thought for
// a slot it can never claim.
func TestPreemptNoNormalCapacityStaysPending(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	// Foreground thought holds the reserved slot; the normal slot is
	// occupied by an equal-priority (non-preemptable) thought — so no
	// normal capacity is free AND no claimable victim exists.
	_ = q.Enqueue(&Thought{ID: "fg", Prompt: "interactive", Priority: 5, Status: ThoughtPending, Foreground: true})
	_ = q.Enqueue(&Thought{ID: "peer", Prompt: "equal priority", Priority: 1, Status: ThoughtPending})
	if _, ok := q.Dequeue(); !ok { // fg → foreground slot
		t.Fatal("first claim failed")
	}
	if _, ok := q.Dequeue(); !ok { // peer → normal slot
		t.Fatal("second claim failed")
	}

	urgent := &Thought{ID: "urgent", Prompt: "urgent", Priority: 1, Status: ThoughtPending}
	if victim := q.preemptCandidate(urgent); victim != "" {
		t.Fatalf("no-capacity trace picked victim %q — must stay pending (fg slot unusable, peer not strictly lower priority)", victim)
	}
	// End-to-end: enqueue → no cooperative preemption of the
	// interactive thought; urgent waits per PolicyQueue.
	if err := q.Enqueue(urgent); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, _ := q.GetThought("fg")
	if got.PreemptRequested {
		t.Fatal("interactive thought preempted despite unclaimable freed slot")
	}
}

// TestPreemptForegroundOnNormalSlot: a non-foreground high-priority
// thought DOES preempt a foreground thought sitting on a normal slot
// (fallback usage) — the freed normal slot is claimable via the
// fallback path.
func TestPreemptForegroundOnNormalSlot(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 2, ForegroundSlots: 1}, NewMemoryStore())
	// Fill the reserved slot with another foreground thought so the
	// low-priority foreground thought falls back to the normal slot.
	_ = q.Enqueue(&Thought{ID: "fg-holder", Prompt: "interactive", Priority: 5, Status: ThoughtPending, Foreground: true})
	_ = q.Enqueue(&Thought{ID: "fg-low", Prompt: "interactive 2", Priority: 5, Status: ThoughtPending, Foreground: true})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("first claim failed")
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("second claim failed")
	}
	// Confirm fg-low actually holds the normal slot (fallback path).
	low, _ := q.GetThought("fg-low")
	if strings.HasSuffix(low.SlotID, "slot-2") { // slot-2 is the foreground slot (tail carve)
		t.Fatalf("test setup: fg-low should sit on a normal slot, got %s", low.SlotID)
	}

	// Non-foreground urgent work preempts it: freed normal slot is
	// claimable by the preemptor.
	if err := q.Enqueue(&Thought{ID: "urgent", Prompt: "urgent", Priority: 1, Status: ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetThought("fg-low"); !got.PreemptRequested {
		t.Fatal("foreground-on-normal-slot victim must be preemptable by non-foreground work")
	}
}

func TestDeleteEmitsDeletedEvent(t *testing.T) {
	// Review #291 catch: Queue.Delete must emit an event carrying the
	// PRIOR status so metrics observers (event-only) can decrement
	// the right gauge — removal is not cancellation.
	q, _ := routeQueue(t)
	rec := &routeRecorder{}
	q.OnEvent(rec.record)

	// Pending delete: Detail = "pending".
	_ = q.Enqueue(&Thought{ID: "p1", Prompt: "x", Priority: 4, Status: ThoughtPending})
	if err := q.Delete("p1"); err != nil {
		t.Fatalf("Delete(pending): %v", err)
	}
	evs := rec.allEvents()
	var deleted []Event
	for _, ev := range evs {
		if ev.Kind == EventDeleted {
			deleted = append(deleted, ev)
		}
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted events = %d, want 1 (all: %+v)", len(deleted), evs)
	}
	if deleted[0].ThoughtID != "p1" || deleted[0].Detail != string(ThoughtPending) {
		t.Fatalf("deleted event = %+v, want prior status pending in Detail", deleted[0])
	}

	// Paused delete: prior status paused.
	_ = q.Enqueue(&Thought{ID: "pz", Prompt: "x", Priority: 4, Status: ThoughtPending})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.PauseWithCheckpoint("pz", []byte("cp")); err != nil {
		t.Fatal(err)
	}
	if err := q.Delete("pz"); err != nil {
		t.Fatalf("Delete(paused): %v", err)
	}
	deleted = nil
	for _, ev := range rec.allEvents() {
		if ev.Kind == EventDeleted && ev.ThoughtID == "pz" {
			deleted = append(deleted, ev)
		}
	}
	if len(deleted) != 1 || deleted[0].Detail != string(ThoughtPaused) {
		t.Fatalf("paused delete event = %+v, want prior status paused", deleted)
	}

	// Protected deletes emit nothing.
	before := len(rec.allEvents())
	_ = q.Enqueue(&Thought{ID: "r1", Prompt: "x", Priority: 4, Status: ThoughtPending})
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	if err := q.Delete("r1"); err == nil {
		t.Fatal("running delete should be rejected")
	}
	if after := len(rec.allEvents()); after != before+2 { // enqueued+started only
		t.Fatalf("protected delete emitted events: %+v", rec.allEvents()[before:])
	}
}

// record captures every queue event (used by the delete-ack test).
func (r *routeRecorder) record(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, ev)
}

// allEvents returns every event seen by the recorder.
func (r *routeRecorder) allEvents() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.all...)
}
