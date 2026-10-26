package navi

import (
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file pins the public compile-and-inspect seam (ADR 0033, NAVI-565): an
// out-of-repo navi can compile its policy and evaluate a real authorization
// decision WITHOUT serving. The acceptance case is a composition test that
// asserts on a decision for every declared tool — an assertion that exercises
// the derived ceiling, the role and group layers, and the conditions, none of
// which a check on the declared grant list can see.

// compileTestNavi builds a navi that validates, declaring the named tools and a
// role that grants exactly the given capabilities to user:operator.
func compileTestNavi(t *testing.T, toolNames []string, grants ...Capability) *Navi {
	t.Helper()
	operator := principal.MustParse("user:operator")
	role, err := NewRole("operator",
		TrustPolicy{Principals: []principal.Ref{operator}},
		CapabilitySet{Allow: Allows(grants...)})
	if err != nil {
		t.Fatalf("NewRole: %v", err)
	}
	n := New("compile-test", WithPlanCloseCheck(false))
	for _, name := range toolNames {
		if err := n.Tools.Add(validTool(name)); err != nil {
			t.Fatalf("Tools.Add(%s): %v", name, err)
		}
	}
	if err := n.Roles.Add(role); err != nil {
		t.Fatalf("Roles.Add: %v", err)
	}
	return n
}

// TestValidateCompilesThePolicy is the core of the decision: Validate is the
// composition-time seam, and after it a navi has a usable Policy without Run.
func TestValidateCompilesThePolicy(t *testing.T) {
	n := compileTestNavi(t, []string{"todo_read"}, Call("todo_read"))
	if n.Policy() != nil {
		t.Fatal("a navi has no compiled policy before it is validated")
	}

	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if n.Policy() == nil {
		t.Fatal("Validate must leave a usable policy (ADR 0033)")
	}

	who, err := NewPrincipal(principal.MustParse("user:operator"))
	if err != nil {
		t.Fatal(err)
	}
	if d := n.Policy().Decide(AccessRequest{Principal: who, Capability: Call("todo_read")}); !d.Allowed {
		t.Fatalf("the declared grant must allow the call: %+v", d)
	}
	// The decision is real: it exercises the derived ceiling and the role
	// layer, so an undeclared tool is refused even though nothing in the role
	// names it.
	if d := n.Policy().Decide(AccessRequest{Principal: who, Capability: Call("undeclared")}); d.Allowed {
		t.Fatal("a call outside the derived ceiling must be refused")
	}
}

// TestValidateFailsOnAPolicyOutsideTheCeiling is decision 5: the referential
// rule is not weakened for the non-serving path. A policy the run would reject
// fails Validate, naming the role and the capability.
func TestValidateFailsOnAPolicyOutsideTheCeiling(t *testing.T) {
	// The role is well formed, so Roles.Add accepts it; whether it names
	// something real is a question only the derived ceiling answers.
	n := compileTestNavi(t, []string{"todo_read"}, Call("undeclared_tool"))
	err := n.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate with an out-of-ceiling grant = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "operator") || !strings.Contains(err.Error(), "undeclared_tool") {
		t.Errorf("the refusal must name the role and the capability, got %q", err)
	}
}

// TestCompilePolicyIsIdempotentAndRecompiles is decision 1 and the staleness
// consequence: CompilePolicy may be called again after a late declaration, and
// the result reflects the current declarations rather than the first set.
func TestCompilePolicyIsIdempotentAndRecompiles(t *testing.T) {
	n := compileTestNavi(t, []string{"todo_read"}, Call("todo_read"))
	if err := n.CompilePolicy(); err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	if err := n.CompilePolicy(); err != nil {
		t.Fatalf("CompilePolicy must be idempotent: %v", err)
	}

	// A tool declared after the first compile is invisible to the policy
	// until it is recompiled — and recompiling picks it up.
	who, err := NewPrincipal(principal.MustParse("user:operator"))
	if err != nil {
		t.Fatal(err)
	}
	if d := n.Policy().Decide(AccessRequest{Principal: who, Capability: Call("todo_write")}); d.Allowed {
		t.Fatal("a tool declared after the compile must not be callable yet")
	}

	if err := n.Tools.Add(validTool("todo_write")); err != nil {
		t.Fatalf("Tools.Add: %v", err)
	}
	// The role still does not grant it, so the ceiling widening alone must not
	// make it callable: the compile step must not manufacture a grant.
	if err := n.CompilePolicy(); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if d := n.Policy().Decide(AccessRequest{Principal: who, Capability: Call("todo_write")}); d.Allowed {
		t.Fatal("recompiling must not grant a capability the role does not name")
	}
	// The reason distinguishes "outside the ceiling" from "no role grants it",
	// which is the difference the new tool makes.
	if d := n.Policy().Decide(AccessRequest{Principal: who, Capability: Call("todo_write")}); strings.Contains(d.Reason, "ceiling") {
		t.Errorf("after recompiling, the new tool is inside the ceiling; refusal reason = %q", d.Reason)
	}
}

// TestValidateCompileFailureIsFatalForAServingNavi confirms the Run path is
// unchanged: a navi that would fail Validate also fails Run, and the compile
// step is the one that reports it.
func TestValidateCompileFailureIsFatalForAServingNavi(t *testing.T) {
	n := compileTestNavi(t, []string{"todo_read"}, Call("undeclared_tool"))
	if err := n.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate = %v, want ErrInvalid", err)
	}
	// Run validates first, so it fails for the same reason rather than
	// reaching serve.
	if err := n.Run(t.Context()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Run = %v, want ErrInvalid", err)
	}
}

// TestValidatedPolicyAnswersThePortalActionPath is decision 6: the portal
// action path is covered by the same seam, so Authorize answers at composition
// time as it does when serving — the operator role that grants enqueue permits
// a portal enqueue, and the same navi refuses an action the role does not
// grant.
func TestValidatedPolicyAnswersThePortalActionPath(t *testing.T) {
	operatorRef := principal.MustParse("user:operator")
	operator, err := NewPrincipal(operatorRef)
	if err != nil {
		t.Fatal(err)
	}
	principals, err := NewPrincipals(operator)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	role, err := NewRole("operator",
		TrustPolicy{Principals: []principal.Ref{operatorRef}},
		CapabilitySet{Allow: Allows(Enqueue(), Connect())})
	if err != nil {
		t.Fatalf("NewRole: %v", err)
	}
	n := New("compile-portal", WithPlanCloseCheck(false), WithTrustSource(principals))
	if err := n.Tools.Add(validTool("todo_read")); err != nil {
		t.Fatalf("Tools.Add: %v", err)
	}
	if err := n.Roles.Add(role); err != nil {
		t.Fatalf("Roles.Add: %v", err)
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	actor := consoleActor(operatorRef)
	if err := n.Authorize(portal.ActionEnqueue, actor); err != nil {
		t.Fatalf("the operator role grants enqueue: %v", err)
	}
	// A control action the role does not grant is refused, with the engine's
	// reason, at composition time.
	err = n.Authorize(portal.ActionPause, actor)
	if !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("pause is not granted = %v, want portal.ErrUnauthorized", err)
	}
	if !strings.Contains(err.Error(), "act:thought.pause") {
		t.Errorf("the refusal must name the capability, got %q", err)
	}
}

// TestValidateStillReportsShapeProblemsFirst pins the ordering: shape checks
// run before the compile step, so an author fixing a navi sees the earlier,
// more specific problem rather than a policy error caused by a half-declared
// navi.
func TestValidateStillReportsShapeProblemsFirst(t *testing.T) {
	n := compileTestNavi(t, []string{"todo_read"}, Call("undeclared_tool"))
	// A shape problem is introduced as well; Validate must report it, not the
	// policy failure that would follow.
	if err := n.Modes.Add(Mode{Name: "broken", Tools: []string{"no_such_tool"}}); err != nil {
		t.Fatalf("Modes.Add: %v", err)
	}
	err := n.Validate()
	if err == nil {
		t.Fatal("Validate must fail")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("the shape problem must be reported first, got %q", err)
	}
}
