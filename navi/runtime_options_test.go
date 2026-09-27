package navi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
)

// TestWithThoughtStoreAndQueueConfig pins the durable-store and queue
// configuration seams a reference navi needs (M5): the queue persists
// through the supplied store with the supplied scheduling config.
func TestWithThoughtStoreAndQueueConfig(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "navi.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts, err := sqlite.NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	n := New("durable",
		WithThoughtStore(ts),
		WithQueueConfig(thought.Config{
			MaxConcurrent:     3,
			SlotExhaustion:    thought.PolicyQueue,
			ReconcileInterval: -1,
			FlushInterval:     -1,
		}),
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
	)
	if n.Thoughts == nil {
		t.Fatal("queue was not built")
	}
	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "p1", Prompt: "persist me", Priority: 4}); err != nil {
		t.Fatal(err)
	}

	// FlushInterval stays off, so the queue writes through synchronously.
	got, err := ts.LoadThought("p1")
	if err != nil {
		t.Fatalf("thought not persisted through the supplied store: %v", err)
	}
	if got.Prompt != "persist me" {
		t.Fatalf("persisted prompt = %q", got.Prompt)
	}
}

// TestInFlightSteering is the tracker unit: unknown thoughts reject, a
// tracked agent receives the message, and unregistration closes the
// window.
func TestInFlightSteering(t *testing.T) {
	tracker := newInFlightAgents()
	a := agent.NewAgent(agent.AgentConfig{})

	if err := tracker.steer("t1", "too early"); err == nil {
		t.Fatal("steering an untracked thought must error")
	}
	unregister := tracker.track("t1", a)
	if err := tracker.steer("t1", "hello"); err != nil {
		t.Fatal(err)
	}
	if msg, ok := a.Steering.Next(); !ok || msg != "hello" {
		t.Fatalf("steering queue = %q (ok=%v), want hello", msg, ok)
	}
	unregister()
	if err := tracker.steer("t1", "too late"); err == nil {
		t.Fatal("steering an unregistered thought must error")
	}
}

// blockingProvider embeds a mock so only Stream needs overriding; it
// blocks until released, giving the test a running thought to steer.
type blockingProvider struct {
	llm.Provider
	started chan struct{}
	release chan struct{}
}

func (p *blockingProvider) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool, config llm.Config) (<-chan llm.StreamEvent, error) {
	close(p.started)
	out := make(chan llm.StreamEvent)
	go func() {
		defer close(out)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// TestRouteMessageSteersRunningThought is the end-to-end steering gate:
// the navi's runner registers the in-flight agent, so RouteMessage's
// running path delivers to the agent instead of rejecting.
func TestRouteMessageSteersRunningThought(t *testing.T) {
	bp := &blockingProvider{
		Provider: llm.NewMockProvider(),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	n := New("steering", WithProvider(bp), WithWorkers(1))
	cancel, done := startServing(t, n)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "hold"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, bp.started, "the run to start")

	if err := n.Thoughts.RouteMessage("t1", "steer me"); err != nil {
		t.Fatalf("running-thought steering failed: %v", err)
	}

	close(bp.release)
	stopServing(t, cancel, done)
}
