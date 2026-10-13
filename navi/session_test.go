package navi

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the session-policy test (ADR 0022 decision 4, ADR 0026): the
// mode chosen at enqueue narrows what a thought's calls may do, never widens,
// and is validated against the originator at the door.

// TestModeSessionDeniesWhatTheModeExcludes pins the derivation: a session is a
// narrowing layer, so it allows every verb and denies what the mode excludes.
func TestModeSessionDeniesWhatTheModeExcludes(t *testing.T) {
	n := New("session-derive", WithPlanCloseCheck(false))
	if err := n.Tools.Add(validTool("read_thing")); err != nil {
		t.Fatal(err)
	}
	if err := n.Tools.Add(execTool("shell_exec")); err != nil {
		t.Fatal(err)
	}

	plan := n.modeSession(PlanMode)
	if plan.Permits(Call("shell_exec"), RequestContext{}) {
		t.Error("a read-only mode must deny an execute tool")
	}
	if plan.Permits(Act("thought", "pause"), RequestContext{}) {
		t.Error("a read-only mode must deny an object mutation")
	}
	if !plan.Permits(Call("read_thing"), RequestContext{}) {
		t.Error("a read-only mode must permit a read-only tool")
	}
	if !plan.Permits(Read("thought"), RequestContext{}) {
		t.Error("a read-only mode must permit a read")
	}
	if !plan.Permits(Enqueue(), RequestContext{}) {
		t.Error("a read-only mode must permit enqueue — a plan continues by scheduling")
	}

	// An explicit tool subset denies tools outside it.
	subset := n.modeSession(Mode{Name: "narrow", Tools: []string{"read_thing"}})
	if subset.Permits(Call("shell_exec"), RequestContext{}) {
		t.Error("a tool outside the declared subset must be denied")
	}
	if !subset.Permits(Call("read_thing"), RequestContext{}) {
		t.Error("a tool in the declared subset must be permitted")
	}

	// A mode that declares nothing denies nothing: the session is the identity
	// of the narrowing layer.
	bare := n.modeSession(ExecuteMode)
	if !bare.Permits(Call("shell_exec"), RequestContext{}) || !bare.Permits(Act("thought", "pause"), RequestContext{}) {
		t.Error("an unrestricted mode must deny nothing")
	}
}

// TestModeSessionNarrowsAToolCall is the engine half: a principal that holds a
// capability is still refused when the thought's mode excludes it, and the same
// call without a mode goes through.
func TestModeSessionNarrowsAToolCall(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "shell", aliceRef, Grant{Capability: Call("shell_exec")}))
	if err := n.Modes.Add(PlanMode); err != nil {
		t.Fatal(err)
	}

	plan := &thought.Thought{ID: "t1", Origin: aliceRef, Mode: "plan"}
	if err := n.toolAuthorize(plan)("shell_exec"); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("plan mode must refuse shell_exec even for a holder: %v", err)
	}
	if err := n.toolAuthorize(&thought.Thought{ID: "t2", Origin: aliceRef})("shell_exec"); err != nil {
		t.Fatalf("no mode must permit alice's shell_exec: %v", err)
	}
}

// TestModeSelectionIsRefusedAtTheDoor is ADR 0026 decision 3: a mode broader
// than the originator's own policy is refused when the thought is enqueued,
// loudly, rather than silently narrowed when it runs. A more restrictive mode
// is always selectable.
func TestModeSelectionIsRefusedAtTheDoor(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	// alice holds no role at all, so she may call no tool.
	n := delegationNavi(t, alice)
	if err := n.Modes.Add(ExecuteMode); err != nil {
		t.Fatal(err)
	}
	if err := n.Modes.Add(PlanMode); err != nil {
		t.Fatal(err)
	}

	err = n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "x", Status: thought.ThoughtPending, Origin: aliceRef, Mode: "execute",
	})
	if !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("a mode broader than alice's policy = %v, want a refusal at enqueue", err)
	}
	if _, gerr := n.Thoughts.GetThought("t1"); gerr == nil {
		t.Fatal("a refused mode must not schedule the thought")
	}

	// Plan permits only read-only tools, and the navi declares only an execute
	// tool — so it permits nothing alice may not do, and is selectable.
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t2", Prompt: "x", Status: thought.ThoughtPending, Origin: aliceRef, Mode: "plan",
	}); err != nil {
		t.Fatalf("a more restrictive mode must be selectable: %v", err)
	}
	got, err := n.Thoughts.GetThought("t2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "plan" {
		t.Errorf("mode = %q, want it recorded on the thought", got.Mode)
	}
}

// TestModeSelectionPermitsAModeWithinThePrincipal is the positive half: a
// principal holding the tool a mode permits may select it.
func TestModeSelectionPermitsAModeWithinThePrincipal(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "shell", aliceRef, Grant{Capability: Call("shell_exec")}))
	if err := n.Modes.Add(ExecuteMode); err != nil {
		t.Fatal(err)
	}
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "x", Status: thought.ThoughtPending, Origin: aliceRef, Mode: "execute",
	}); err != nil {
		t.Fatalf("a mode within alice's policy must be selectable: %v", err)
	}
}
