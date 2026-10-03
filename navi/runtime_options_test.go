package navi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
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

// TestProviderFallbackChain pins the ADR 0009 fallback layer: a configured
// fallback wraps the default resolution, and a per-thought provider
// override bypasses the chain.
func TestProviderFallbackChain(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)
	path := writeConfig(t, "provider:\n  fallback: openrouter\n")

	n := testNavi(t, WithRuntimeConfig(path))
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	if n.fallback == nil {
		t.Fatal("configured fallback did not build a chain")
	}
	p, model, err := n.resolveFor(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*llm.ProviderFallback); !ok {
		t.Fatalf("default resolution = %T, want the fallback chain", p)
	}
	if model != "qwen3.8:27b" {
		t.Fatalf("model = %q", model)
	}

	p2, _, err := n.resolveFor(&thought.Thought{ID: "t2", Provider: "openrouter", Model: "some/model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p2.(*llm.ProviderFallback); ok {
		t.Fatal("a per-thought provider override must not use the fallback chain")
	}
}

func TestCompactionConfig(t *testing.T) {
	if compactionConfig(0) != nil {
		t.Fatal("budget 0 must disable compaction")
	}
	c := compactionConfig(4096)
	if c == nil || !c.Enabled || c.MaxTokens != 4096 {
		t.Fatalf("compaction config = %+v", c)
	}
}

// TestDurableFlusherPersists pins the background persistence loop: a navi
// with a durable store and a positive flush interval actually persists
// thoughts (the flusher/reconcile loops start in serve).
func TestDurableFlusherPersists(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "flush.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts, err := sqlite.NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	n := New("flush",
		WithThoughtStore(ts),
		WithQueueConfig(thought.Config{
			MaxConcurrent:     1,
			SlotExhaustion:    thought.PolicyQueue,
			ReconcileInterval: -1,
			FlushInterval:     10 * time.Millisecond,
		}),
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
	)
	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "flush-1", Prompt: "persist", Priority: 4}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := ts.LoadThought("flush-1"); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("flusher never persisted the thought")
}

// TestWithEndpointAppliesOverride pins the runtime endpoint override: a
// copy is adjusted, the declared set is not mutated, and an empty
// endpoint/provider is a no-op.
func TestWithEndpointAppliesOverride(t *testing.T) {
	models := []model.Provider{
		{Name: "ollama", Endpoint: "http://localhost:11434"},
		{Name: "openrouter", Endpoint: "https://openrouter.ai"},
	}
	got := withEndpoint(models, "ollama", "http://10.0.0.50:11434")
	if got[0].Endpoint != "http://10.0.0.50:11434" {
		t.Fatalf("override not applied: %+v", got[0])
	}
	if models[0].Endpoint != "http://localhost:11434" {
		t.Fatal("withEndpoint mutated the input descriptor")
	}
	if got[1].Endpoint != "https://openrouter.ai" {
		t.Fatalf("other provider changed: %+v", got[1])
	}
	if withEndpoint(models, "", "x")[0].Endpoint != "http://localhost:11434" {
		t.Fatal("empty provider must be a no-op")
	}
	if withEndpoint(models, "ollama", "")[0].Endpoint != "http://localhost:11434" {
		t.Fatal("empty endpoint must be a no-op")
	}
}
