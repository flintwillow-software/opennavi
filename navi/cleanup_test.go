package navi

import (
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// wordCounter counts one token per whitespace-separated word, so the test can
// reason about budgets exactly.
type wordCounter struct{}

func (wordCounter) CountText(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return len(strings.Fields(text))
}

// waitForCleanup polls for a cleanup thought matching the predicate.
func waitForCleanup(t *testing.T, n *Navi, want func(*thought.Thought) bool, what string) *thought.Thought {
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
	t.Fatalf("no cleanup thought found: %s", what)
	return nil
}

// TestOverBudgetSchedulesCleanup is ADR 0039 decision 4: a refused write
// schedules a p1 cleanup thought rather than blocking the navi.
func TestOverBudgetSchedulesCleanup(t *testing.T) {
	backend := inmem.New()
	// Seed an over-budget persisted set so the hydrate trigger fires.
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope:   datastore.ScopeSession,
		Subject: principal.MustParse("navi:cleanup-navi"),
		Content: "one two three four five",
	}); err != nil {
		t.Fatal(err)
	}

	n := New("cleanup-navi",
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
		WithShortTermMemory(),
		WithShortTermMemoryBudget(datastore.ScopeSession, 2),
		WithTokenizer(wordCounter{}),
	)
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	ct := waitForCleanup(t, n, func(x *thought.Thought) bool {
		return x.IsMemoryCleanup()
	}, "over-budget cleanup from hydrate")

	// p1, self-scheduled, recorded with scope and trigger.
	if ct.Priority != thought.SelfScheduledDefault {
		t.Errorf("cleanup priority = %d, want %d (p1)", ct.Priority, thought.SelfScheduledDefault)
	}
	if ct.Metadata[thought.MetadataOrigin] != thought.OriginSelf {
		t.Errorf("cleanup origin = %q, want self", ct.Metadata[thought.MetadataOrigin])
	}
	if ct.Metadata[thought.MetadataCleanupScope] != string(datastore.ScopeSession) {
		t.Errorf("cleanup scope = %q, want session", ct.Metadata[thought.MetadataCleanupScope])
	}
	if ct.Metadata[thought.MetadataCleanupTrigger] != thought.CleanupOverBudget {
		t.Errorf("cleanup trigger = %q, want %q", ct.Metadata[thought.MetadataCleanupTrigger], thought.CleanupOverBudget)
	}
	if !strings.Contains(ct.Prompt, "over budget") {
		t.Errorf("cleanup brief must state the problem: %q", ct.Prompt)
	}
}

// TestPlanClosedSchedulesCleanup is ADR 0039 decision 7: a drained plan queues
// a cleanup alongside the closing check, depending on the plan's sinks.
func TestPlanClosedSchedulesCleanup(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(
		streamEvents("stage one done"),
		streamEvents("stage two done"),
		streamEvents("check done"),
		streamEvents("cleanup done"),
	)
	n := New("plan-cleanup-navi",
		WithProvider(mock),
		WithWorkers(1),
		WithShortTermMemory(),
	)
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	// A two-stage plan: t2 depends on t1, so the pair is a plan and its
	// completion drains it.
	now := time.Now().UTC()
	for _, tt := range []*thought.Thought{
		{ID: "t1", Prompt: "stage one", Status: thought.ThoughtPending, CreatedAt: now, UpdatedAt: now},
		{ID: "t2", Prompt: "stage two", DependsOn: []string{"t1"}, Status: thought.ThoughtPending, CreatedAt: now, UpdatedAt: now},
	} {
		if err := n.Thoughts.Enqueue(tt); err != nil {
			t.Fatal(err)
		}
	}
	waitStatus(t, n, "t2", "complete")

	ct := waitForCleanup(t, n, func(x *thought.Thought) bool {
		return x.IsMemoryCleanup() && x.Metadata[thought.MetadataCleanupTrigger] == thought.CleanupPlanClosed
	}, "plan-closed cleanup")

	if ct.Priority != thought.SelfScheduledDefault {
		t.Errorf("cleanup priority = %d, want %d (p1)", ct.Priority, thought.SelfScheduledDefault)
	}
	// It depends on the plan's sinks, so it runs after the plan drained.
	if len(ct.DependsOn) == 0 {
		t.Errorf("plan-closed cleanup must depend on the plan's sinks, got none")
	}
	if !strings.Contains(ct.Prompt, "drained") {
		t.Errorf("plan-closed brief must state the residue focus: %q", ct.Prompt)
	}
}

// TestCleanupDoesNotRecurse is ADR 0039 decision 5: a cleanup that completes
// without fixing the tier is not followed by another cleanup.
func TestCleanupDoesNotRecurse(t *testing.T) {
	backend := inmem.New()
	// A persistent over-budget set: the cleanup thought completes but the tier
	// stays over budget, so a recursive rule would loop forever.
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope:   datastore.ScopeSession,
		Subject: principal.MustParse("navi:norecurse-navi"),
		Content: "one two three four five",
	}); err != nil {
		t.Fatal(err)
	}
	mock := llm.NewMockProvider(
		streamEvents("cleanup ran, did nothing"),
		streamEvents("other"),
		streamEvents("other2"),
	)
	n := New("norecurse-navi",
		WithProvider(mock),
		WithWorkers(1),
		WithShortTermMemory(),
		WithShortTermMemoryBudget(datastore.ScopeSession, 2),
		WithTokenizer(wordCounter{}),
	)
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	// Wait for the first cleanup, then let it run and settle.
	first := waitForCleanup(t, n, func(x *thought.Thought) bool {
		return x.IsMemoryCleanup()
	}, "first over-budget cleanup")
	waitStatus(t, n, first.ID, "complete")

	// Give the sweeper time to (not) recurse.
	time.Sleep(200 * time.Millisecond)
	cleanups := 0
	for _, x := range n.Thoughts.GetThoughts() {
		if x.IsMemoryCleanup() && x.Status != thought.ThoughtComplete {
			cleanups++
		}
	}
	if cleanups != 0 {
		t.Fatalf("cleanup recursed: %d cleanup thoughts still queued", cleanups)
	}
}
