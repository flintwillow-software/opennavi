package navi

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the row-level read-scoping test (ADR 0025 decision 3, ADR 0028):
// a type may declare a scope, the surface ANDs it with the caller's query, and
// the provider applies the result — so a scoped principal sees its own rows and
// a caller can narrow but never widen.

// scopeTestProvider serves a fixed set of objects, ignoring the query: the
// surface's filter is applied by the surface's own helper before paging, which
// is what a real provider does.
type scopeTestProvider struct{ objs []object.Object }

func (p *scopeTestProvider) List(q object.Query) ([]object.Object, string, error) {
	// A real provider applies the declared filter before paging.
	return filterObjects(p.objs, q.Filter), "", nil
}

// ignoringProvider is a provider that does NOT apply the filter, to prove the
// surface's backstop.
type ignoringProvider struct{ objs []object.Object }

func (p *ignoringProvider) List(object.Query) ([]object.Object, string, error) {
	return p.objs, "", nil
}
func (p *ignoringProvider) Get(id string) (object.Object, error) {
	for _, o := range p.objs {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}
func (p *ignoringProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

func (p *scopeTestProvider) Get(id string) (object.Object, error) {
	for _, o := range p.objs {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (p *scopeTestProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

const TypeScopedThing = "scoped_thing"

// scopedSurface builds a surface over one scoped type, acting as alice.
func scopedSurface(t *testing.T) *objectSurface {
	t.Helper()
	types := newTypeCollection()
	reg := TypeRegistration{
		Type: object.Type{
			Name: TypeScopedThing, Label: "Scoped thing", ID: "id",
			Fields: []object.Field{
				{Name: "id", Kind: object.KindString},
				{Name: "origin", Kind: object.KindString},
				{Name: "title", Kind: object.KindString},
			},
			Scope: &object.Scope{Field: "origin"},
		},
		Provider: &scopeTestProvider{objs: []object.Object{
			{Type: TypeScopedThing, ID: "t-alice", Fields: map[string]any{"id": "t-alice", "origin": "user:alice", "title": "hers"}},
			{Type: TypeScopedThing, ID: "t-bob", Fields: map[string]any{"id": "t-bob", "origin": "user:bob", "title": "his"}},
		}},
	}
	if err := types.Add(reg); err != nil {
		t.Fatalf("Types.Add: %v", err)
	}
	return newObjectSurface(types, operatorGate(), consoleActor(principal.MustParse("user:alice")))
}

// TestScopeFiltersAListToTheActor is the core rule: a scoped type shows a
// principal only the rows whose scoped field is its own reference.
func TestScopeFiltersAListToTheActor(t *testing.T) {
	s := scopedSurface(t)

	alice := s.Scope(consoleActor(principal.MustParse("user:alice")))
	objs, _, err := alice.List(TypeScopedThing, object.Query{})
	if err != nil {
		t.Fatalf("alice List: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "t-alice" {
		t.Fatalf("alice sees %v, want only her row", objs)
	}

	bob := s.Scope(consoleActor(principal.MustParse("user:bob")))
	objs, _, err = bob.List(TypeScopedThing, object.Query{})
	if err != nil {
		t.Fatalf("bob List: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "t-bob" {
		t.Fatalf("bob sees %v, want only his row", objs)
	}
}

// TestScopeHidesAnObjectFromGet pins the by-id path: a row the scope excludes
// is reported as ABSENT, not refused, so a scoped principal cannot learn
// whether an id exists.
func TestScopeHidesAnObjectFromGet(t *testing.T) {
	s := scopedSurface(t)
	alice := s.Scope(consoleActor(principal.MustParse("user:alice")))

	if _, err := alice.Get(TypeScopedThing, "t-bob"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("alice Get(bob's row) = %v, want ErrNotFound", err)
	}
	if _, err := alice.Get(TypeScopedThing, "t-alice"); err != nil {
		t.Fatalf("alice Get(her row) = %v, want it", err)
	}
}

// TestScopeWinsOverTheCallersFilter is "a caller may narrow but never widen":
// a caller naming the scoped field cannot reach another principal's row.
func TestScopeWinsOverTheCallersFilter(t *testing.T) {
	s := scopedSurface(t)
	alice := s.Scope(consoleActor(principal.MustParse("user:alice")))

	objs, _, err := alice.List(TypeScopedThing, object.Query{Filter: map[string]any{"origin": "user:bob"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, o := range objs {
		if o.ID == "t-bob" {
			t.Fatalf("the caller widened the scope: %v", objs)
		}
	}
}

// TestScopeRefusesWithoutAPrincipal pins the fail-closed direction: a scoped
// type with no verified actor yields nothing.
func TestScopeRefusesWithoutAPrincipal(t *testing.T) {
	s := scopedSurface(t)
	s.actor = consoleActor(principal.Ref{})
	if _, _, err := s.List(TypeScopedThing, object.Query{}); !errors.Is(err, portal.ErrUnauthorized) {
		t.Fatalf("an unscoped actor = %v, want a refusal", err)
	}
}

// TestFilterObjectsAppliesEqualityAndIn pins the provider-side filter helper.
func TestFilterObjectsAppliesEqualityAndIn(t *testing.T) {
	objs := []object.Object{
		{ID: "a", Fields: map[string]any{"origin": "user:alice", "priority": 2}},
		{ID: "b", Fields: map[string]any{"origin": "user:bob", "priority": 4}},
	}
	if got := filterObjects(objs, map[string]any{"origin": "user:alice"}); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("equality filter = %v, want just a", got)
	}
	if got := filterObjects(objs, map[string]any{"priority": 4}); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("numeric filter = %v, want just b", got)
	}
	got := filterObjects(objs, map[string]any{"origin": map[string]any{"$in": []any{"user:bob"}}})
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("$in filter = %v, want just b", got)
	}
	if got := filterObjects(objs, nil); len(got) != 2 {
		t.Errorf("no filter = %v, want both", got)
	}
}

// TestThoughtOriginIsTheEffectivePrincipal pins the exposure the scope depends
// on: a thought with no recorded origin is shown as the navi's own service
// principal, so a scope on `origin` matches system-originated work instead of
// hiding it from the navi that did it.
func TestThoughtOriginIsTheEffectivePrincipal(t *testing.T) {
	n, s := objectNavi(t)
	if err := n.Thoughts.Enqueue(&thought.Thought{ID: "t1", Prompt: "p", Status: thought.ThoughtPending}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	obj, err := s.Get(TypeThought, "t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := n.serviceRef().String(); obj.Fields["origin"] != want {
		t.Errorf("origin = %v, want the service principal %s", obj.Fields["origin"], want)
	}
}

// TestScopeIsABackstopAgainstAProviderThatIgnoresFilters pins the fail-closed
// direction: even a provider that drops the query still cannot return another
// principal's row, because the surface enforces the scope on the way out.
func TestScopeIsABackstopAgainstAProviderThatIgnoresFilters(t *testing.T) {
	types := newTypeCollection()
	reg := TypeRegistration{
		Type: object.Type{
			Name: TypeScopedThing, ID: "id",
			Fields: []object.Field{
				{Name: "id", Kind: object.KindString},
				{Name: "origin", Kind: object.KindString},
			},
			Scope: &object.Scope{Field: "origin"},
		},
		Provider: &ignoringProvider{objs: []object.Object{
			{Type: TypeScopedThing, ID: "t-alice", Fields: map[string]any{"id": "t-alice", "origin": "user:alice"}},
			{Type: TypeScopedThing, ID: "t-bob", Fields: map[string]any{"id": "t-bob", "origin": "user:bob"}},
		}},
	}
	if err := types.Add(reg); err != nil {
		t.Fatal(err)
	}
	s := newObjectSurface(types, operatorGate(), consoleActor(principal.MustParse("user:alice")))
	objs, _, err := s.List(TypeScopedThing, object.Query{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "t-alice" {
		t.Fatalf("a filter-ignoring provider leaked rows: %v", objs)
	}
}
