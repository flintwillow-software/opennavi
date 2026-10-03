package navi

import (
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

func memoryCandidate(backend datastore.Backend) datastore.Candidate {
	return datastore.Candidate{
		Name:    "memory-test",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return backend, nil },
	}
}

// TestShortTermMemoryIsInjectedIntoEveryThought is the issue's gate (a):
// injection happens for every thought, with no tool call, because it rides
// the system prompt each run builds.
func TestShortTermMemoryIsInjectedIntoEveryThought(t *testing.T) {
	backend := inmem.New()
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope:   datastore.ScopeUser,
		Subject: "mem-navi",
		Content: "prefers plain language",
	}); err != nil {
		t.Fatal(err)
	}

	mock := llm.NewMockProvider(streamEvents("one"), streamEvents("two"))
	n := New("mem-navi", WithProvider(mock), WithWorkers(1), WithShortTermMemory())
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	for _, id := range []string{"t1", "t2"} {
		if err := n.Thoughts.Enqueue(&thought.Thought{ID: id, Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, n, id, "complete")
	}

	reqs := mock.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one per thought)", len(reqs))
	}
	for i, req := range reqs {
		if len(req) == 0 || req[0].Role != llm.RoleSystem {
			t.Fatalf("request %d has no system turn: %+v", i, req)
		}
		if !strings.Contains(req[0].Content, "prefers plain language") {
			t.Fatalf("thought %d prompt is missing short-term memory: %q", i, req[0].Content)
		}
	}
}

// TestMemoryWriteIsVisibleToTheNextThought is the issue's gate (b): the agent
// writes with memory_write during thought 1, and thought 2's assembled prompt
// already contains it.
func TestMemoryWriteIsVisibleToTheNextThought(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(
		toolCallStream("memory_write", `{"content":"the code is blue","scope":"session"}`),
		streamEvents("noted"),
		streamEvents("done"),
	)
	n := New("mem-write-navi", WithProvider(mock), WithWorkers(1), WithShortTermMemory())
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "remember something"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t2", Prompt: "what do you know"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t2", "complete")

	reqs := mock.Requests()
	if len(reqs) < 3 {
		t.Fatalf("requests = %d, want at least 3", len(reqs))
	}
	second := reqs[2]
	if len(second) == 0 || second[0].Role != llm.RoleSystem {
		t.Fatalf("thought 2 has no system turn: %+v", second)
	}
	if !strings.Contains(second[0].Content, "the code is blue") {
		t.Fatalf("thought 2 prompt is missing the note written in thought 1: %q", second[0].Content)
	}
}

// TestThoughtThatWritesNothingLeavesMemoryUnchanged is the issue's gate (c).
func TestThoughtThatWritesNothingLeavesMemoryUnchanged(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(streamEvents("nothing to add"))
	n := New("mem-quiet-navi", WithProvider(mock), WithWorkers(1), WithShortTermMemory())
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "just answer"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")
	flushTranscripts(t, n)

	records, err := backend.Memory().Recall(datastore.RecallQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a thought that wrote nothing changed memory: %+v", records)
	}
}

// TestShortTermMemoryWithoutDatastoreFailsAtRun pins the assembly check the
// umbrella acceptance asked for: memory requires a configured datastore.
func TestShortTermMemoryWithoutDatastoreFailsAtRun(t *testing.T) {
	n := New("no-ds-memory", WithProvider(llm.NewMockProvider(streamEvents("ok"))), WithShortTermMemory())

	err := n.Run(t.Context())
	if err == nil {
		t.Fatal("Run must fail when short-term memory is declared without a datastore")
	}
	if !strings.Contains(err.Error(), "short-term memory requires a datastore") {
		t.Fatalf("error should name the reason: %v", err)
	}
}
