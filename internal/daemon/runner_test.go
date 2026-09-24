package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

func scriptedRunner(t *testing.T, streams ...[]llm.StreamEvent) *AgentRunner {
	t.Helper()
	s := testStore(t)
	sessionM := session.NewSessionManager(s)
	return NewAgentRunner(agentRunnerDeps{
		chain:    llm.NewMockProvider(streams...),
		sessionM: sessionM,
	})
}

func TestAgentRunnerCompletesThought(t *testing.T) {
	runner := scriptedRunner(t, []llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: "the answer"},
		{Type: llm.StreamEventStop},
	})
	tt := &thought.Thought{ID: "t1", Prompt: "what?", Status: thought.ThoughtRunning}
	if err := runner.Run(context.Background(), tt); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRunnerFailsOnErrorStream(t *testing.T) {
	runner := scriptedRunner(t, []llm.StreamEvent{
		{Type: llm.StreamEventError, Error: errors.New("model exploded")},
	})
	tt := &thought.Thought{ID: "t2", Prompt: "what?", Status: thought.ThoughtRunning}
	err := runner.Run(context.Background(), tt)
	if err == nil || !strings.Contains(err.Error(), "model exploded") {
		t.Fatalf("error = %v, want model exploded", err)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	// Ruling acceptance (c): park → resume produces equivalent agent
	// state. Encode a state, decode it, assert message-for-message
	// equivalence.
	orig := []agent.AgentMessage{
		{Role: agent.RoleUser, Content: "remember the magic word is pangolin"},
		{Role: agent.RoleAssistant, Content: "noted", ToolCalls: []agent.ToolCall{
			{ToolCallID: "call_0", Name: "file_read", Arguments: []byte(`{"path":"x"}`)},
		}},
		{Role: agent.RoleTool, Content: "file contents", ToolCallID: "call_0"},
	}
	blob, err := encodeCheckpoint(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(blob), checkpointPrefix) {
		t.Fatalf("blob missing version prefix")
	}
	got, err := decodeCheckpoint(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(orig) {
		t.Fatalf("messages = %d, want %d", len(got), len(orig))
	}
	for i := range orig {
		if got[i].Role != orig[i].Role || got[i].Content != orig[i].Content {
			t.Fatalf("msg %d mismatch", i)
		}
	}
}

func TestCheckpointStaleFormatDegradesToFreshRun(t *testing.T) {
	// Ruling condition 3: an unrestorable checkpoint never wedges a
	// worker or loses the thought — worst case is a re-run from the
	// thought prompt with a fresh agent.
	runner := scriptedRunner(t, []llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: "fresh run answer"},
		{Type: llm.StreamEventStop},
	})
	tt := &thought.Thought{
		ID:         "t3",
		Prompt:     "the actual task",
		Status:     thought.ThoughtRunning,
		Checkpoint: []byte("navicp9:not-a-real-version"),
	}
	if err := runner.Run(context.Background(), tt); err != nil {
		t.Fatal(err)
	}
	if tt.Checkpoint != nil {
		t.Fatal("stale checkpoint not cleared")
	}
}

func TestDecodeCheckpointGarbage(t *testing.T) {
	if _, err := decodeCheckpoint([]byte("not a checkpoint at all")); err == nil {
		t.Fatal("unrecognized format should error")
	}
	if _, err := decodeCheckpoint([]byte(checkpointPrefix + "{invalid json")); err == nil {
		t.Fatal("invalid payload should error")
	}
}

// End-to-end through the pool: park-on-cancel preserves the
// conversation, and the parked thought is restorable.
func TestPoolParkPreservesConversation(t *testing.T) {
	s := testStore(t)
	thoughtStore, err := sqlite.NewThoughtStore(s.DB())
	if err != nil {
		t.Fatal(err)
	}
	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue,
	}, thoughtStore)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, t *thought.Thought) error {
		close(started)
		<-ctx.Done()
		// Park semantics: snapshot + return ctx error.
		t.Checkpoint, _ = encodeCheckpoint([]agent.AgentMessage{
			{Role: agent.RoleUser, Content: t.Prompt},
			{Role: agent.RoleAssistant, Content: "partial progress"},
		})
		return ctx.Err()
	})

	p := thought.NewPool(q, runner, 1)
	p.Start()

	if err := q.Enqueue(&thought.Thought{ID: "park-me", Prompt: "long task", Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	<-started
	p.Cancel("park-me")

	// Pool parks the thought (worker Pause path): status paused with
	// checkpoint.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := q.GetThought("park-me")
		if err == nil && got.Status == thought.ThoughtPaused && len(got.Checkpoint) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thought not parked with checkpoint in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.StopAndWait()

	// Restore: decode the checkpoint — equivalent conversation.
	stored, err := q.GetThought("park-me")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := decodeCheckpoint(stored.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "long task" || msgs[1].Content != "partial progress" {
		t.Fatalf("restored conversation = %+v", msgs)
	}
}

// context0 aliases the standard context for test Runner impls.
type context0 = context.Context

// RunnerFunc adapts a function onto thought.Runner.
type RunnerFunc func(ctx context0, t *thought.Thought) error

func (f RunnerFunc) Run(ctx context0, t *thought.Thought) error { return f(ctx, t) }

var _ thought.Runner = RunnerFunc(nil)

// ── provider/model per thought (S010104) ─────────────────────────────────

func TestResolvePerThoughtProviderDefaultsToRuntime(t *testing.T) {
	chain := llm.NewMockProvider([]llm.StreamEvent{{Type: llm.StreamEventStop}})
	r := NewAgentRunner(agentRunnerDeps{
		chain:          chain,
		model:          "runtime-model",
		providerConfig: config.ProviderConfig{Primary: "ollama", Model: "runtime-model"},
	})
	p, m, err := r.resolvePerThoughtProvider(&thought.Thought{ID: "x", Prompt: "p"})
	if err != nil {
		t.Fatalf("no-override resolution failed: %v", err)
	}
	if p != chain || m != "runtime-model" {
		t.Errorf("runtime chain/model not used: %v", m)
	}
}

func TestResolvePerThoughtProviderOverride(t *testing.T) {
	chain := llm.NewMockProvider([]llm.StreamEvent{{Type: llm.StreamEventStop}})
	r := NewAgentRunner(agentRunnerDeps{
		chain:          chain,
		model:          "runtime-model",
		providerConfig: config.ProviderConfig{Primary: "ollama", Model: "runtime-model"},
	})
	p, m, err := r.resolvePerThoughtProvider(&thought.Thought{ID: "x", Prompt: "p", Provider: "mock", Model: "fast-1"})
	if err != nil {
		t.Fatalf("override resolution failed: %v", err)
	}
	if p == nil || p == chain {
		t.Error("override provider not used")
	}
	if m != "fast-1" {
		t.Errorf("model override = %q, want fast-1", m)
	}
}

func TestResolvePerThoughtProviderUnknownFailsLoudly(t *testing.T) {
	r := NewAgentRunner(agentRunnerDeps{providerConfig: config.ProviderConfig{Primary: "ollama"}})
	_, _, err := r.resolvePerThoughtProvider(&thought.Thought{ID: "x", Prompt: "p", Provider: "nosuch-provider"})
	if err == nil || !strings.Contains(err.Error(), "nosuch-provider") {
		t.Errorf("unknown provider error must name it: %v", err)
	}
}
