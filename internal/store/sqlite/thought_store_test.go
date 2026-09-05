package sqlite

import (
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
)

func openThoughtStore(t *testing.T) (*Store, *ThoughtStore) {
	t.Helper()
	st, err := Open(t.TempDir()+"/thoughts.db", Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts, err := NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return st, ts
}

func TestThoughtStoreRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{
		ID:        "t-1",
		Prompt:    "deploy the thing",
		Context:   "some context",
		Metadata:  map[string]string{"source": "cron"},
		Priority:  2,
		Status:    thought.ThoughtPending,
		LogPath:   "/tmp/x",
		CreatedAt: testNow(),
		UpdatedAt: testNow(),
	}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Prompt != want.Prompt || got.Context != want.Context ||
		got.Priority != want.Priority || got.Status != want.Status || got.LogPath != want.LogPath {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Metadata["source"] != "cron" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestThoughtStoreCheckpointRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{ID: "t-2", Prompt: "p", Status: thought.ThoughtPaused, Checkpoint: []byte(`{"msgs":3}`)}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-2")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Checkpoint) != `{"msgs":3}` {
		t.Fatalf("checkpoint = %s", got.Checkpoint)
	}
}

func TestThoughtStoreLoadMissing(t *testing.T) {
	_, ts := openThoughtStore(t)
	if _, err := ts.LoadThought("nope"); err != thought.ErrThoughtNotFound {
		t.Fatalf("error = %v", err)
	}
}

func TestThoughtStoreDelete(t *testing.T) {
	_, ts := openThoughtStore(t)
	tt := &thought.Thought{ID: "t-3", Prompt: "p"}
	if err := ts.SaveThought(tt); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteThought("t-3"); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteThought("t-3"); err != thought.ErrThoughtNotFound {
		t.Fatalf("double delete error = %v", err)
	}
}

func TestThoughtStoreQueueCrashRecovery(t *testing.T) {
	// End-to-end: persist running + pending thoughts through the store,
	// reopen the queue, verify running→pending recovery (spec §3.5).
	path := t.TempDir() + "/q.db"
	st, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	running := &thought.Thought{ID: "was-running", Prompt: "r", Priority: 4, Status: thought.ThoughtRunning, SlotID: "slot-1", WorkerID: "worker-1"}
	pending := &thought.Thought{ID: "was-pending", Prompt: "p", Priority: 2, Status: thought.ThoughtPending}
	if err := ts.SaveThought(running); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveThought(pending); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Reopen: fresh queue over the same DB recovers.
	st2, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ts2, err := NewThoughtStore(st2.DB())
	if err != nil {
		t.Fatal(err)
	}
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, ts2)
	if err != nil {
		t.Fatal(err)
	}

	recovered, _ := q.GetThought("was-running")
	if recovered == nil {
		t.Fatal("running thought lost in recovery")
	}
	if recovered.Status != thought.ThoughtPending {
		t.Fatalf("recovered status = %q, want pending", recovered.Status)
	}
	if recovered.SlotID != "" || recovered.WorkerID != "" {
		t.Fatalf("recovered slot/worker not cleared: %+v", recovered)
	}
	// Priority order: was-pending (2) before was-running (4).
	first, ok := q.Dequeue()
	if !ok || first.ID != "was-pending" {
		t.Fatalf("first claim = %v, want was-pending", first)
	}
	second, ok := q.Dequeue()
	if !ok || second.ID != "was-running" {
		t.Fatalf("second claim = %v, want was-running", second)
	}
}

func testNow() time.Time { return time.Now().UTC() }
