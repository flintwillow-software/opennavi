package navi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/thought"
)

// --- helpers ----------------------------------------------------------

// startServing runs the navi and waits until it reports serving.
func startServing(t *testing.T, n *Navi) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n.Serving() {
			return cancel, done
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	t.Fatal("navi never reported serving")
	return nil, nil
}

func stopServing(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// waitStatus polls until a thought reaches the wanted status.
func waitStatus(t *testing.T, n *Navi, id, want string) *thought.Thought {
	t.Helper()
	return waitStatusWithin(t, n, id, want, 5*time.Second)
}

// waitStatusWithin is waitStatus with an explicit budget — live provider
// calls take far longer than an in-process mock.
func waitStatusWithin(t *testing.T, n *Navi, id, want string, budget time.Duration) *thought.Thought {
	t.Helper()
	deadline := time.Now().Add(budget)
	last := "<unreadable>"
	for time.Now().Before(deadline) {
		if got, err := n.Thoughts.GetThought(id); err == nil {
			last = string(got.Status)
			if string(got.Status) == want {
				return got
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("thought %s never reached %q (last seen %q)", id, want, last)
	return nil
}

func mockNavi(t *testing.T, mock *llm.MockProvider, opts ...Option) *Navi {
	t.Helper()
	all := append([]Option{WithProvider(mock), WithWorkers(1)}, opts...)
	return withDatastore(t, New("mock-navi", all...))
}

// withDatastore declares the reference in-memory datastore, so the navi
// persists transcripts through the public contract.
func withDatastore(t *testing.T, n *Navi) *Navi {
	t.Helper()
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		t.Fatal(err)
	}
	return n
}

// flushTranscripts drains the ordered writer so a test can read a
// transcript that the runner wrote asynchronously (NAVI-374).
func flushTranscripts(t *testing.T, n *Navi) {
	t.Helper()
	if err := n.Datastores.Handle().FlushWrites(); err != nil {
		t.Fatalf("flush transcripts: %v", err)
	}
}

// --- the gate: a thought actually executes through a provider ---------

// streamEvents scripts one assistant turn: a content delta then stop. The
// agent runner consumes the streaming path (the old completion runner
// consumed Complete), so tests script streams.
func streamEvents(content string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: content},
		{Type: llm.StreamEventStop},
	}
}

func TestRunExecutesAThoughtThroughAMockProvider(t *testing.T) {
	mock := llm.NewMockProvider(streamEvents("pong"))
	n := mockNavi(t, mock)

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "say pong", Priority: 5,
	}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	// The provider was really called, and it received the thought.
	reqs := mock.Requests()
	if len(reqs) == 0 {
		t.Fatal("provider was never called")
	}
	if last := reqs[0][len(reqs[0])-1].Content; !strings.Contains(last, "say pong") {
		t.Fatalf("provider did not receive the prompt: %q", last)
	}

	// The exchange is recorded in the thought's transcript, with the
	// assembled context as the first entry (ADR 0013 decision 3).
	flushTranscripts(t, n)
	entries, err := n.Datastores.Handle().Thoughts().LoadTranscript("t1")
	if err != nil {
		t.Fatalf("load transcript: %v", err)
	}
	if len(entries) == 0 || entries[0].Role != datastore.RoleSystem || strings.TrimSpace(entries[0].Message) == "" {
		t.Fatalf("the assembled context must be the first transcript entry: %+v", entries)
	}
	if got, want := transcriptText(entries[1:]), []string{"say pong", "pong"}; !equalStrings(got, want) {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
}

func TestPersonalityBecomesTheSystemTurn(t *testing.T) {
	mock := llm.NewMockProvider(streamEvents("ok"))
	n := mockNavi(t, mock, WithPersonality("You are terse."))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	reqs := mock.Requests()
	if len(reqs) == 0 || len(reqs[0]) < 2 {
		t.Fatalf("want a system turn plus a user turn, got %v", reqs)
	}
	// The personality seeds the system turn; core skills (and memory, when
	// configured) are injected after it.
	if reqs[0][0].Role != llm.RoleSystem || !strings.HasPrefix(reqs[0][0].Content, "You are terse.") {
		t.Fatalf("first turn should start with the personality: %+v", reqs[0][0])
	}
}

func TestThoughtContextRidesAlongWithThePrompt(t *testing.T) {
	mock := llm.NewMockProvider(streamEvents("ok"))
	n := mockNavi(t, mock)

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "the task", Context: "extra context",
	}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	reqs := mock.Requests()
	body := reqs[0][len(reqs[0])-1].Content
	if !strings.Contains(body, "the task") || !strings.Contains(body, "extra context") {
		t.Fatalf("accumulated context is not part of the prompt: %q", body)
	}
}

func TestFailedProviderFailsTheThought(t *testing.T) {
	// A stream error fails the run; the runner surfaces it and the worker
	// marks the thought failed.
	n := mockNavi(t, llm.NewMockProvider().WithStreamError(errors.New("boom")))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "boom"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "failed")
}

// --- interruption -----------------------------------------------------

type blockingRunner struct{ started chan struct{} }

func (r *blockingRunner) Run(ctx context.Context, t *thought.Thought) error {
	close(r.started)
	<-ctx.Done()
	return ctx.Err()
}

// TestInterruptedThoughtIsParkedNotFailed pins the park-not-kill promise:
// shutting down mid-thought leaves resumable work, not a failed record.
func TestInterruptedThoughtIsParkedNotFailed(t *testing.T) {
	blocker := &blockingRunner{started: make(chan struct{})}
	n := New("interrupt-test", WithRunner(blocker), WithWorkers(1))

	cancel, done := startServing(t, n)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "long"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, blocker.started, "the runner to start")

	stopServing(t, cancel, done)

	got, err := n.Thoughts.GetThought("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "paused" {
		t.Fatalf("an interrupted thought must be parked, got status %q", got.Status)
	}
}

// --- helpers ----------------------------------------------------------

func transcriptText(entries []datastore.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Message)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
