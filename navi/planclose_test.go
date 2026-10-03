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

// recordingRunner records the order thoughts ran in and can fail named
// stages, so a plan's completion can be driven deterministically.
type recordingRunner struct {
	mu    sync.Mutex
	order []string
	fail  map[string]bool
}

func (r *recordingRunner) Run(_ context.Context, t *thought.Thought) error {
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

// stage enqueues a pending thought, optionally depending on earlier stages.
func stage(t *testing.T, n *Navi, id string, deps ...string) {
	t.Helper()
	now := time.Now().UTC()
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: id, Prompt: "stage " + id, Priority: 4, Status: thought.ThoughtPending,
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
	r := &recordingRunner{}
	n := New("planclose", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	stage(t, n, "t3", "t2")
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
	r := &recordingRunner{fail: map[string]bool{"t2": true}}
	n := New("planclose-fail", WithRunner(r), WithWorkers(2))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "t2", "t1")
	stage(t, n, "t3", "t2")
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
	r := &recordingRunner{}
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
	r := &recordingRunner{}
	n := New("planclose-fork", WithRunner(r), WithWorkers(3))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	stage(t, n, "t1")
	stage(t, n, "left", "t1")
	stage(t, n, "right", "t1")
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
	r := &recordingRunner{}
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

func ids(ts []*thought.Thought) []string {
	out := make([]string, 0, len(ts))
	for _, x := range ts {
		out = append(out, x.ID)
	}
	return out
}
