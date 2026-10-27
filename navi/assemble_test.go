package navi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool/thoughts"
)

// TestAssembledContextIsFixedAtEnqueue is the issue's first gate: a thought
// carries its assembled context from Enqueue, and assembly is reproducible.
func TestAssembledContextIsFixedAtEnqueue(t *testing.T) {
	n := New("assemble-test")
	if err := n.Skills.Add(skill.Skill{Name: "auto_one", Content: "auto body", AutoLoad: true}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	first := &thought.Thought{ID: "t1", Prompt: "hi"}
	if err := n.Thoughts.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Assembled, "auto body") {
		t.Fatalf("assembled at enqueue = %q", first.Assembled)
	}

	second := &thought.Thought{ID: "t2", Prompt: "hi"}
	if err := n.Thoughts.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	if second.Assembled != first.Assembled {
		t.Fatalf("assembly is not reproducible:\n%q\n%q", first.Assembled, second.Assembled)
	}
}

// TestAssemblerRejectsUnknownSkillAtEnqueue: a thought that cannot be
// assembled is rejected at the door, not mid-run.
func TestAssemblerRejectsUnknownSkillAtEnqueue(t *testing.T) {
	n := New("reject-test")
	n.assembleSkills()

	err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "hi", Skills: []string{"does_not_exist"}})
	if err == nil {
		t.Fatal("an unknown declared skill must reject the enqueue")
	}
	if !strings.Contains(err.Error(), "does_not_exist") {
		t.Fatalf("error should name the skill: %v", err)
	}
}

// TestRuntimeConfigDoesNotChangeAssembly is the build-time-only gate (ADR
// 0013 decision 4): no runtime config path can add a context source.
func TestRuntimeConfigDoesNotChangeAssembly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("provider:\n  primary: ollama\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	assemble := func(opts ...Option) string {
		n := New("cfg-test", opts...)
		if err := n.Skills.Add(skill.Skill{Name: "fixed", Content: "fixed body", AutoLoad: true}); err != nil {
			t.Fatal(err)
		}
		n.assembleSkills()
		th := &thought.Thought{ID: "t", Prompt: "hi"}
		if err := n.Thoughts.Enqueue(th); err != nil {
			t.Fatal(err)
		}
		return th.Assembled
	}

	plain := assemble()
	configured := assemble(WithRuntimeConfig(cfgPath))
	if plain != configured {
		t.Fatalf("runtime config changed the assembled context:\n%q\n%q", plain, configured)
	}
}

// TestTruncationIsReported pins the budget: over-budget assembly cuts with a
// visible marker rather than silently.
func TestTruncationIsReported(t *testing.T) {
	n := New("truncate-test", WithThoughtBudget(20))
	if err := n.Skills.Add(skill.Skill{
		Name:     "big",
		Content:  strings.Repeat("some guidance sentence. ", 200),
		AutoLoad: true,
	}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	th := &thought.Thought{ID: "t1", Prompt: "hi"}
	if err := n.Thoughts.Enqueue(th); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(th.Assembled, "[context truncated: over the 20-token budget]") {
		t.Fatalf("truncation was not reported: %q", th.Assembled)
	}
	if estimateTokens(th.Assembled) > 20*2 {
		t.Fatalf("truncated context still far over budget: %d tokens", estimateTokens(th.Assembled))
	}
}

// TestDeferralEndToEnd is the acceptance that ties it together: a thought
// approaching its budget writes the handoff into short-term memory and queues
// a successor declaring a skill, and the successor's assembled context
// contains both.
func TestDeferralEndToEnd(t *testing.T) {
	backend := inmem.New()
	mock := llm.NewMockProvider(
		toolCallStream("stm_write", `{"content":"handoff: the config lives in main.go","scope":"session"}`),
		toolCallStream("thoughts_create", `{"prompt":"continue with the runbook","skills":["deploy_runbook"]}`),
		streamEvents("deferred"),
		streamEvents("continued"),
	)
	n := New("defer-navi", WithProvider(mock), WithWorkers(1), WithShortTermMemory())
	if err := n.Datastores.Add(memoryCandidate(backend)); err != nil {
		t.Fatal(err)
	}
	if err := n.Skills.Add(skill.Skill{Name: "deploy_runbook", Content: "deploy runbook body"}); err != nil {
		t.Fatal(err)
	}
	tools, err := thoughts.Tools(n.Thoughts)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if err := n.Tools.Add(tl); err != nil {
			t.Fatal(err)
		}
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "start the task"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	var successor string
	for _, x := range n.Thoughts.GetThoughts() {
		if x.ID != "t1" {
			successor = x.ID
		}
	}
	if successor == "" {
		t.Fatal("the agent did not queue a follow-up thought")
	}
	waitStatus(t, n, successor, "complete")

	reqs := mock.Requests()
	last := reqs[len(reqs)-1]
	if !strings.Contains(last[0].Content, "handoff: the config lives in main.go") {
		t.Fatalf("successor's assembled context is missing the memory handoff:\n%s", last[0].Content)
	}
	if !strings.Contains(last[0].Content, "deploy runbook body") {
		t.Fatalf("successor's assembled context is missing the declared skill:\n%s", last[0].Content)
	}
}
