package navi

import (
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/principal"
)

// declaredCeiling is a small derived ceiling: one tool and one type with a
// field and an action.
func declaredCeiling() CapabilitySet {
	return CeilingFromDeclarations([]object.Type{{
		Name:    "thought",
		Fields:  []object.Field{{Name: "id"}, {Name: "assembled"}},
		Actions: []object.Action{{Name: "pause"}, {Name: "cancel"}},
	}}, []string{"todo_read", "shell_exec"})
}

func resolved(t *testing.T, ref string, claims map[string]string, groups ...string) *Principal {
	t.Helper()
	refs := make([]principal.Ref, 0, len(groups))
	for _, g := range groups {
		refs = append(refs, principal.MustParse(g))
	}
	p, err := NewResolvedPrincipal(principal.MustParse(ref), claims, refs)
	if err != nil {
		t.Fatalf("NewResolvedPrincipal: %v", err)
	}
	return p
}

func TestCeilingFromDeclarations(t *testing.T) {
	c := declaredCeiling()
	for _, want := range []Capability{
		Connect(), Enqueue(),
		Call("todo_read"), Call("shell_exec"),
		Read("thought"), ReadField("thought", "id"), ReadField("thought", "assembled"),
		Act("thought", "pause"), Act("thought", "cancel"),
	} {
		if !c.Permits(want, RequestContext{}) {
			t.Errorf("%s must be inside the derived ceiling", want)
		}
	}
	// Anything undeclared is outside it — the referential rule, by construction.
	for _, outside := range []Capability{
		Call("undeclared_tool"), Read("todo"), Act("thought", "delete"), ReadField("thought", "nope"),
	} {
		if c.Permits(outside, RequestContext{}) {
			t.Errorf("%s must be outside the derived ceiling", outside)
		}
	}
}

func TestGroupIsThePermissionSide(t *testing.T) {
	ref := principal.MustParse("group:eng")
	grants := CapabilitySet{Allow: Allows(Call("todo_read"))}
	g, err := NewGroup(ref, grants)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	if g.Ref() != ref {
		t.Errorf("Ref = %s, want %s", g.Ref(), ref)
	}
	if !g.Grants().Permits(Call("todo_read"), RequestContext{}) {
		t.Error("the group's grants must be returned")
	}
	if g.Shared() != nil {
		t.Error("a group is not a shared identity until it is made one")
	}

	// A group reference must be group-kind.
	if _, err := NewGroup(principal.MustParse("user:alice"), grants); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("non-group ref = %v, want ErrInvalidPrincipal", err)
	}
	if _, err := NewGroup(principal.Ref{}, grants); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("zero ref = %v, want ErrInvalidPrincipal", err)
	}
}

func TestRoleSplitsTrustAndPermission(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Read("thought"))})
	if err != nil {
		t.Fatalf("NewRole: %v", err)
	}
	if role.Name != "reader" || role.IsZero() {
		t.Errorf("role = %+v", role)
	}
	if !role.Holds.Holds(resolved(t, "user:alice", nil)) {
		t.Error("the named principal must hold the role")
	}
	if role.Holds.Holds(resolved(t, "user:bob", nil)) {
		t.Error("another principal must not hold the role")
	}

	// A role is not a principal: it has no ref and no keys.
	if _, err := NewRole("  ", TrustPolicy{Principals: []principal.Ref{alice}}, CapabilitySet{}); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("empty name = %v, want ErrInvalidRole", err)
	}
	// A trust policy nobody can satisfy is refused.
	if _, err := NewRole("nobody", TrustPolicy{}, CapabilitySet{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty trust policy = %v, want ErrInvalid", err)
	}
	var zero Role
	if !zero.IsZero() {
		t.Error("the zero Role must report zero")
	}
}

func TestTrustPolicyHolds(t *testing.T) {
	eng := principal.MustParse("group:eng")
	alice := principal.MustParse("user:alice")

	direct := TrustPolicy{Principals: []principal.Ref{alice}}
	if !direct.Holds(resolved(t, "user:alice", nil)) {
		t.Error("a named principal holds it")
	}
	if direct.Holds(nil) {
		t.Error("no principal holds nothing")
	}

	// Group membership comes from the trust source, which is what places the
	// principal in the group (ADR 0021 decision 4).
	viaGroup := TrustPolicy{Groups: []principal.Ref{eng}}
	if !viaGroup.Holds(resolved(t, "user:bob", nil, "group:eng")) {
		t.Error("a principal the trust source placed in the group holds it")
	}
	if viaGroup.Holds(resolved(t, "user:bob", nil)) {
		t.Error("a principal outside the group does not hold it")
	}

	// Every required claim must be present.
	viaClaim := TrustPolicy{Claims: map[string]string{"tier": "field"}}
	if !viaClaim.Holds(resolved(t, "user:carol", map[string]string{"tier": "field"})) {
		t.Error("a principal carrying the claim holds it")
	}
	if viaClaim.Holds(resolved(t, "user:carol", map[string]string{"tier": "guest"})) {
		t.Error("a different claim value must not hold it")
	}
	if viaClaim.Holds(resolved(t, "user:carol", nil)) {
		t.Error("an absent claim must not hold it")
	}

	// A group-kind reference is required where a group is named.
	bad := TrustPolicy{Groups: []principal.Ref{principal.MustParse("user:not-a-group")}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("non-group in a trust policy = %v, want ErrInvalid", err)
	}
}

func TestPolicyDefaultDeny(t *testing.T) {
	p, err := NewPolicy(declaredCeiling(), nil, nil)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	d := p.Decide(AccessRequest{Principal: resolved(t, "user:nobody", nil), Capability: Call("todo_read")})
	if d.Allowed {
		t.Fatal("an empty policy must deny")
	}
	if d.Reason == "" {
		t.Error("a refusal must say why (a loud refusal, not a silent one)")
	}
}

func TestPolicyAllowsViaRoleAndGroup(t *testing.T) {
	alice := principal.MustParse("user:alice")
	eng := principal.MustParse("group:eng")

	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("todo_read"), Read("thought"))})
	if err != nil {
		t.Fatal(err)
	}
	group, err := NewGroup(eng, CapabilitySet{Allow: Allows(Act("thought", "pause"))})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, []*Group{group})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	// The role's grant applies to the principal that holds it.
	d := p.Decide(AccessRequest{Principal: resolved(t, "user:alice", nil), Capability: Read("thought")})
	if !d.Allowed || d.Matched != "role:reader" {
		t.Fatalf("role grant = %+v, want allowed by role:reader", d)
	}
	// The group's grant applies to whoever the trust source put in the group.
	d = p.Decide(AccessRequest{Principal: resolved(t, "user:bob", nil, "group:eng"), Capability: Act("thought", "pause")})
	if !d.Allowed || d.Matched != "group:group:eng" {
		t.Fatalf("group grant = %+v, want allowed by the group", d)
	}
	// A principal with neither is denied.
	d = p.Decide(AccessRequest{Principal: resolved(t, "user:carol", nil), Capability: Read("thought")})
	if d.Allowed {
		t.Fatal("a principal with no applicable layer must be denied")
	}
}

func TestPolicyDenyWinsAcrossLayers(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("shell_exec"))})
	if err != nil {
		t.Fatal(err)
	}
	group, err := NewGroup(principal.MustParse("group:eng"),
		CapabilitySet{Deny: Allows(Call("shell_exec"))})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, []*Group{group})
	if err != nil {
		t.Fatal(err)
	}

	// The role allows it, the group denies it, and the principal is in both.
	d := p.Decide(AccessRequest{
		Principal:  resolved(t, "user:alice", nil, "group:eng"),
		Capability: Call("shell_exec"),
	})
	if d.Allowed {
		t.Fatal("an explicit deny in any layer must win")
	}
	if !strings.Contains(d.Reason, "denied") {
		t.Errorf("the refusal should name the deny: %q", d.Reason)
	}
}

func TestPolicyIntersectsTheCeiling(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("todo_read"))})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A capability outside the derived ceiling is denied whatever a role says
	// — and this role cannot even be declared with one (see the next test).
	d := p.Decide(AccessRequest{Principal: resolved(t, "user:alice", nil), Capability: Call("undeclared")})
	if d.Allowed {
		t.Fatal("a capability outside the ceiling must be denied")
	}
	if !strings.Contains(d.Reason, "ceiling") {
		t.Errorf("the refusal should name the ceiling: %q", d.Reason)
	}
}

func TestNewPolicyRejectsGrantsOutsideTheCeiling(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("power",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("undeclared"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicy(declaredCeiling(), []Role{role}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("a grant outside the ceiling = %v, want ErrInvalid", err)
	}

	// Duplicates are refused too — on a role that is otherwise valid.
	good, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("todo_read"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicy(declaredCeiling(), []Role{good, good}, nil); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate role = %v, want ErrDuplicate", err)
	}
}

func TestPolicySessionNarrowsAndNeverWidens(t *testing.T) {
	alice := principal.MustParse("user:alice")
	// The role permits enqueue on the LAN only.
	role, err := NewRole("lan-only",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport("lan"))}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, nil)
	if err != nil {
		t.Fatal(err)
	}
	who := resolved(t, "user:alice", nil)
	lan := RequestContext{Transport: "lan"}
	lora := RequestContext{Transport: "lora"}

	if !p.Decide(AccessRequest{Principal: who, Capability: Enqueue(), Context: lan}).Allowed {
		t.Fatal("the LAN grant must permit the LAN")
	}
	if p.Decide(AccessRequest{Principal: who, Capability: Enqueue(), Context: lora}).Allowed {
		t.Fatal("the LAN grant must not permit LoRa")
	}

	// A bare session policy does NOT widen the conditioned role: every layer
	// must permit, so the meet is the conditioned one.
	bare := CapabilitySet{Allow: Allows(Enqueue())}
	if p.Decide(AccessRequest{Principal: who, Capability: Enqueue(), Context: lora, Session: &bare}).Allowed {
		t.Fatal("a bare session must not widen a conditioned role")
	}
	if !p.Decide(AccessRequest{Principal: who, Capability: Enqueue(), Context: lan, Session: &bare}).Allowed {
		t.Fatal("a bare session must not narrow a permitted request either")
	}

	// A session that denies narrows further.
	deny := CapabilitySet{Deny: Allows(Enqueue())}
	if p.Decide(AccessRequest{Principal: who, Capability: Enqueue(), Context: lan, Session: &deny}).Allowed {
		t.Fatal("a session deny must narrow")
	}
}

// TestConnectorPrincipalIsAGrantableSubject is the acceptance criterion carried
// over from NAVI-403: the vocabulary must be able to name a connector:* subject
// as a policy subject, so a navi that composes a connector whose inbound work
// acts as connector:github can grant it enqueue — and a refusal is loud.
func TestConnectorPrincipalIsAGrantableSubject(t *testing.T) {
	github := principal.MustParse("connector:github")
	role, err := NewRole("inbound",
		TrustPolicy{Principals: []principal.Ref{github}},
		CapabilitySet{Allow: Allows(Enqueue())})
	if err != nil {
		t.Fatalf("NewRole with a connector subject: %v", err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, nil)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	who, err := NewPrincipal(github)
	if err != nil {
		t.Fatal(err)
	}
	d := p.Decide(AccessRequest{Principal: who, Capability: Enqueue()})
	if !d.Allowed {
		t.Fatalf("connector:github must be grantable enqueue: %+v", d)
	}

	// The same connector is refused a capability it was not granted, with a
	// reason — the failure a navi would otherwise see as a poll that quietly
	// never succeeds.
	d = p.Decide(AccessRequest{Principal: who, Capability: Call("shell_exec")})
	if d.Allowed {
		t.Fatal("an ungranted capability must be refused")
	}
	if !strings.Contains(d.Reason, "no role or group grants") {
		t.Errorf("the refusal must name the reason: %q", d.Reason)
	}
}

func TestPolicyConditionsComposeAcrossLayers(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: []Grant{When(Call("todo_read"), OnChannel("console"))}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(declaredCeiling(), []Role{role}, nil)
	if err != nil {
		t.Fatal(err)
	}
	who := resolved(t, "user:alice", nil)

	if !p.Decide(AccessRequest{Principal: who, Capability: Call("todo_read"), Context: RequestContext{Channel: "console"}}).Allowed {
		t.Fatal("the console channel must be permitted")
	}
	if p.Decide(AccessRequest{Principal: who, Capability: Call("todo_read"), Context: RequestContext{Channel: "microphone"}}).Allowed {
		t.Fatal("a microphone must not inherit the console's grant")
	}
}
