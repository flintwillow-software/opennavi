package navi

import (
	"context"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// checkpointRunner snapshots a checkpoint and then blocks until cancelled,
// standing in for an agent mid-run.
type checkpointRunner struct{ started chan struct{} }

func (r *checkpointRunner) Run(ctx context.Context, t *thought.Thought) error {
	close(r.started)
	t.Checkpoint = []byte("navicp1:[]")
	<-ctx.Done()
	return ctx.Err()
}

// TestParkedThoughtKeepsItsCheckpoint pins the NAVI-374 fix: Dequeue hands
// the runner a clone, the runner snapshots progress into it, and the worker
// must park with that checkpoint so resume continues rather than restarts.
func TestParkedThoughtKeepsItsCheckpoint(t *testing.T) {
	r := &checkpointRunner{started: make(chan struct{})}
	n := New("checkpoint-test", WithRunner(r), WithWorkers(1))

	cancel, done := startServing(t, n)
	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "long"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.started, "the runner to start")
	stopServing(t, cancel, done)

	got, err := n.Thoughts.GetThought("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != thought.ThoughtPaused {
		t.Fatalf("status = %q, want paused", got.Status)
	}
	if string(got.Checkpoint) != "navicp1:[]" {
		t.Fatalf("checkpoint dropped on park: %q", got.Checkpoint)
	}
}

// stalledBackend is an in-memory backend whose transcript write blocks until
// the test releases it, so a slow backend can be shown not to block a run.
type stalledBackend struct {
	*inmem.Backend
	block chan struct{}
	saved chan struct{}
}

func (b *stalledBackend) Thoughts() datastore.Thoughts {
	return stalledThoughts{Thoughts: b.Backend.Thoughts(), b: b}
}

type stalledThoughts struct {
	datastore.Thoughts
	b *stalledBackend
}

func (s stalledThoughts) SaveTranscript(id string, entries []datastore.Entry) error {
	<-s.b.block
	select {
	case <-s.b.saved:
	default:
		close(s.b.saved)
	}
	return s.Thoughts.SaveTranscript(id, entries)
}

func TestStalledBackendDoesNotBlockARun(t *testing.T) {
	sb := &stalledBackend{
		Backend: inmem.New(),
		block:   make(chan struct{}),
		saved:   make(chan struct{}),
	}
	mock := llm.NewMockProvider(streamEvents("pong"))
	n := New("stalled-test", WithProvider(mock), WithWorkers(1))
	if err := n.Datastores.Add(datastore.Candidate{
		Name:    "stalled",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return sb, nil },
	}); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	// The run reaches complete even though the backend's transcript write is
	// stuck: the write is staged on the ordered writer, off the run path.
	got := waitStatus(t, n, "t1", "complete")
	if got.Result != "pong" {
		t.Fatalf("result = %q, want pong", got.Result)
	}

	close(sb.block)
	select {
	case <-sb.saved:
	case <-time.After(2 * time.Second):
		t.Fatal("the staged transcript write never reached the backend")
	}
}

// TestThoughtsRecoverThroughTheDatastore drives the queue's store adapter:
// a completed thought persists through the datastore, and a second navi over
// the same backend recovers it at assembly.
func TestThoughtsRecoverThroughTheDatastore(t *testing.T) {
	shared := inmem.New()
	cand := datastore.Candidate{
		Name:    "shared",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return shared, nil },
	}

	n1 := New("recover-1", WithProvider(llm.NewMockProvider(streamEvents("ok"))), WithWorkers(1))
	if err := n1.Datastores.Add(cand); err != nil {
		t.Fatal(err)
	}
	cancel, done := startServing(t, n1)
	if err := n1.Thoughts.Enqueue(&thought.Thought{ID: "persisted", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n1, "persisted", "complete")
	stopServing(t, cancel, done) // shutdown drains the writer

	n2 := New("recover-2", WithProvider(llm.NewMockProvider(streamEvents("ok"))), WithWorkers(1))
	if err := n2.Datastores.Add(cand); err != nil {
		t.Fatal(err)
	}
	cancel2, done2 := startServing(t, n2)
	defer stopServing(t, cancel2, done2)

	got, err := n2.Thoughts.GetThought("persisted")
	if err != nil {
		t.Fatalf("the second navi did not recover the persisted thought: %v", err)
	}
	if got.Prompt != "hi" {
		t.Fatalf("recovered thought = %+v", got)
	}
}
