package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
)

// TestRuntimeProcessesEnqueuedThought is the regression test for #247:
// an assembled runtime must actually process enqueued thoughts. The
// pre-fix head failed this test because Pool.Start() was never called
// — the daemon held the queue open and waited forever.
func TestRuntimeProcessesEnqueuedThought(t *testing.T) {
	inner := llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: "the answer"},
		{Type: llm.StreamEventStop},
	})

	rt, err := Assemble(RuntimeParams{
		SessionDB:        filepath.Join(t.TempDir(), "rt.db"),
		Model:            "test-model",
		Workers:          2,
		OverrideProvider: inner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	enqueued := &thought.Thought{
		ID: "rt-thought-1", Prompt: "the task",
		Priority: 4, Status: thought.ThoughtPending,
	}
	if err := rt.Queue.Enqueue(enqueued); err != nil {
		t.Fatal(err)
	}

	// Bound the run: mock completes instantly, 2s is generous.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(2 * time.Second); cancel() }()
	_ = rt.Run(ctx)

	got, gerr := rt.Queue.GetThought(enqueued.ID)
	if gerr != nil {
		t.Fatalf("GetThought: %v", gerr)
	}
	if got.Status == thought.ThoughtPending {
		t.Fatal("thought still pending after Run — pool never claimed it (#247 bug)")
	}
}

// TestRuntimeE2EKill9Recovery: crash recovery from the daemon path.
func TestRuntimeE2EKill9Recovery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kill9.db")

	// Phase 1: assemble, claim the thought (running in DB), then
	// hard-kill (close without drain — simulates kill -9).
	rt1, err := Assemble(RuntimeParams{
		SessionDB: dbPath,
		Model:     "test-model",
		Workers:   1,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallDelta{
				ID: "call_0", Name: "f", Arguments: "{}",
			}},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The pool was never started (no Run call), but the thought is
	// in the DB. Simulate kill -9 by marking it running.
	thoughtStore, err := sqlite.NewThoughtStore(rt1.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	claimed := &thought.Thought{
		ID: "kill9-thought", Prompt: "long task",
		Priority: 4, Status: thought.ThoughtRunning,
		SlotID: "slot-1", WorkerID: "worker-dead",
	}
	if err := thoughtStore.SaveThought(claimed); err != nil {
		t.Fatal(err)
	}
	rt1.Store.Close()

	// Phase 2: restart — fresh assemble over the same DB. The queue's
	// crash recovery transitions running → pending (spec §3.5), and
	// the pool (started by Run) processes it.
	rt2, err := Assemble(RuntimeParams{
		SessionDB: dbPath,
		Model:     "test-model",
		Workers:   1,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "recovered"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(2 * time.Second); cancel() }()
	_ = rt2.Run(ctx)

	// The thought must have been recovered and processed.
	got, gerr := rt2.Queue.GetThought("kill9-thought")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status == thought.ThoughtPending {
		t.Fatal("thought still pending after recovery run")
	}
}
