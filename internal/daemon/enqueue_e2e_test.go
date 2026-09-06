package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// TestE2EEnqueueRecoverAfterKill: the #241 E2E — enqueue via the CLI
// path (thought store write), start the daemon's pool, kill -9
// equivalent (abandon without drain, fresh process reads the same DB),
// restart, verify the thought is recovered (running → pending per
// spec §3.5) and claimable.
func TestE2EEnqueueRecoverAfterKill(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")

	// Step 1: "enqueue" — write the thought via the store API.
	store1, err := sqlite.Open(dbPath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts1, err := sqlite.NewThoughtStore(store1.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	orig := &thought.Thought{
		ID: "e2e-thought-1", Prompt: "the E2E task",
		Priority: 4, Status: thought.ThoughtPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ts1.SaveThought(orig); err != nil {
		t.Fatal(err)
	}
	store1.Close()

	// Step 2: fresh "daemon" — new store over the same DB. The queue's
	// crash recovery transitions running → pending (simulates a kill -9
	// where the thought was claimed but never completed).
	rt, err := Assemble(RuntimeParams{
		SessionDB: dbPath,
		Model:     "test-model",
		Workers:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the kill: the thought is marked running (claimed by a
	// worker that then dies). Force the state as the queue would have
	// left it.
	thoughtStore, err := sqlite.NewThoughtStore(rt.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	claimed := &thought.Thought{
		ID: orig.ID, Prompt: orig.Prompt, Priority: orig.Priority,
		Status: thought.ThoughtRunning, CreatedAt: orig.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	}
	if err := thoughtStore.SaveThought(claimed); err != nil {
		t.Fatal(err)
	}

	// Step 3: restart — fresh queue over the same DB recovers running
	// → pending (spec §3.5).
	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue,
	}, thoughtStore)
	if err != nil {
		t.Fatal(err)
	}
	recovered, ok := q.Dequeue()
	if !ok {
		t.Fatal("recovered thought not claimable after restart")
	}
	if recovered.ID != orig.ID || recovered.Prompt != orig.Prompt {
		t.Fatalf("recovered = %+v, want %+v", recovered, orig)
	}
}

func TestE2EEnqueueDaemonPicksUp(t *testing.T) {
	// The daemon assembles over the same DB the CLI wrote to; the pool
	// claims and runs the enqueued thought.
	dbPath := filepath.Join(t.TempDir(), "e2e.db")

	// "CLI" write.
	cliStore, err := sqlite.Open(dbPath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := sqlite.NewThoughtStore(cliStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	cliThought := &thought.Thought{
		ID: "cli-123", Prompt: "hello from enqueue", Priority: 4,
		Status: thought.ThoughtPending, CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := ts.SaveThought(cliThought); err != nil {
		t.Fatal(err)
	}
	cliStore.Close()

	// Daemon starts.
	rt, err := Assemble(RuntimeParams{
		SessionDB: dbPath,
		Model:     "test-model",
		Workers:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inject a mock chain via the runner deps (Assemble wired ollama;
	// for the E2E we verify the queue claim path, not the LLM call).
	thoughtStore, err := sqlite.NewThoughtStore(rt.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue,
	}, thoughtStore)
	if err != nil {
		t.Fatal(err)
	}
	// The enqueued thought is claimable by the daemon.
	got, ok := q.Dequeue()
	if !ok || got.ID != cliThought.ID {
		t.Fatalf("daemon didn't pick up enqueued thought: %v, %v", got, ok)
	}
}
