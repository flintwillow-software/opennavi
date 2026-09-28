package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// binaryParams assembles a foreground runtime over a temp DB.
func binaryParams(t *testing.T, events []llm.StreamEvent, workers int) RuntimeParams {
	t.Helper()
	return RuntimeParams{
		SessionDB:        filepath.Join(t.TempDir(), "binary.db"),
		Model:            "test-model",
		Workers:          workers,
		Foreground:       true,
		OverrideProvider: llm.NewMockProvider(events),
	}
}

func binaryEvents(content string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: content},
		{Type: llm.StreamEventStop},
	}
}

func waitDrain(t *testing.T, q *thought.Queue, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		busy := false
		for _, th := range q.GetThoughts() {
			switch th.Status {
			case thought.ThoughtPending, thought.ThoughtRunning, thought.ThoughtPaused:
				busy = true
			}
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue never drained: %+v", q.GetThoughts())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ── binary-run scenarios ─────────────────────────────────

// TestBinaryRunForegroundSlotReserved: Assemble with Foreground marks
// the queue's foreground reservation; the interactive thought claims
// it and daemon-style backlog cannot.
func TestBinaryRunForegroundSlotConfigured(t *testing.T) {
	rt, err := Assemble(binaryParams(t, binaryEvents("hi"), 1))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if !rt.Config.Foreground {
		t.Fatal("Foreground flag lost through Assemble")
	}

	// Queue config: exactly one foreground slot carved from the pool.
	fgSlots := 0
	for _, s := range rt.Queue.Slots() {
		if s.Foreground {
			fgSlots++
		}
	}
	if fgSlots != 1 {
		t.Fatalf("foreground slots = %d, want 1", fgSlots)
	}

	// A non-foreground thought queues; the foreground thought claims
	// the reserved slot.
	_ = rt.Queue.Enqueue(&thought.Thought{ID: "d1", Prompt: "daemon", Priority: 4, Status: thought.ThoughtPending})
	if _, ok := rt.Queue.Dequeue(); !ok {
		t.Fatal("claim failed")
	}
	_ = rt.Queue.Enqueue(&thought.Thought{ID: "d2", Prompt: "x", Priority: 4, Status: thought.ThoughtPending})
	if _, ok := rt.Queue.Dequeue(); ok {
		t.Fatal("normal thought claimed the foreground-reserved slot")
	}
	if err := rt.Queue.Enqueue(&thought.Thought{ID: "fg", Prompt: "interactive", Priority: 4, Status: thought.ThoughtPending, Foreground: true}); err != nil {
		t.Fatal(err)
	}
	claimed, ok := rt.Queue.Dequeue()
	if !ok || claimed.ID != "fg" {
		t.Fatalf("foreground thought must claim the reserved slot (got %+v)", claimed)
	}
}

// TestBinaryRunDurableThoughtAndProcessing: interactive input becomes
// a durable thought (persisted), scheduled, processed with the
// governed tool registry — the full pipeline, headlessly.
func TestBinaryRunDurableThoughtScheduledAndProcessed(t *testing.T) {
	rt, err := Assemble(RuntimeParams{
		SessionDB:  filepath.Join(t.TempDir(), "binary-e2e.db"),
		Model:      "test-model",
		Workers:    1,
		Foreground: true,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "binary hello"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	// Interactive input → durable thought (stdin line in the real
	// entry; tests enqueue directly over the same path).
	now := time.Now().UTC()
	th := &thought.Thought{
		ID: "fg-1", Prompt: "say hi", Priority: 4,
		Status: thought.ThoughtPending, Foreground: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := rt.Queue.Enqueue(th); err != nil {
		t.Fatal(err)
	}

	sessionDone := make(chan struct{})
	close(sessionDone) // session ends immediately; the run drains
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := rt.RunForeground(ctx, sessionDone); err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	got, gerr := rt.Queue.GetThought("fg-1")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != thought.ThoughtComplete {
		t.Fatalf("foreground thought status = %s, want complete", got.Status)
	}
}

// TestBinaryRunSteeringBridgeDelivers: RouteMessage on the running
// foreground thought reaches the in-flight agent's steering queue via
// the registered bridge (running path +).
func TestBinaryRunSteeringBridgeDelivers(t *testing.T) {
	rt, err := Assemble(RuntimeParams{
		SessionDB:  filepath.Join(t.TempDir(), "steer.db"),
		Model:      "test-model",
		Workers:    1,
		Foreground: true,
		// Blocking provider: holds the agent mid-run until steering
		// lands, so RouteMessage's turn-boundary injection is exercised
		// while the thought is genuinely running.
		OverrideProvider: blockingLLMProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	th := &thought.Thought{ID: "fg-1", Prompt: "long task", Priority: 4, Status: thought.ThoughtPending, Foreground: true}
	if err := rt.Queue.Enqueue(th); err != nil {
		t.Fatal(err)
	}

	sessionDone := make(chan struct{})
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { runDone <- rt.RunForeground(ctx, sessionDone) }()

	// Wait for the thought to start, then steer it through the queue.
	// Note: status==Running (the pool's claim) precedes the runner's
	// agent registration — under -race/lag the window is real, so the
	// wait below polls the tracker itself (the thing the bridge needs)
	// rather than relying on the status alone.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := rt.inflight.lookup("fg-1"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never registered with the steering bridge")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The bridge must resolve the in-flight agent and inject the
	// message (RouteMessage → SetSteeringRouter closure → agent.Steer).
	// Re-entry via GetThought inside RouteMessage is safe (outside
	// q.mu) — proven by the bridge's snapshot use in tests.
	path, err := rt.Queue.RouteMessageWithPath("fg-1", "steer: check tests")
	if err != nil {
		t.Fatalf("RouteMessage(running): %v", err)
	}
	if path != thought.PathSteering {
		t.Fatalf("path = %q, want steering", path)
	}

	// The steering landed in the agent's queue — verify via the tracker
	// (the same agent the bridge resolved). Destructive snapshot.
	steered := rt.steeringSnapshot("fg-1")
	if len(steered) != 1 || steered[0] != "steer: check tests" {
		t.Fatalf("steering snapshot = %v, want the routed message", steered)
	}

	// Release the run and drain: the agent processes, thought completes.
	cancel()
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunForeground: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunForeground did not return")
	}
}

// blockingLLMProvider holds the agent mid-run until its context is
// cancelled — the steering bridge and park-on-interrupt tests exercise
// the bridge/injection while the thought is genuinely running.
type blockingLLMProvider struct{}

func (blockingLLMProvider) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool, cfg llm.Config) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	go func() {
		<-ctx.Done() // hold the run open until interrupted
		close(ch)
	}()
	return ch, nil
}

func (blockingLLMProvider) Complete(ctx context.Context, messages []llm.Message, tools []llm.Tool, cfg llm.Config) (*llm.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingLLMProvider) Ping(ctx context.Context) error { return nil }

func (blockingLLMProvider) Name() string { return "blocking" }

func (blockingLLMProvider) TokenCount(messages []llm.Message) int { return 0 }

func (blockingLLMProvider) MaxContextWindow(cfg llm.Config) int { return 128000 }

// TestBinaryRunParkOnInterrupt: interrupt semantics — cancelling the
// run parks the in-flight foreground thought with its checkpoint
// (never kill-and-lose).
func TestBinaryRunParkOnInterrupt(t *testing.T) {
	rt, err := Assemble(RuntimeParams{
		SessionDB:  filepath.Join(t.TempDir(), "park.db"),
		Model:      "test-model",
		Workers:    1,
		Foreground: true,
		// Blocking provider: the run stays genuinely in-flight until
		// the interrupt — park-on-interrupt needs an in-flight run.
		OverrideProvider: blockingLLMProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	if err := rt.Queue.Enqueue(&thought.Thought{ID: "fg-park", Prompt: "long task", Priority: 4, Status: thought.ThoughtPending, Foreground: true}); err != nil {
		t.Fatal(err)
	}

	sessionDone := make(chan struct{})
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- rt.RunForeground(ctx, sessionDone) }()

	// Wait for the claim, then interrupt.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, gerr := rt.Queue.GetThought("fg-park")
		if gerr == nil && got.Status == thought.ThoughtRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thought never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunForeground: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunForeground did not return after interrupt")
	}

	// Parked with checkpoint — never kill-and-lose.
	got, err := rt.Queue.GetThought("fg-park")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != thought.ThoughtPaused {
		t.Fatalf("status after interrupt = %s, want paused (park-with-checkpoint)", got.Status)
	}
	if len(got.Checkpoint) == 0 {
		t.Fatal("parked thought lost its checkpoint")
	}
}

// TestBinaryRunExitWhenDrained: session end + queue drain → the run
// returns (process would exit). Work enqueued before the session ends
// is finished, not dropped.
func TestBinaryRunExitWhenDrained(t *testing.T) {
	rt, err := Assemble(RuntimeParams{
		SessionDB:        filepath.Join(t.TempDir(), "drain.db"),
		Model:            "test-model",
		Workers:          2,
		Foreground:       true,
		OverrideProvider: llm.NewMockProvider(binaryEvents("done")),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	// Queue work, THEN end the session — RunForeground must finish
	// the queued work before exiting (a binary run completes its
	// work; nothing is lost).
	for i := 0; i < 3; i++ {
		now := time.Now().UTC()
		th := &thought.Thought{
			ID: fmt.Sprintf("w%d", i), Prompt: "work", Priority: 4,
			Status: thought.ThoughtPending, Foreground: true,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := rt.Queue.Enqueue(th); err != nil {
			t.Fatal(err)
		}
	}
	sessionDone := make(chan struct{})
	close(sessionDone)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rt.RunForeground(ctx, sessionDone); err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	// All work terminal.
	for _, th := range rt.Queue.GetThoughts() {
		switch th.Status {
		case thought.ThoughtPending, thought.ThoughtRunning, thought.ThoughtPaused:
			t.Fatalf("thought %s still %s after drain-exit", th.ID, th.Status)
		}
	}
}

// TestBinaryRunForegroundCompetesByPriority: the foreground thought
// does not get exclusive high priority — daemon-style work with
// higher priority claims slots first ("same priority and
// preemption rules").
func TestBinaryRunForegroundCompetesByPriority(t *testing.T) {
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 2, ForegroundSlots: 1}, thought.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	// Foreground enqueued FIRST but low priority; a higher-priority
	// daemon thought claims the shared slot first — foreground does
	// not jump the queue (same priority/preemption rules).
	_ = q.Enqueue(&thought.Thought{ID: "fg", Prompt: "interactive", Priority: 5, Status: thought.ThoughtPending, Foreground: true})
	_ = q.Enqueue(&thought.Thought{ID: "urgent-daemon", Prompt: "x", Priority: 1, Status: thought.ThoughtPending})
	first, ok := q.Dequeue()
	if !ok || first.ID != "urgent-daemon" {
		t.Fatalf("higher-priority daemon work must claim the shared slot first (claimed %s)", first.ID)
	}
	// The foreground thought then takes its reserved slot (it does
	// not wait behind the urgent daemon work).
	second, ok := q.Dequeue()
	if !ok || second.ID != "fg" || second.SlotID == "" {
		t.Fatalf("foreground fallback claim failed (claimed=%+v)", second)
	}
}
