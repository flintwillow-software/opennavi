package navi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the delegated-authorization enforcement test (ADR 0022): the
// acting principal reaches every enforcement point, the request facts a
// condition is evaluated against are the ones the binding supplied, and a
// refusal is loud rather than looking like nothing happened.

// delegationNavi builds a navi with a compiled policy and alice resolved by a
// trust source. Roles are added by the caller before compilePolicy.
func delegationNavi(t *testing.T, alice *Principal, roles ...Role) *Navi {
	t.Helper()
	principals, err := NewPrincipals(alice)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	n := New("delegation-test", WithPlanCloseCheck(false), WithTrustSource(principals))
	if err := n.Tools.Add(validTool("shell_exec")); err != nil {
		t.Fatalf("Tools.Add: %v", err)
	}
	for _, r := range roles {
		if err := n.Roles.Add(r); err != nil {
			t.Fatalf("Roles.Add(%s): %v", r.Name, err)
		}
	}
	if err := n.compilePolicy(); err != nil {
		t.Fatalf("compilePolicy: %v", err)
	}
	return n
}

func mustRole(t *testing.T, name string, who principal.Ref, grants ...Grant) Role {
	t.Helper()
	r, err := NewRole(name, TrustPolicy{Principals: []principal.Ref{who}}, CapabilitySet{Allow: grants})
	if err != nil {
		t.Fatalf("NewRole(%s): %v", name, err)
	}
	return r
}

// TestRequestFactsReachConditions is the piece NAVI-404 must not miss: the
// binding supplies transport and channel, and the engine evaluates a condition
// against them. A policy conditioned on transport must allow through the
// matching binding and refuse through a different one — not refuse through
// both, which is what happens when the facts are never threaded.
func TestRequestFactsReachConditions(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "lora-enqueue", aliceRef, When(Enqueue(), OnTransport("lora"))))

	overLora := portal.Actor{Who: aliceRef, Info: portal.RequestInfo{Transport: "lora", Channel: "api"}}
	if err := n.Authorize(portal.ActionEnqueue, overLora); err != nil {
		t.Fatalf("enqueue over the granted transport = %v, want allowed", err)
	}

	overLan := portal.Actor{Who: aliceRef, Info: portal.RequestInfo{Transport: "lan", Channel: "api"}}
	err = n.Authorize(portal.ActionEnqueue, overLan)
	if !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("enqueue over a different transport = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "enqueue") {
		t.Errorf("the refusal must name the action, got %q", err)
	}

	// The input channel is a separate fact: the same transport with a
	// different channel must not inherit the grant.
	if err := n.Authorize(portal.ActionEnqueue, portal.Actor{Who: aliceRef, Info: portal.RequestInfo{Transport: "lora", Channel: "microphone"}}); err != nil {
		t.Errorf("the grant is transport-scoped, so a different channel is still allowed: %v", err)
	}
}

// TestChannelConditionDistinguishesInputPaths is the other half: a grant
// scoped to the console must not extend to an ambient microphone embedded the
// same way (ADR 0021 decision 9).
func TestChannelConditionDistinguishesInputPaths(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "console-only", aliceRef, When(Call("shell_exec"), OnChannel("console"))))

	console := &thought.Thought{ID: "t1", Origin: aliceRef, OriginInfo: thought.OriginInfo{Channel: "console"}}
	if err := n.toolAuthorize(console)("shell_exec"); err != nil {
		t.Fatalf("a console-originated call = %v, want allowed", err)
	}

	mic := &thought.Thought{ID: "t2", Origin: aliceRef, OriginInfo: thought.OriginInfo{Channel: "microphone"}}
	if err := n.toolAuthorize(mic)("shell_exec"); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("a microphone-originated call = %v, want a refusal", err)
	}
}

// TestRefusalIsLoud pins the acceptance criterion carried from NAVI-403: a
// refused enqueue carries the engine's reason, so it cannot look like nothing
// happened at the call site.
func TestRefusalIsLoud(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	// alice holds no role at all.
	n := delegationNavi(t, alice)

	err = n.Authorize(portal.ActionEnqueue, portal.Actor{Who: aliceRef})
	if !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("enqueue with no grant = %v, want ErrUnauthorized", err)
	}
	if !strings.Contains(err.Error(), "enqueue") || !strings.Contains(err.Error(), "no role or group") {
		t.Errorf("the refusal must name the action and the reason, got %q", err)
	}
}

// TestToolPathSharesThePrincipal is ADR 0022 decision 5: the same principal
// that caused a thought authorizes its tool calls, so a principal cannot
// launder an action through the agent.
func TestToolPathSharesThePrincipal(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "shell", aliceRef, Grant{Capability: Call("shell_exec")}))

	onBehalfOfAlice := &thought.Thought{ID: "t1", Origin: aliceRef}
	if err := n.toolAuthorize(onBehalfOfAlice)("shell_exec"); err != nil {
		t.Fatalf("alice's tool call = %v, want allowed", err)
	}

	// A different principal that did not get the grant is refused.
	bobRef := principal.MustParse("user:bob")
	onBehalfOfBob := &thought.Thought{ID: "t2", Origin: bobRef}
	if err := n.toolAuthorize(onBehalfOfBob)("shell_exec"); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("bob's tool call = %v, want a refusal", err)
	}

	// System-originated work acts as the navi's service principal and holds
	// the compiled ceiling, so the runtime's own machinery keeps working
	// (ADR 0021 decision 10).
	system := &thought.Thought{ID: "t3"}
	if err := n.toolAuthorize(system)("shell_exec"); err != nil {
		t.Fatalf("system-originated tool call = %v, want allowed", err)
	}
}

// TestObjectPathSharesThePrincipal is the object half of decision 5: a read
// and a declared action authorize as the connection's actor, and the same
// mutation through the object path and through the control path agree.
func TestObjectPathSharesThePrincipal(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice, mustRole(t, "cancel", aliceRef, Grant{Capability: Act("thought", "cancel")}, Grant{Capability: Read("thought")}))
	enqueueThought(t, n, "t1", time.Now().UTC())

	aliceActor := consoleActor(aliceRef)
	scoped := n.objects.Scope(aliceActor)

	// The read is authorized as alice and her grant permits it.
	if _, _, err := scoped.List(TypeThought, object.Query{}); err != nil {
		t.Fatalf("alice's read = %v, want allowed", err)
	}
	if err := scoped.Act(TypeThought, "t1", "cancel", nil); err != nil {
		t.Fatalf("alice's cancel = %v, want allowed", err)
	}

	// A principal with no role is refused for both the read and the action,
	// while the surface's declared default actor (the navi's service
	// principal) is unaffected.
	bobActor := consoleActor(principal.MustParse("user:bob"))
	if _, _, err := n.objects.Scope(bobActor).List(TypeThought, object.Query{}); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("bob's read = %v, want a refusal", err)
	}
	if err := n.objects.Scope(bobActor).Act(TypeThought, "t1", "cancel", nil); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("bob's cancel = %v, want a refusal", err)
	}
}

// TestFollowUpInheritsOriginator is ADR 0022 decision 3: a thought that
// declares a dependency takes that dependency's principal, request facts, and
// lineage, so "on whose behalf" is carried down the chain.
func TestFollowUpInheritsOriginator(t *testing.T) {
	aliceRef := principal.MustParse("user:alice")
	alice, err := NewPrincipal(aliceRef)
	if err != nil {
		t.Fatal(err)
	}
	n := delegationNavi(t, alice)

	parent := &thought.Thought{
		ID: "parent", Prompt: "start", Status: thought.ThoughtPending,
		Origin: aliceRef, OriginInfo: thought.OriginInfo{Transport: "lora", Channel: "api"},
	}
	if err := n.Thoughts.Enqueue(parent); err != nil {
		t.Fatalf("enqueue parent: %v", err)
	}
	child := &thought.Thought{ID: "child", Prompt: "continue", Status: thought.ThoughtPending, DependsOn: []string{"parent"}}
	if err := n.Thoughts.Enqueue(child); err != nil {
		t.Fatalf("enqueue child: %v", err)
	}

	got, err := n.Thoughts.GetThought("child")
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != aliceRef {
		t.Errorf("child origin = %s, want %s", got.Origin, aliceRef)
	}
	if got.OriginInfo.Transport != "lora" || got.OriginInfo.Channel != "api" {
		t.Errorf("child origin facts = %+v, want the parent's", got.OriginInfo)
	}
	if len(got.Lineage) != 1 || got.Lineage[0] != "parent" {
		t.Errorf("child lineage = %v, want [parent]", got.Lineage)
	}

	// A thought with neither an origin nor a dependency is system-originated:
	// the runtime attributes it to its own service principal at decision time,
	// and it is not silently stamped with someone else's identity.
	orphan := &thought.Thought{ID: "orphan", Prompt: "tick", Status: thought.ThoughtPending}
	if err := n.Thoughts.Enqueue(orphan); err != nil {
		t.Fatalf("enqueue orphan: %v", err)
	}
	gotOrphan, err := n.Thoughts.GetThought("orphan")
	if err != nil {
		t.Fatal(err)
	}
	if !gotOrphan.Origin.IsZero() {
		t.Errorf("an unattributed thought gained origin %s", gotOrphan.Origin)
	}
}
