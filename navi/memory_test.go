package navi

import (
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/memory"
	"github.com/flintwillow-software/opennavi/principal"
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
		Subject: principal.MustParse("navi:mem-navi"),
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
// writes with stm_write during thought 1, and thought 2's assembled prompt
// already contains it.
func TestMemoryWriteIsVisibleToTheNextThought(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(
		toolCallStream("stm_write", `{"content":"the code is blue","scope":"session"}`),
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

// TestBothMemoryTiersAreReachableFromANavi is NAVI-578's gate: a navi that
// declares memory gets BOTH tiers' tools registered and active, so every tier
// is reachable from a thought rather than only existing in the datastore.
func TestBothMemoryTiersAreReachableFromANavi(t *testing.T) {
	backend := inmem.New()
	n := New("tier-navi", WithProvider(llm.NewMockProvider()), WithWorkers(1), WithShortTermMemory())
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}

	active := map[string]bool{}
	for _, name := range n.registry.ActiveNames() {
		active[name] = true
	}
	for _, want := range []string{"stm_write", "ltm_write", "ltm_search", "ltm_read", "ltm_prune"} {
		if !active[want] {
			t.Errorf("%s is not in the active set: %v", want, n.registry.ActiveNames())
		}
	}
	if active["memory_write"] {
		t.Error("memory_write is still active; the tier split replaces it")
	}

	// The long-term tier is real: a write through the tool reaches the
	// datastore and is findable, while a short-term write does not.
	svc := n.longMemory
	if svc == nil {
		t.Fatal("the navi declared memory but built no long-term tier")
	}
	if _, err := svc.Write(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "the archive works",
	}); err != nil {
		t.Fatalf("long-term write: %v", err)
	}
	recs, err := svc.Search(memory.SearchQuery{Query: "archive works"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("archive search returned %d entries, want 1", len(recs))
	}
	// And it is NOT injected: the tier boundary holds end to end.
	if block := n.shortMemory.Inject(); strings.Contains(block, "archive works") {
		t.Fatalf("a long-term entry leaked into the injected short-term tier:\n%s", block)
	}
}

// TestMemorySkillShipsOnlyWhenMemoryIsDeclared: the core memory skill names the
// tier tools, so a navi with no memory surface must not be told about them.
func TestMemorySkillShipsOnlyWhenMemoryIsDeclared(t *testing.T) {
	// Without memory.
	plain := New("no-mem", WithProvider(llm.NewMockProvider()), WithWorkers(1))
	if err := plain.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := plain.assemble(); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.skillIndex["core_memory"]; ok {
		t.Fatal("core_memory shipped for a navi that declares no memory")
	}

	// With memory.
	backend := inmem.New()
	withMem := New("with-mem", WithProvider(llm.NewMockProvider()), WithWorkers(1), WithShortTermMemory())
	if err := withMem.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}
	if err := withMem.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := withMem.assemble(); err != nil {
		t.Fatal(err)
	}
	sk, ok := withMem.skillIndex["core_memory"]
	if !ok {
		t.Fatal("core_memory did not ship for a navi that declares memory")
	}
	// The skill must name the tier tools, which is the whole point of it.
	for _, want := range []string{"stm_write", "ltm_write", "ltm_search", "ltm_read", "ltm_prune"} {
		if !strings.Contains(sk.Content, want) {
			t.Errorf("core_memory does not name %s", want)
		}
	}
	// And it must state which tier is for what.
	for _, want := range []string{"always in front of you", "knowledge base you search"} {
		if !strings.Contains(sk.Content, want) {
			t.Errorf("core_memory does not explain the tiers (missing %q)", want)
		}
	}
}
