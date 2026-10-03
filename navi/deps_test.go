package navi

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool/thoughts"
)

// ── thought dependencies at the navi level (NAVI-378) ───────────────────

// stagedRunner records the order thoughts start in and holds the chain
// head until the test releases it, so a dependent that ignored its
// dependency would start visibly early.
type stagedRunner struct {
	mu      sync.Mutex
	order   []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newStagedRunner() *stagedRunner {
	return &stagedRunner{entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *stagedRunner) Run(ctx context.Context, t *thought.Thought) error {
	if t.ID == "t1" {
		r.once.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	r.order = append(r.order, t.ID)
	r.mu.Unlock()
	return nil
}

func (r *stagedRunner) started() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// TestStagedPlanRunsInOrderWithTwoWorkers is the acceptance gate: a plan
// whose later stages depend on earlier ones cannot start early, even with
// two workers free to claim them.
func TestStagedPlanRunsInOrderWithTwoWorkers(t *testing.T) {
	// The closing check is off here: this test pins ordering alone (the
	// verification the navi appends to a drained plan has its own tests).
	r := newStagedRunner()
	n := New("staged-plan", WithRunner(r), WithWorkers(2), WithPlanCloseCheck(false))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	enqueue := func(tk *thought.Thought) {
		t.Helper()
		if err := n.Thoughts.Enqueue(tk); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	enqueue(&thought.Thought{ID: "t1", Prompt: "stage one", Priority: 4, Status: thought.ThoughtPending, CreatedAt: now, UpdatedAt: now})
	enqueue(&thought.Thought{ID: "t2", Prompt: "stage two", Priority: 4, Status: thought.ThoughtPending, DependsOn: []string{"t1"}, CreatedAt: now, UpdatedAt: now})
	enqueue(&thought.Thought{ID: "t3", Prompt: "stage three", Priority: 4, Status: thought.ThoughtPending, DependsOn: []string{"t2"}, CreatedAt: now, UpdatedAt: now})

	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the chain head never started")
	}
	// The head is parked inside the runner with a second worker idle: a
	// stage that ignored its dependency would start now. Give the pool a
	// moment to prove it does not.
	time.Sleep(50 * time.Millisecond)
	if got := r.started(); len(got) != 0 {
		t.Fatalf("a stage started before its dependency completed: %v", got)
	}

	close(r.release)
	waitStatus(t, n, "t1", "complete")
	waitStatus(t, n, "t2", "complete")
	waitStatus(t, n, "t3", "complete")

	if got, want := r.started(), []string{"t1", "t2", "t3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages ran %v, want %v", got, want)
	}
}

// TestAgentExpressesDependencyThroughThoughtsCreate is the model-facing
// gate: the agent queues its follow-up through thoughts_create with the
// id it is continuing, the successor carries the edge, and it does not run
// until its predecessor completed.
func TestAgentExpressesDependencyThroughThoughtsCreate(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(
		toolCallStream("thoughts_create", `{"prompt":"continue the plan","depends_on":["t1"]}`),
		streamEvents("queued the next stage"),
		streamEvents("continued"),
	)
	n := New("dep-plan", WithProvider(mock), WithWorkers(2), WithPlanCloseCheck(false))
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}
	tools, err := thoughts.Tools(n.Thoughts)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if err := n.Tools.Add(tl); err != nil {
			t.Fatal(err)
		}
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "start the plan"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	var successor *thought.Thought
	for _, x := range n.Thoughts.GetThoughts() {
		if x.ID != "t1" {
			successor = x
		}
	}
	if successor == nil {
		t.Fatal("the agent did not queue a follow-up thought")
	}
	if want := []string{"t1"}; !reflect.DeepEqual(successor.DependsOn, want) {
		t.Fatalf("successor depends_on = %v, want %v", successor.DependsOn, want)
	}

	waitStatus(t, n, successor.ID, "complete")
}
