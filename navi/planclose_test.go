package navi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// ── the closing check (ADR 0016) ────────────────────────────────────────

// recordingRunner records the order thoughts ran in, can fail named
// stages, and (when gated) holds every run until the test releases it.
//
// The gate matters: a live navi with free workers will claim and finish a
// stage the instant it is enqueued, so staging a plan one stage at a time
// lets the navi observe a *partial* plan and close it (see NAVI-382). Real
// plans are staged by a running planner thought, which is not the case in
// these tests, so the test stages the whole plan behind the gate.
type recordingRunner struct {
	mu    sync.Mutex
	order []string
	fail  map[string]bool
	gate  chan struct{}
}

func newRecordingRunner() *recordingRunner {
	return &recordingRunner{gate: make(chan struct{})}
}

// release lets every gated run proceed.
func (r *recordingRunner) release() { close(r.gate) }

func (r *recordingRunner) Run(_ context.Context, t *thought.Thought) error {
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	r.order = append(r.order, t.ID)
	shouldFail := r.fail[t.ID]
	r.mu.Unlock()
	if shouldFail {
		return fmt.Errorf("stage %s failed", t.ID)
	}
	return nil
}

func (r *recordingRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// stagingRunner runs a thought the moment the queue claims it, except the
// thoughts named as holds: those block until released (or the run is
// cancelled).
//
// Unlike recordingRunner's gate, a plan staged under this runner is staged
// the way production stages it — one stage enqueued, claimed, and finished
// before the next exists (NAVI-382) — so it pins the real behaviour rather
// than a gated approximation. The holds exist only to keep workers busy so
// a synthesized check stays *pending* while the test grows the plan around
// it.
type stagingRunner struct {
	mu    sync.Mutex
	holds map[string]chan struct{}
}

func newStagingRunner(holdIDs ...string) *stagingRunner {
	r := &stagingRunner{holds: make(map[string]chan struct{}, len(holdIDs))}
	for _, id := range holdIDs {
		r.holds[id] = make(chan struct{})
	}
	return r
}

// release lets the named hold run. Releasing an unknown or already
// released hold is a no-op.
func (r *stagingRunner) release(id string) {
	r.mu.Lock()
	ch := r.holds[id]
	delete(r.holds, id)
	r.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// releaseAll lets every remaining hold run, so shutdown never waits on a
// test's hold.
func (r *stagingRunner) releaseAll() {
	r.mu.Lock()
	chs := make([]chan struct{}, 0, len(r.holds))
	for _, ch := range r.holds {
		chs = append(chs, ch)
	}
	r.holds = make(map[string]chan struct{})
	r.mu.Unlock()
	for _, ch := range chs {
		close(ch)
	}
}

func (r *stagingRunner) Run(ctx context.Context, t *thought.Thought) error {
	r.mu.Lock()
	ch := r.holds[t.ID]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// stage enqueues a pending thought, optionally depending on earlier stages.
func stage(t *testing.T, n *Navi, id string, deps ...string) {
	t.Helper()
	stagePrio(t, n, id, 4, deps...)
}

// stagePrio is stage with an explicit priority. Tests that need to decide
// which thought a freed worker claims next set it here: Enqueue clamps
// self-scheduled work into one band, so priority plus stable queue order is
// the scheduling a test can rely on.
func stagePrio(t *testing.T, n *Navi, id string, priority int, deps ...string) {
	t.Helper()
	now := time.Now().UTC()
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: id, Prompt: "stage " + id, Priority: priority, Status: thought.ThoughtPending,
		DependsOn: deps, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// waitForThought polls the queue for a thought matching want.
func waitForThought(t *testing.T, n *Navi, want func(*thought.Thought) bool, what string) *thought.Thought {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, x := range n.Thoughts.GetThoughts() {
			if want(x) {
				return x
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

// verifications returns every synthesized closing check on the queue.
func verifications(n *Navi) []*thought.Thought {
	var out []*thought.Thought
	for _, x := range n.Thoughts.GetThoughts() {
		if strings.HasPrefix(x.ID, verifyIDPrefix) {
			out = append(out, x)
		}
	}
	return out
}

// TestDrainedPlanGetsAVerificationCheck is the acceptance gate: a staged
// task leaves a verification thought behind, it depends on the plan's last
// stage, it is identifiable as a check, and it runs after the plan.
func TestDrainedPlanGetsAVerificationCheck(t *testing.T) {
	r := newRecordingRunner()
	n := New("planclose", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	stage(t, n, "t3", "t2")
	r.release()
	waitStatus(t, n, "t3", "complete")

	check := waitForThought(t, n, func(x *thought.Thought) bool {
		return strings.HasPrefix(x.ID, verifyIDPrefix)
	}, "the synthesized verification thought")

	if !check.IsVerification() {
		t.Fatalf("check is not tagged as a verification: %v", check.Metadata)
	}
	if check.Metadata[thought.MetadataOrigin] != thought.OriginSelf {
		t.Fatalf("check origin = %q, want %s (conservative priority band)", check.Metadata[thought.MetadataOrigin], thought.OriginSelf)
	}
	if check.Metadata[thought.MetadataPlan] == "" {
		t.Fatalf("check carries no plan id: %v", check.Metadata)
	}
	if len(check.DependsOn) != 1 || check.DependsOn[0] != "t3" {
		t.Fatalf("check depends_on = %v, want the plan's sink [t3]", check.DependsOn)
	}
	for _, want := range []string{"t1", "t2", "t3", "Verify what the plan actually produced"} {
		if !strings.Contains(check.Prompt, want) {
			t.Fatalf("check prompt does not mention %q:\n%s", want, check.Prompt)
		}
	}

	// It is released by its dependency and runs last, after the plan.
	waitStatus(t, n, check.ID, "complete")
	ran := r.ran()
	if len(ran) != 4 || ran[3] != check.ID {
		t.Fatalf("run order = %v, want the check last", ran)
	}
	if len(verifications(n)) != 1 {
		t.Fatalf("a drained plan produced %d checks, want 1", len(verifications(n)))
	}
}

// TestFailedPlanLeavesNoVerificationCheck: a plan that did not succeed is
// not closed with a check — there is nothing verified to report.
func TestFailedPlanLeavesNoVerificationCheck(t *testing.T) {
	r := newRecordingRunner()
	r.fail = map[string]bool{"t2": true}
	n := New("planclose-fail", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	stage(t, n, "t3", "t2")
	r.release()
	waitStatus(t, n, "t2", "failed")
	waitStatus(t, n, "t3", "failed") // failed transitively, by design (NAVI-378)

	// Give the listener a sweep window: it must not queue anything.
	time.Sleep(250 * time.Millisecond)
	if got := verifications(n); len(got) != 0 {
		t.Fatalf("a failed plan was closed with %d check(s): %v", len(got), ids(got))
	}
}

// TestModelQueuedVerificationSuppressesSynthesis: when the model closes
// its own plan, the runtime stays out of the way — and a verification
// never spawns a verification (no recursion).
func TestModelQueuedVerificationSuppressesSynthesis(t *testing.T) {
	r := &recordingRunner{}
	n := New("planclose-model", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	now := time.Now().UTC()
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "human-check", Prompt: "check the plan", Priority: 4, Status: thought.ThoughtPending,
		DependsOn: []string{"t1"},
		Metadata:  map[string]string{thought.MetadataRole: thought.RoleVerification},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	waitStatus(t, n, "human-check", "complete")
	time.Sleep(250 * time.Millisecond)
	if got := verifications(n); len(got) != 0 {
		t.Fatalf("runtime synthesized a check despite the plan's own: %v", ids(got))
	}
	if len(r.ran()) != 2 {
		t.Fatalf("ran %v, want just the stage and its check", r.ran())
	}
}

// TestPlanCloseCanBeDisabled: authors who do not want the extra thought
// can turn it off (the guidance still asks the model to close its plan).
func TestPlanCloseCanBeDisabled(t *testing.T) {
	r := newRecordingRunner()
	r.release()
	n := New("planclose-off", WithRunner(r), WithWorkers(2), WithPlanCloseCheck(false))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	waitStatus(t, n, "t2", "complete")

	time.Sleep(250 * time.Millisecond)
	if got := verifications(n); len(got) != 0 {
		t.Fatalf("closing check queued while disabled: %v", ids(got))
	}
}

// TestForkedPlanClosesOnce: parallel sinks are closed by ONE check that
// depends on both, and a second member completing does not queue another
// (the plan id is derived from the sinks).
func TestForkedPlanClosesOnce(t *testing.T) {
	r := newRecordingRunner()
	n := New("planclose-fork", WithRunner(r), WithWorkers(3))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "left", "t1")
	stage(t, n, "right", "t1")
	r.release()
	waitStatus(t, n, "left", "complete")
	waitStatus(t, n, "right", "complete")

	check := waitForThought(t, n, func(x *thought.Thought) bool {
		return strings.HasPrefix(x.ID, verifyIDPrefix)
	}, "the synthesized verification thought")
	if len(check.DependsOn) != 2 || check.DependsOn[0] != "left" || check.DependsOn[1] != "right" {
		t.Fatalf("check depends_on = %v, want both sinks [left right]", check.DependsOn)
	}

	waitStatus(t, n, check.ID, "complete")
	time.Sleep(250 * time.Millisecond)
	if got := verifications(n); len(got) != 1 {
		t.Fatalf("forked plan closed by %d checks, want 1: %v", len(got), ids(got))
	}
}

// TestSingleThoughtIsNotAPlan: a standalone thought gets no closing check
// (there is no plan to close).
func TestSingleThoughtIsNotAPlan(t *testing.T) {
	r := newRecordingRunner()
	r.release()
	n := New("planclose-single", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "only")
	waitStatus(t, n, "only", "complete")

	time.Sleep(250 * time.Millisecond)
	if got := verifications(n); len(got) != 0 {
		t.Fatalf("a standalone thought was closed with %d check(s)", len(got))
	}
}

// TestIncrementallyStagedPlanClosesAgainstFinalSinks pins NAVI-382's real
// case: a plan staged one stage at a time — how a still-running planner
// thought creates it, since `thoughts_create` returns the id the next stage
// names.
//
// Stage 1 (t1 → left) drains on its own, so the closing check fires on the
// partial plan; stage 2 (right, a second fork on t1) arrives afterwards.
// The plan must end up closed against its final sinks [left right], and the
// superseded check for [left] — still pending, because the holds keep both
// workers off it — must be cancelled.
//
// No gate holds the plan's stages back: t1 runs and completes before left is
// enqueued, and left before right. The holds exist only to keep workers busy.
// They share the synthesized check's priority band, so a worker freed at the
// wrong moment claims a hold (enqueued earlier, stable queue order) instead
// of the check.
func TestIncrementallyStagedPlanClosesAgainstFinalSinks(t *testing.T) {
	r := newStagingRunner("t1", "hold-1", "hold-2", "hold-3")
	n := New("planclose-incremental", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done) // registered first, so it runs last
	defer r.releaseAll()               // drain the holds before shutdown

	// Stage 1, one thought at a time: t1 runs (and holds), left queues behind.
	stagePrio(t, n, "t1", 1)
	waitStatus(t, n, "t1", "running")
	stagePrio(t, n, "left", 2, "t1")

	// Occupiers for both workers around the drain.
	stagePrio(t, n, "hold-1", 3)
	stagePrio(t, n, "hold-2", thought.SelfScheduledHighBound)
	stagePrio(t, n, "hold-3", thought.SelfScheduledHighBound)

	r.release("t1")
	waitStatus(t, n, "left", "complete")

	premature := waitForThought(t, n, func(x *thought.Thought) bool {
		return isSynthesizedCheck(x) && sameIDSet(x.DependsOn, []string{"left"})
	}, "the check for the partial plan")
	if premature.Status != thought.ThoughtPending {
		t.Fatalf("premature check status = %s, want pending (no worker should have claimed it)", premature.Status)
	}

	// Stage 2: the plan forks on t1, past the drained {t1, left} plan.
	stagePrio(t, n, "right", 4, "t1")
	r.release("hold-1")
	waitStatus(t, n, "right", "complete")

	final := waitForThought(t, n, func(x *thought.Thought) bool {
		return isSynthesizedCheck(x) && sameIDSet(x.DependsOn, []string{"left", "right"})
	}, "the check for the final sink set")
	if final.ID == premature.ID {
		t.Fatalf("the final check is the premature one (%s)", final.ID)
	}
	if got := verifications(n); len(got) != 2 {
		t.Fatalf("the plan produced %d checks, want the premature one and its successor: %v", len(got), ids(got))
	}

	got, err := n.Thoughts.GetThought(premature.ID)
	if err != nil {
		t.Fatalf("premature check: %v", err)
	}
	if got.Status != thought.ThoughtCancelled {
		t.Fatalf("superseded check status = %s, want cancelled", got.Status)
	}
}

// TestExtendingADrainedPlanIssuesAFreshCheck: a plan that already drained
// and was checked still gets checked again when it grows a new sink — the
// extension is not silently left unclosed. The superseded check had already
// run, so it is kept; the new check is added alongside it.
func TestExtendingADrainedPlanIssuesAFreshCheck(t *testing.T) {
	r := newRecordingRunner()
	n := New("planclose-extend", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	r.release()
	waitStatus(t, n, "t2", "complete")

	first := waitForThought(t, n, isSynthesizedCheck, "the check for the drained plan")
	waitStatus(t, n, first.ID, "complete")

	// The plan grows a new sink after it already drained and was checked.
	stage(t, n, "t3", "t2")
	waitStatus(t, n, "t3", "complete")

	second := waitForThought(t, n, func(x *thought.Thought) bool {
		return isSynthesizedCheck(x) && x.ID != first.ID
	}, "the check for the extended plan")
	if !sameIDSet(second.DependsOn, []string{"t3"}) {
		t.Fatalf("extended check depends_on = %v, want the new sink [t3]", second.DependsOn)
	}
	waitStatus(t, n, second.ID, "complete")

	if got, err := n.Thoughts.GetThought(first.ID); err != nil || got.Status != thought.ThoughtComplete {
		t.Fatalf("superseded check = %v (err %v), want it kept complete", got, err)
	}
	if len(verifications(n)) != 2 {
		t.Fatalf("extended plan produced %d checks, want 2: %v", len(verifications(n)), ids(verifications(n)))
	}
}

func ids(ts []*thought.Thought) []string {
	out := make([]string, 0, len(ts))
	for _, x := range ts {
		out = append(out, x.ID)
	}
	return out
}
