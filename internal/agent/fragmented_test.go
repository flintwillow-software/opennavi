package agent

import (
	"sync"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/llm"
)

// TestAgentInvokesToolWithFragmentedArguments is the end-to-end guard for
// NAVI-572: when an OpenAI-compatible provider streams a tool call whose
// arguments arrive in fragments, the tool must be invoked with the FULLY
// ACCUMULATED arguments.
//
// This drives the REAL adapter path (agent.LLMProviderAdapter over an
// llm.Provider), because that is where the defect lived: the adapter discarded
// the terminal tool-call event that carries the accumulated arguments, on the
// belief that the start event already had them. For Ollama it does; for
// OpenAI-compatible providers the start event carries "" and every tool was
// invoked with empty arguments — `invalid arguments: unexpected end of JSON
// input`, which is how Bastilli's first real inbound thought died.
//
// A test that scripts the agent's own Event type would miss this entirely: it
// would bypass the adapter. The provider-level mock is the point.
func TestAgentInvokesToolWithFragmentedArguments(t *testing.T) {
	// One streamed turn: start with empty arguments, terminal with the body.
	inner := llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventMessageStart},
		{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallDelta{
			ID: "call_1", Name: "echo", Arguments: "",
		}},
		{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallDelta{
			ID: "call_1", Name: "echo", Arguments: `{"issue":"NAVI-571"}`,
		}},
		{Type: llm.StreamEventMessageEnd},
		{Type: llm.StreamEventStop},
	}, []llm.StreamEvent{
		{Type: llm.StreamEventMessageStart},
		{Type: llm.StreamEventContentDelta, Content: "done"},
		{Type: llm.StreamEventMessageEnd},
		{Type: llm.StreamEventStop},
	})

	var mu sync.Mutex
	var gotArgs []map[string]any
	exec := NewToolExecutor("parallel", 1)
	exec.RegisterTool(AgentTool{Name: "echo", Description: "echo"}, func(args map[string]any) (string, error) {
		mu.Lock()
		gotArgs = append(gotArgs, args)
		mu.Unlock()
		return "ok", nil
	})

	a := NewAgent(AgentConfig{Provider: "openrouter", Model: "m"})
	a.Provider = NewProviderAdapter(inner, "openrouter")
	a.Executor = exec

	if err := a.Prompt("go", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	a.WaitForIdle()

	mu.Lock()
	defer mu.Unlock()
	if len(gotArgs) != 1 {
		t.Fatalf("tool invoked %d times, want 1 (a Complete update must replace, not duplicate)", len(gotArgs))
	}
	if got := gotArgs[0]["issue"]; got != "NAVI-571" {
		t.Fatalf("tool received %#v, want the accumulated arguments", gotArgs[0])
	}
}

// TestAgentKeepsSingleChunkArgumentsWorking pins the other provider shape:
// Ollama sends name AND arguments in one chunk and emits no terminal event
// carrying more. That must keep working unchanged.
func TestAgentKeepsSingleChunkArgumentsWorking(t *testing.T) {
	inner := llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventMessageStart},
		{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallDelta{
			ID: "c1", Name: "echo", Arguments: `{"whole":"object"}`,
		}},
		{Type: llm.StreamEventMessageEnd},
		{Type: llm.StreamEventStop},
	}, []llm.StreamEvent{
		{Type: llm.StreamEventMessageStart},
		{Type: llm.StreamEventMessageEnd},
		{Type: llm.StreamEventStop},
	})

	var mu sync.Mutex
	var got map[string]any
	exec := NewToolExecutor("parallel", 1)
	exec.RegisterTool(AgentTool{Name: "echo", Description: "e"}, func(args map[string]any) (string, error) {
		mu.Lock()
		got = args
		mu.Unlock()
		return "ok", nil
	})

	a := NewAgent(AgentConfig{Provider: "ollama", Model: "m"})
	a.Provider = NewProviderAdapter(inner, "ollama")
	a.Executor = exec
	if err := a.Prompt("go", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	a.WaitForIdle()

	mu.Lock()
	defer mu.Unlock()
	if got["whole"] != "object" {
		t.Fatalf("single-chunk arguments regressed: %#v", got)
	}
}
