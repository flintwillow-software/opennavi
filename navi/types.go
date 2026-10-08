package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/object"
)

// TypeRegistration pairs an exposed object type's descriptor with the
// provider that serves its objects (ADR 0017). The navi registers the types
// it exposes — like tools, models, and skills — so what a portal can see is
// visible in main.go.
//
// The navi's own aggregates (thought, todo, note, memory) are registered for
// the author; a component's type is registered by the navi that wires it,
// with the provider reading through the same view the component was bound to
// (ADR 0017 decision 8).
type TypeRegistration struct {
	// Type is the descriptor.
	Type object.Type
	// Provider serves the type's objects.
	Provider object.Provider
}

// TypeCollection is the navi's exposed-type registry: a collection of
// descriptors, validated on Add like every other collection.
type TypeCollection struct {
	*Collection[TypeRegistration]
}

func newTypeCollection() *TypeCollection {
	return &TypeCollection{Collection: newCollection("type",
		func(r TypeRegistration) string { return r.Type.Name },
		validateTypeRegistration)}
}

func validateTypeRegistration(r TypeRegistration) error {
	if err := r.Type.Validate(); err != nil {
		return err
	}
	if r.Provider == nil {
		return fmt.Errorf("object: type %q requires a provider", r.Type.Name)
	}
	return nil
}

// ── the surface ─────────────────────────────────────────────────────────

// objectSurface is the navi's object.Surface: it resolves a type name to its
// registered provider and enforces the descriptor's field visibility, so a
// provider may return every field and the surface still omits what the
// descriptor marks sensitive — nothing is exposed by default (ADR 0017
// decision 5).
type objectSurface struct {
	types *TypeCollection
}

var _ object.Surface = (*objectSurface)(nil)

func newObjectSurface(types *TypeCollection) *objectSurface {
	return &objectSurface{types: types}
}

// Types returns every exposed type descriptor, in declaration order.
func (s *objectSurface) Types() []object.Type {
	regs := s.types.All()
	out := make([]object.Type, 0, len(regs))
	for _, r := range regs {
		out = append(out, r.Type)
	}
	return out
}

// List returns a bounded page of one type's objects.
func (s *objectSurface) List(typeName string, q object.Query) ([]object.Object, string, error) {
	reg, err := s.registration(typeName)
	if err != nil {
		return nil, "", err
	}
	objs, next, err := reg.Provider.List(q)
	if err != nil {
		return nil, "", err
	}
	return projectAll(reg.Type, objs), next, nil
}

// Get returns one object of one type.
func (s *objectSurface) Get(typeName, id string) (object.Object, error) {
	reg, err := s.registration(typeName)
	if err != nil {
		return object.Object{}, err
	}
	obj, err := reg.Provider.Get(id)
	if err != nil {
		return object.Object{}, err
	}
	return project(reg.Type, obj), nil
}

// Detail returns a bounded page of a named detail collection's objects,
// projected against the detail type's descriptor when one is exposed.
func (s *objectSurface) Detail(typeName, id, detail string, q object.Query) ([]object.Object, string, error) {
	reg, err := s.registration(typeName)
	if err != nil {
		return nil, "", err
	}
	d, ok := reg.Type.Detail(detail)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s.%s", object.ErrUnknownDetail, typeName, detail)
	}
	objs, next, err := reg.Provider.Detail(id, detail, q)
	if err != nil {
		return nil, "", err
	}
	if dt, ok := s.types.Get(d.Type); ok {
		return projectAll(dt.Type, objs), next, nil
	}
	return objs, next, nil
}

// Act performs a declared action. Actions and live deltas arrive with
// NAVI-387; until then a declared action is recognised and refused with
// ErrUnsupported rather than reported as unknown.
func (s *objectSurface) Act(typeName, id, action string, args map[string]any) error {
	reg, err := s.registration(typeName)
	if err != nil {
		return err
	}
	if _, ok := reg.Type.Action(action); !ok {
		return fmt.Errorf("%w: %s.%s", object.ErrUnknownAction, typeName, action)
	}
	return object.ErrUnsupported
}

// Watch returns the object deltas after a cursor. Deltas arrive with
// NAVI-387; until then the surface reports no changes.
func (s *objectSurface) Watch(after int) ([]object.Delta, int, bool) {
	return nil, after, false
}

func (s *objectSurface) registration(typeName string) (TypeRegistration, error) {
	reg, ok := s.types.Get(typeName)
	if !ok {
		return TypeRegistration{}, fmt.Errorf("%w: %s", object.ErrUnknownType, typeName)
	}
	return reg, nil
}

// project strips fields the descriptor does not expose: sensitive fields are
// omitted entirely (absent, never null), and undeclared fields are dropped so
// a provider cannot widen the surface past its descriptor.
func project(t object.Type, o object.Object) object.Object {
	out := object.Object{Type: t.Name, ID: o.ID, Version: o.Version, Fields: map[string]any{}}
	for _, f := range t.Fields {
		if f.Sensitive {
			continue
		}
		if v, ok := o.Fields[f.Name]; ok {
			out.Fields[f.Name] = v
		}
	}
	return out
}

func projectAll(t object.Type, objs []object.Object) []object.Object {
	out := make([]object.Object, 0, len(objs))
	for _, o := range objs {
		out = append(out, project(t, o))
	}
	return out
}
