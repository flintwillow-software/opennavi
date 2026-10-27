package thought

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// ── NAVI-574: (priority, created_at) is the ordering ────────────────────

// arrivalStore is an ordered in-memory Store whose LoadThoughts returns
// thoughts in the queue's stated order — (priority, created_at), arrival
// second — mirroring the SQLite store's query. The plain MemoryStore returns
// a map's iteration order, which cannot express the restart contract.
type arrivalStore struct {
	mu       sync.Mutex
	thoughts map[string]*Thought
}

func newArrivalStore() *arrivalStore {
	return &arrivalStore{thoughts: make(map[string]*Thought)}
}

func (s *arrivalStore) SaveThought(t *Thought) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thoughts[t.ID] = t.Clone()
	return nil
}

func (s *arrivalStore) LoadThoughts() ([]*Thought, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Thought, 0, len(s.thoughts))
	for _, t := range s.thoughts {
		out = append(out, t.Clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if pi, pj := out[i].EffectivePriority(), out[j].EffectivePriority(); pi != pj {
			return pi < pj
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *arrivalStore) LoadThought(id string) (*Thought, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.thoughts[id]
	if !ok {
		return nil, ErrThoughtNotFound
	}
	return t.Clone(), nil
}

func (s *arrivalStore) DeleteThought(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.thoughts, id)
	return nil
}

// TestEqualPriorityOrdersByArrivalExplicitly proves the tiebreak is
// deliberate, not incidental: two thoughts of equal priority come back
// oldest-first, and the order is reversed from their insertion into the
// pending slice.
func TestEqualPriorityOrdersByArrivalExplicitly(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 4, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	// Enqueue the YOUNGER thought first, so insertion order and arrival
	// order disagree. A priority-only (or insertion-order) comparator would
	// return "younger" first; the stated rule returns "older".
	younger := newThought("younger", 4)
	younger.CreatedAt = base.Add(time.Minute)
	older := newThought("older", 4)
	older.CreatedAt = base

	if err := q.Enqueue(younger); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(older); err != nil {
		t.Fatal(err)
	}

	got, ok := q.Dequeue()
	if !ok {
		t.Fatal("nothing claimable")
	}
	if got.ID != "older" {
		t.Fatalf("claimed %q first; equal priority must be oldest-first (arrival), not insertion order", got.ID)
	}
}

// TestRestartResumesFromTheTopOfTheQueue: a restart is a fresh start, so the
// queue comes back most-urgent-first, oldest-first within a band — NOT
// most-recently-touched-first. A thought touched after a younger one must not
// be promoted over it.
func TestRestartResumesFromTheTopOfTheQueue(t *testing.T) {
	store := newArrivalStore()
	base := time.Now().UTC()

	older := newThought("older", 4)
	older.CreatedAt = base
	younger := newThought("younger", 4)
	younger.CreatedAt = base.Add(time.Minute)

	// The older thought is TOUCHED LATER (a status change / metadata write /
	// retry), so its UpdatedAt is the most recent of the two. Ordering by
	// updated_at — the previous rule — would promote it to the front.
	older.UpdatedAt = base.Add(time.Hour)
	younger.UpdatedAt = base.Add(30 * time.Minute)

	if err := store.SaveThought(older); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThought(younger); err != nil {
		t.Fatal(err)
	}

	// A fresh queue over the persisted state: crash recovery.
	q, err := NewQueue(Config{MaxConcurrent: 4, SlotExhaustion: PolicyQueue}, store)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := q.Dequeue()
	if !ok {
		t.Fatal("nothing claimable after recovery")
	}
	if got.ID != "older" {
		t.Fatalf("after restart claimed %q first; arrival order (oldest-first) must survive, not last-activity", got.ID)
	}
}

// TestRestartOrdersMostUrgentFirst pins the primary key: urgency outranks
// arrival across bands.
func TestRestartOrdersMostUrgentFirst(t *testing.T) {
	store := newArrivalStore()
	base := time.Now().UTC()

	urgentButYoung := newThought("urgent", 1)
	urgentButYoung.CreatedAt = base.Add(time.Hour)
	plannedButOld := newThought("planned", 6)
	plannedButOld.CreatedAt = base

	if err := store.SaveThought(urgentButYoung); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThought(plannedButOld); err != nil {
		t.Fatal(err)
	}

	q, err := NewQueue(Config{MaxConcurrent: 4, SlotExhaustion: PolicyQueue}, store)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := q.Dequeue()
	if got.ID != "urgent" {
		t.Fatalf("claimed %q; the more urgent thought must come first regardless of age", got.ID)
	}
}

// TestResumedThoughtKeepsItsArrivalPosition: a parked thought rejoins at its
// ORIGINAL arrival position within its band — it is not overtaken by work that
// arrived while it was parked, and it does not jump newer, more urgent work.
func TestResumedThoughtKeepsItsArrivalPosition(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()

	victim := newThought("victim", 4)
	victim.CreatedAt = base
	if err := q.Enqueue(victim); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("victim not claimable")
	}
	// Park deliberately (an operator park: no preemption edge).
	if err := q.PauseWithCheckpoint("victim", []byte("cp")); err != nil {
		t.Fatal(err)
	}

	// Work that arrives while the victim is parked, same band, LATER
	// arrival.
	newer := newThought("newer", 4)
	newer.CreatedAt = base.Add(time.Minute)
	if err := q.Enqueue(newer); err != nil {
		t.Fatal(err)
	}

	if err := q.Resume("victim"); err != nil {
		t.Fatal(err)
	}

	got, ok := q.Dequeue()
	if !ok {
		t.Fatal("nothing claimable")
	}
	if got.ID != "victim" {
		t.Fatalf("claimed %q; a resumed thought keeps its original arrival position within its band", got.ID)
	}
}

// TestResumeDoesNotJumpMoreUrgentWork: being interrupted does not promote a
// thought (ADR 0035 decision 9). A stale p7 must not resume ahead of a fresh
// p3.
func TestResumeDoesNotJumpMoreUrgentWork(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()

	victim := newThought("victim", 7)
	victim.CreatedAt = base
	if err := q.Enqueue(victim); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("victim not claimable")
	}
	if err := q.PauseWithCheckpoint("victim", nil); err != nil {
		t.Fatal(err)
	}

	// A fresh, more urgent arrival (p3) that arrived while the victim was
	// parked.
	fresh := newThought("fresh", 3)
	fresh.CreatedAt = base.Add(time.Minute)
	if err := q.Enqueue(fresh); err != nil {
		t.Fatal(err)
	}

	if err := q.Resume("victim"); err != nil {
		t.Fatal(err)
	}

	got, _ := q.Dequeue()
	if got.ID != "fresh" {
		t.Fatalf("claimed %q; priority is respected over resumption — a stale p7 must not jump a fresh p3", got.ID)
	}
}

// ── NAVI-575: strictly-higher-wins, cancel vs steer ─────────────────────

// TestPreemptionAtEveryPriority: the fixed threshold is gone, so a p5 arriving
// while a p7 runs preempts. Under the old rule the p5 was ignored.
func TestPreemptionAtEveryPriority(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(newThought("p7", 7)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("p7 not claimable")
	}
	if err := q.Enqueue(newThought("p5", 5)); err != nil {
		t.Fatal(err)
	}
	victim, _ := q.GetThought("p7")
	if victim.PreemptedBy != "p5" {
		t.Fatalf("p5 did not preempt a running p7 (PreemptedBy=%q) — the threshold must be gone", victim.PreemptedBy)
	}
}

// TestEqualPriorityDoesNotPreempt: two equally urgent things do not outrank
// each other.
func TestEqualPriorityDoesNotPreempt(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(newThought("p0-running", 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("p0 not claimable")
	}
	if err := q.Enqueue(newThought("p0-arriving", 0)); err != nil {
		t.Fatal(err)
	}
	victim, _ := q.GetThought("p0-running")
	if victim.PreemptedBy != "" || victim.PreemptRequested || victim.SteerRequested {
		t.Fatalf("equal priority must not preempt: %+v", victim)
	}
}

// TestP0CancelsAndP1Steers is the band rule (ADR 0035 decision 3): a p0
// preempts by cancellation, anything else by steering.
func TestP0CancelsAndP1Steers(t *testing.T) {
	t.Run("p0 cancels", func(t *testing.T) {
		q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
		_ = q.Enqueue(newThought("victim", 5))
		_, _ = q.Dequeue()
		_ = q.Enqueue(newThought("urgent", 0))
		got, _ := q.GetThought("victim")
		if !got.PreemptRequested {
			t.Fatal("p0 must cancel (PreemptRequested), not steer")
		}
		if got.SteerRequested {
			t.Fatal("p0 must not steer")
		}
	})

	t.Run("p1 steers", func(t *testing.T) {
		q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
		_ = q.Enqueue(newThought("victim", 5))
		_, _ = q.Dequeue()
		_ = q.Enqueue(newThought("respond", 1))
		got, _ := q.GetThought("victim")
		if got.PreemptRequested {
			t.Fatal("p1 must NOT cancel — steering is the alternative to cancellation, not a prelude to it")
		}
		if !got.SteerRequested {
			t.Fatal("p1 must steer (SteerRequested)")
		}
	})

	t.Run("p5 steers a p7", func(t *testing.T) {
		q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
		_ = q.Enqueue(newThought("victim", 7))
		_, _ = q.Dequeue()
		_ = q.Enqueue(newThought("arrival", 5))
		got, _ := q.GetThought("victim")
		if got.PreemptRequested {
			t.Fatal("a non-p0 preemption must steer, not cancel")
		}
		if !got.SteerRequested {
			t.Fatal("p5 arriving during a p7 must steer")
		}
	})
}

// TestSteerDeliversAnActionableInstruction: the victim receives the steer
// through the routing bridge, and the instruction tells it what to do.
func TestSteerDeliversAnActionableInstruction(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())

	type delivery struct{ id, msg string }
	delivered := make(chan delivery, 1)
	q.SetSteeringRouter(func(id, msg string) error {
		delivered <- delivery{id, msg}
		return nil
	})

	_ = q.Enqueue(newThought("victim", 5))
	_, _ = q.Dequeue()
	_ = q.Enqueue(newThought("respond", 1))

	select {
	case d := <-delivered:
		if d.id != "victim" {
			t.Fatalf("steer delivered to %q, want victim", d.id)
		}
		if d.msg == "" {
			t.Fatal("steer instruction is empty — the victim has nothing to act on")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steer was never delivered to the victim")
	}
}

// TestHandoffNoteWrittenOnBothPaths: the resume guarantee — "a resumed thought
// is never contextless" — holds uniformly, so the floor is written whether the
// park was a cancellation or a steer (ADR 0035 decision 3).
func TestHandoffNoteWrittenOnBothPaths(t *testing.T) {
	t.Run("cancelled park", func(t *testing.T) {
		q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
		v := newThought("victim", 5)
		v.Prompt = "do the thing"
		_ = q.Enqueue(v)
		_, _ = q.Dequeue()
		_ = q.Enqueue(newThought("urgent", 0))
		if err := q.ParkWithHandoff("victim", []byte("cp"), ParkPreempted); err != nil {
			t.Fatal(err)
		}
		got, _ := q.GetThought("victim")
		if got.HandoffNote == "" {
			t.Fatal("cancelled park left no handoff note")
		}
		if got.ParkReason != ParkPreempted {
			t.Fatalf("park reason = %q, want preempted", got.ParkReason)
		}
	})

	t.Run("steered park", func(t *testing.T) {
		q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
		v := newThought("victim", 5)
		v.Prompt = "do the thing"
		_ = q.Enqueue(v)
		_, _ = q.Dequeue()
		_ = q.Enqueue(newThought("respond", 1))
		if err := q.ParkWithHandoff("victim", []byte("cp"), ParkPreempted); err != nil {
			t.Fatal(err)
		}
		got, _ := q.GetThought("victim")
		if got.HandoffNote == "" {
			t.Fatal("steered park left no handoff note")
		}
	})
}

// TestHandoffFloorDoesNotClobberTheThoughtsOwnNote: the runtime note is a
// floor, not the mechanism — a thought that wrote its own handoff keeps it.
func TestHandoffFloorDoesNotClobberTheThoughtsOwnNote(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	v := newThought("victim", 5)
	v.Prompt = "do the thing"
	_ = q.Enqueue(v)
	_, _ = q.Dequeue()
	_ = q.Enqueue(newThought("urgent", 0))
	if err := q.AppendContext("victim", ""); err != nil {
		t.Fatal(err)
	}
	// The thought writes its own handoff while it still can.
	q.mu.Lock()
	live, _ := q.findRunning("victim")
	live.HandoffNote = "my own words: the migration is half-applied, run 003_next"
	q.mu.Unlock()

	if err := q.ParkWithHandoff("victim", nil, ParkPreempted); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("victim")
	if got.HandoffNote != "my own words: the migration is half-applied, run 003_next" {
		t.Fatalf("the runtime floor clobbered the thought's own handoff: %q", got.HandoffNote)
	}
}

// TestParkReasonDistinguishesTheTwoParks is the unit-level statement of the
// distinction auto-resume depends on: a park is a preemption park only when it
// carries BOTH the preempted reason and a live release edge.
func TestParkReasonDistinguishesTheTwoParks(t *testing.T) {
	preempted := &Thought{ID: "v", Status: ThoughtPaused, ParkReason: ParkPreempted, PreemptedBy: "p"}
	if !preempted.IsPreemptionPark() {
		t.Fatal("a preemption park with a release edge must be auto-resumable")
	}
	// A preempted reason with no edge (the edge was already cleared) is not a
	// preemption park: there is nothing left to release it.
	noEdge := &Thought{ID: "v", Status: ThoughtPaused, ParkReason: ParkPreempted}
	if noEdge.IsPreemptionPark() {
		t.Fatal("a preemption park without a release edge must not be treated as resumable")
	}
	// A deliberate park is never resumable, whatever edge it carries.
	deliberate := &Thought{ID: "v", Status: ThoughtPaused, ParkReason: ParkDeliberate, PreemptedBy: "p"}
	if deliberate.IsPreemptionPark() {
		t.Fatal("a deliberate park must never be auto-resumed")
	}
}

// TestDeliberateParkIsNotAPreemptionPark: PauseWithCheckpoint (the operator /
// tool path) records a deliberate park, which is never auto-resumed.
func TestDeliberateParkIsNotAPreemptionPark(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	_ = q.Enqueue(newThought("t", 4))
	_, _ = q.Dequeue()
	if err := q.PauseWithCheckpoint("t", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("t")
	if got.ParkReason != ParkDeliberate {
		t.Fatalf("park reason = %q, want deliberate", got.ParkReason)
	}
	if got.IsPreemptionPark() {
		t.Fatal("a deliberate park must not be treated as a preemption park")
	}
	if got.HandoffNote != "" {
		t.Fatal("a deliberate park does not get the preemption handoff floor")
	}
}

// ── NAVI-581: auto-resume on the preemptor's terminal state ─────────────

// preemptSetup runs `victim` and parks it via a strictly-higher `preemptor`
// arrival, leaving the preemptor running. It returns the queue.
func preemptSetup(t *testing.T, victimPrio, preemptorPrio int) *Queue {
	t.Helper()
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	_ = q.Enqueue(newThought("victim", victimPrio))
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("victim not claimable")
	}
	_ = q.Enqueue(newThought("preemptor", preemptorPrio))
	// The victim yields (its runner snapshotted a checkpoint); the pool parks
	// it with the handoff floor.
	if err := q.ParkWithHandoff("victim", []byte("cp"), ParkPreempted); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("preemptor not claimable after the park freed the slot")
	}
	return q
}

func TestPreemptorCompletesVictimResumes(t *testing.T) {
	q := preemptSetup(t, 7, 3)
	if err := q.Complete("preemptor", "done"); err != nil {
		t.Fatal(err)
	}
	victim, _ := q.GetThought("victim")
	if victim.Status != ThoughtPending {
		t.Fatalf("victim status = %q, want pending (auto-resumed)", victim.Status)
	}
	if victim.PreemptedBy != "" || victim.ParkReason != "" {
		t.Fatalf("release edge not cleared: PreemptedBy=%q ParkReason=%q", victim.PreemptedBy, victim.ParkReason)
	}
	got, ok := q.Dequeue()
	if !ok || got.ID != "victim" {
		t.Fatalf("victim not claimable after its preemptor completed: %v (ok=%v)", got, ok)
	}
	if string(got.Checkpoint) != "cp" {
		t.Fatalf("checkpoint lost on auto-resume: %q", got.Checkpoint)
	}
}

func TestPreemptorFailsVictimResumes(t *testing.T) {
	q := preemptSetup(t, 7, 3)
	if err := q.Fail("preemptor", "boom"); err != nil {
		t.Fatal(err)
	}
	victim, _ := q.GetThought("victim")
	if victim.Status != ThoughtPending {
		t.Fatalf("victim status = %q, want pending — a failed preemptor must not destroy the work it interrupted", victim.Status)
	}
}

func TestPreemptorCancelledVictimResumes(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	// The victim is parked by a preemptor; the preemptor is then parked by an
	// even more urgent arrival, and CANCELLED while parked.
	_ = q.Enqueue(newThought("victim", 7))
	_, _ = q.Dequeue()
	_ = q.Enqueue(newThought("preemptor", 3))
	_ = q.ParkWithHandoff("victim", []byte("cp"), ParkPreempted)
	_, _ = q.Dequeue()

	_ = q.Enqueue(newThought("top", 1))
	_ = q.ParkWithHandoff("preemptor", []byte("cp2"), ParkPreempted)

	// Cancelling a paused thought is allowed; it must still release its victim.
	if err := q.Cancel("preemptor"); err != nil {
		t.Fatal(err)
	}
	victim, _ := q.GetThought("victim")
	if victim.Status != ThoughtPending {
		t.Fatalf("victim status = %q, want pending — a cancelled preemptor must release its victim", victim.Status)
	}
}

// TestPreemptionChainUnwindsThroughTerminalStates: A parked by B, B parked by
// C. When C finishes B resumes; when B then finishes A resumes. The chain only
// unwinds if every link terminates.
func TestPreemptionChainUnwindsThroughTerminalStates(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	a := newThought("A", 7)
	a.CreatedAt = base
	_ = q.Enqueue(a)
	_, _ = q.Dequeue()

	b := newThought("B", 3)
	b.CreatedAt = base.Add(time.Second)
	_ = q.Enqueue(b)
	_ = q.ParkWithHandoff("A", []byte("cpA"), ParkPreempted)
	_, _ = q.Dequeue()

	c := newThought("C", 1)
	c.CreatedAt = base.Add(2 * time.Second)
	_ = q.Enqueue(c)
	_ = q.ParkWithHandoff("B", []byte("cpB"), ParkPreempted)
	_, _ = q.Dequeue()

	// C finishes -> B resumes.
	if err := q.Complete("C", "done"); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetThought("B"); got.Status != ThoughtPending {
		t.Fatalf("B status = %q, want pending after C finished", got.Status)
	}
	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != "B" {
		t.Fatalf("B not claimable: %v (ok=%v)", claimed, ok)
	}

	// B finishes -> A resumes.
	if err := q.Complete("B", "done"); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetThought("A"); got.Status != ThoughtPending {
		t.Fatalf("A status = %q, want pending after B finished — the chain must unwind", got.Status)
	}
}

// TestOperatorPausedThoughtIsNotAutoResumed: the queue must not resurrect work
// someone chose to stop.
func TestOperatorPausedThoughtIsNotAutoResumed(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	// A thought parked DELIBERATELY while some unrelated thought runs.
	_ = q.Enqueue(newThought("paused", 4))
	_, _ = q.Dequeue()
	if err := q.PauseWithCheckpoint("paused", nil); err != nil {
		t.Fatal(err)
	}
	_ = q.Enqueue(newThought("unrelated", 2))
	_, _ = q.Dequeue()

	// The unrelated thought finishes; the operator's pause must survive.
	if err := q.Complete("unrelated", "done"); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("paused")
	if got.Status != ThoughtPaused {
		t.Fatalf("operator-paused thought status = %q, want paused (never auto-resumed)", got.Status)
	}
}

// TestDeleteReleasesStrandedVictims: a PAUSED preemptor is deletable, and its
// deletion would cut the release edge out from under a victim it parked — so
// the delete path clears the edge and resumes the victim rather than stranding
// it. This is the reachable case: A parks B, then A is itself parked by C, so
// A sits paused with a parked victim and no terminal transition to release it.
func TestDeleteReleasesStrandedVictims(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	_ = q.Enqueue(newThought("B", 7))
	_, _ = q.Dequeue()
	_ = q.Enqueue(newThought("A", 3))
	_ = q.ParkWithHandoff("B", []byte("cpB"), ParkPreempted)
	_, _ = q.Dequeue()

	// A is now parked by C, so A is paused WITH a parked victim (B) and no
	// terminal transition in sight.
	_ = q.Enqueue(newThought("C", 1))
	_ = q.ParkWithHandoff("A", []byte("cpA"), ParkPreempted)

	if err := q.Delete("A"); err != nil {
		t.Fatalf("a paused thought is deletable: %v", err)
	}
	victim, _ := q.GetThought("B")
	if victim.Status != ThoughtPending {
		t.Fatalf("B status = %q, want pending — deleting A must not strand the victim it parked", victim.Status)
	}
	if victim.PreemptedBy != "" {
		t.Fatalf("dangling release edge left: %q", victim.PreemptedBy)
	}
}

// TestStaleSteerDoesNotSurviveAFailedRun: a steer is consumed by the run it
// was delivered to. A steered thought whose run FAILED must not carry the
// flag into a retry, or the pool would park it as a preemption park on behalf
// of a preemptor that is long gone.
func TestStaleSteerDoesNotSurviveAFailedRun(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	_ = q.Enqueue(newThought("victim", 5))
	_, _ = q.Dequeue()
	_ = q.Enqueue(newThought("respond", 1))
	if got, _ := q.GetThought("victim"); !got.SteerRequested {
		t.Fatal("setup: victim was not steered")
	}

	// The victim's run errors instead of parking itself.
	if err := q.Fail("victim", "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetThought("victim")
	if got.SteerRequested {
		t.Fatal("a failed run left the steer flag set; a retry would be spuriously parked")
	}
}

// ── pool-level: a steered victim parks at its turn boundary ─────────────

// TestPoolSteeredVictimParksAtTurnBoundary: a steered victim that returns from
// its run is parked by the runtime with the handoff floor — bounded by one
// turn, and never a cancellation.
func TestPoolSteeredVictimParksAtTurnBoundary(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	victimGate := make(chan struct{})
	preemptorGate := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, th *Thought) error {
		switch th.ID {
		case "victim":
			<-victimGate // the victim keeps running until it chooses to yield
			th.Checkpoint = []byte("victim-cp")
			return nil
		case "preemptor":
			<-preemptorGate
			return nil
		}
		return nil
	})
	p := NewPool(q, runner, 1)
	p.Start()
	defer p.StopAndWait()

	_ = q.Enqueue(newThought("victim", 5))
	waitFor(t, func() bool {
		got, _ := q.GetThought("victim")
		return got != nil && got.Status == ThoughtRunning
	}, "victim should start")

	// p1: steer, not cancel.
	_ = q.Enqueue(newThought("preemptor", 1))
	// Let the victim finish its current run; it returns normally.
	close(victimGate)

	waitFor(t, func() bool {
		got, _ := q.GetThought("victim")
		return got != nil && got.Status == ThoughtPaused
	}, "steered victim should park at the turn boundary")
	got, _ := q.GetThought("victim")
	if got.ParkReason != ParkPreempted {
		t.Fatalf("park reason = %q, want preempted", got.ParkReason)
	}
	if got.HandoffNote == "" {
		t.Fatal("steered park has no handoff note")
	}
	if string(got.Checkpoint) != "victim-cp" {
		t.Fatalf("checkpoint = %q, want the victim's own snapshot", got.Checkpoint)
	}

	// The preemptor finishing releases the victim (auto-resume).
	close(preemptorGate)
	waitFor(t, func() bool {
		g, _ := q.GetThought("victim")
		return g != nil && g.Status == ThoughtComplete
	}, "steered victim should auto-resume and complete")
}
