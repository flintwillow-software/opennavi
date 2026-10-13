package navi

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/principal"
)

// TestNaviCompilesTheDeclaredPolicy pins the build-time declaration path: roles
// and groups are declared like tools and models, and assembly derives the
// ceiling from the catalog and compiles them against it.
func TestNaviCompilesTheDeclaredPolicy(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Read("thought"))})
	if err != nil {
		t.Fatal(err)
	}
	group, err := NewGroup(principal.MustParse("group:eng"), CapabilitySet{Allow: Allows(Enqueue())})
	if err != nil {
		t.Fatal(err)
	}

	n := New("policy-test", WithPlanCloseCheck(false))
	if err := n.Roles.Add(role); err != nil {
		t.Fatalf("Roles.Add: %v", err)
	}
	if err := n.Groups.Add(group); err != nil {
		t.Fatalf("Groups.Add: %v", err)
	}
	if n.Policy() != nil {
		t.Fatal("no policy exists before assembly")
	}

	if err := n.compilePolicy(); err != nil {
		t.Fatalf("compilePolicy: %v", err)
	}
	p := n.Policy()
	if p == nil {
		t.Fatal("assembly compiled no policy")
	}
	if len(p.Roles()) != 1 || len(p.Groups()) != 1 {
		t.Fatalf("compiled %d roles and %d groups, want 1 and 1", len(p.Roles()), len(p.Groups()))
	}

	// The ceiling is derived from what the navi declares: `thought` is one of
	// its own exposed types, so reading it is inside the ceiling.
	who, err := NewPrincipal(alice)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Decide(AccessRequest{Principal: who, Capability: Read("thought")}); !d.Allowed {
		t.Fatalf("the declared role must grant read:thought: %+v", d)
	}
	// A capability naming something the navi does not declare is outside it.
	if d := p.Decide(AccessRequest{Principal: who, Capability: Call("undeclared_tool")}); d.Allowed {
		t.Fatal("an undeclared capability must be outside the derived ceiling")
	}
}

// TestNaviRefusesAPolicyOutsideTheDerivedCeiling is the referential rule: a
// declaration that names something the navi does not have fails the run rather
// than being discovered per request.
func TestNaviRefusesAPolicyOutsideTheDerivedCeiling(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("power",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Call("undeclared_tool"))})
	if err != nil {
		t.Fatal(err)
	}
	// The role is well formed, so Add accepts it: whether it names something
	// real is a question only the derived ceiling can answer.
	n := New("policy-bad", WithPlanCloseCheck(false))
	if err := n.Roles.Add(role); err != nil {
		t.Fatalf("Roles.Add: %v", err)
	}
	if err := n.compilePolicy(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a role granting an undeclared tool = %v, want ErrInvalid", err)
	}

	// The same for a group.
	g, err := NewGroup(principal.MustParse("group:eng"), CapabilitySet{Allow: Allows(Act("thought", "undeclared"))})
	if err != nil {
		t.Fatal(err)
	}
	n2 := New("policy-bad-group", WithPlanCloseCheck(false))
	if err := n2.Groups.Add(g); err != nil {
		t.Fatalf("Groups.Add: %v", err)
	}
	if err := n2.compilePolicy(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a group granting an undeclared action = %v, want ErrInvalid", err)
	}
}

func TestNaviValidateRejectsDuplicateAndEmptyDeclarations(t *testing.T) {
	alice := principal.MustParse("user:alice")
	role, err := NewRole("reader",
		TrustPolicy{Principals: []principal.Ref{alice}},
		CapabilitySet{Allow: Allows(Read("thought"))})
	if err != nil {
		t.Fatal(err)
	}

	// A duplicate role name is refused at Add, which is where the collection
	// keeps its index consistent — it does not append the duplicate, so the
	// backstop re-check in validate has nothing to find.
	n := New("policy-dup", WithPlanCloseCheck(false))
	if err := n.Roles.Add(role); err != nil {
		t.Fatal(err)
	}
	if err := n.Roles.Add(role); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate role = %v, want ErrDuplicate", err)
	}
	if got := n.Roles.Len(); got != 1 {
		t.Errorf("the collection holds %d roles after a refused duplicate, want 1", got)
	}

	// A malformed declaration is refused at Add.
	empty := New("policy-empty", WithPlanCloseCheck(false))
	if err := empty.Roles.Add(Role{}); err == nil {
		t.Error("an unnamed role must be refused at Add")
	}
	if err := empty.Groups.Add(nil); err == nil {
		t.Error("a nil group must be refused at Add")
	}
}
