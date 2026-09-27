package navi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

// scriptedTool builds a complete tool.Tool (all registry-required
// metadata) for runner tests.
func scriptedTool(name string, access tool.ToolAccess, h tool.Handler) tool.Tool {
	schema, err := tool.ParseSchema([]byte(
		`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`))
	if err != nil {
		panic(err)
	}
	return tool.Tool{
		Def: tool.Def{
			Name:          name,
			Description:   "scripted test tool " + name,
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        access,
			Timeout:       5 * time.Second,
			Recovery:      "none",
			Mode:          tool.ToolModeSequential,
			PromptSnippet: "test tool",
			SourceInfo:    tool.SourceInfo{Origin: tool.OriginBuiltin},
		},
		Handler: h,
	}
}

// toolCallStream scripts one tool call followed by a stop.
func toolCallStream(name, args string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallDelta{
			ID: "call-1", Name: name, Arguments: args,
		}},
		{Type: llm.StreamEventStop},
	}
}

// TestAgentRunnerDispatchesThroughTheRegistry is the increment-1 gate: a
// declared tool reaches the agent executor through the registry and
// tools/bridge, the model's call really invokes the handler, and the
// final assistant turn becomes the thought's result.
func TestAgentRunnerDispatchesThroughTheRegistry(t *testing.T) {
	var mu sync.Mutex
	var gotArgs map[string]any

	echo := scriptedTool("echo_run", tool.AccessReadOnly,
		func(_ context.Context, args map[string]any) (string, error) {
			mu.Lock()
			gotArgs = args
			mu.Unlock()
			return "echoed", nil
		})

	mock := llm.NewMockProvider(toolCallStream("echo_run", `{"x":"hi"}`), streamEvents("done"))
	n := mockNavi(t, mock)
	if err := n.Tools.Add(echo); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "use the tool"}); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, n, "t1", "complete")
	if got.Result != "done" {
		t.Fatalf("result = %q, want the final assistant turn", got.Result)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotArgs == nil || gotArgs["x"] != "hi" {
		t.Fatalf("tool handler args = %v, want x=hi", gotArgs)
	}

	// The tool exchange is recorded in the thought's session.
	sess, err := n.SessionStore().LoadSession(SessionIDFor("t1"))
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	text := strings.Join(sessionText(sess), "|")
	if !strings.Contains(text, "echoed") || !strings.Contains(text, "done") {
		t.Fatalf("transcript = %q, want the tool result and final reply", text)
	}
}

// TestUnknownModeFailsTheThought pins loud failure: a thought naming a
// mode the navi does not declare must not silently run with the default
// surface (ADR 0007).
func TestUnknownModeFailsTheThought(t *testing.T) {
	n := mockNavi(t, llm.NewMockProvider(streamEvents("ok")))

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "x", Mode: "nope",
	}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "failed")
}

// TestModeRestrictsTheToolSurface pins the permission ceiling: a
// read-only mode must not bind a write tool, even though the navi's
// compiled surface includes it.
func TestModeRestrictsTheToolSurface(t *testing.T) {
	var wrote bool

	readTool := scriptedTool("read_get", tool.AccessReadOnly,
		func(_ context.Context, _ map[string]any) (string, error) { return "read", nil })
	writeTool := scriptedTool("write_put", tool.AccessWrite,
		func(_ context.Context, _ map[string]any) (string, error) {
			wrote = true
			return "wrote", nil
		})

	// The model asks for the write tool; the mode must make it
	// unreachable, and the run then settles on the final reply.
	mock := llm.NewMockProvider(toolCallStream("write_put", `{"x":"y"}`), streamEvents("done"))
	n := mockNavi(t, mock)
	if err := n.Tools.Add(readTool); err != nil {
		t.Fatal(err)
	}
	if err := n.Tools.Add(writeTool); err != nil {
		t.Fatal(err)
	}
	if err := n.Modes.Add(Mode{
		Name:        "plan",
		Permissions: PermissionPolicy{MaxAccess: tool.AccessReadOnly},
	}); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "write something", Mode: "plan",
	}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, n, "t1", "complete")

	if wrote {
		t.Fatal("a write tool must not be bound in a read-only mode")
	}
}

// TestBuiltinPlanModeValidates pins the shipped planning profile: it is
// a legal mode and it restricts the surface without escalating the
// ceiling.
func TestBuiltinPlanModeValidates(t *testing.T) {
	n := mockNavi(t, llm.NewMockProvider(streamEvents("ok")))
	if err := n.Tools.Add(scriptedTool("read_get", tool.AccessReadOnly,
		func(_ context.Context, _ map[string]any) (string, error) { return "read", nil })); err != nil {
		t.Fatal(err)
	}
	if err := n.Modes.Add(PlanMode); err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("plan mode must validate: %v", err)
	}

	// Starting from the built-in's own policy, narrowing is fine and
	// widening past the compiled ceiling is rejected (ADR 0006).
	ceiling := PlanMode.Permissions
	if err := (PermissionPolicy{MaxAccess: tool.AccessReadOnly}).Exceeds(ceiling); err != nil {
		t.Fatalf("read-only is inside plan's ceiling: %v", err)
	}
	if err := (PermissionPolicy{MaxAccess: tool.AccessExecute}).Exceeds(ceiling); err == nil {
		t.Fatal("execute exceeds plan's read-only ceiling and must be rejected")
	}
}

// TestModeEscalationPastCeilingRejected pins the cross-cutting check Run
// performs: a mode may restrict but never widen the compiled ceiling.
func TestModeEscalationPastCeilingRejected(t *testing.T) {
	n := New("ceiling",
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
		WithPermissions(PermissionPolicy{MaxAccess: tool.AccessReadOnly}),
	)
	if err := n.Modes.Add(ExecuteMode); err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an execute mode above a read-only ceiling must be rejected, got %v", err)
	}
}
