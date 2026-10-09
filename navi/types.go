package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
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
	// Act performs the type's declared actions. It is optional: a type whose
	// actions are declared but not yet served reports ErrUnsupported, and a
	// type with no actions needs no handler.
	Act ActionHandler
}

// ActionHandler applies one declared action to an object. It is the write
// counterpart of Provider: the surface resolves the type, authorizes the
// caller, and validates the arguments against the action's schema, then
// delegates the mutation here. The surface publishes the resulting delta.
type ActionHandler func(id, action string, args map[string]any) error

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

	// auth and identity are the authorization path for actions (ADR 0017
	// decision 4): the same portal.Authorizer a control portal uses, consulted
	// with the identity the navi declares the surface acts as. A nil
	// authorizer refuses every action — fail closed.
	auth     portal.Authorizer
	identity string

	// deltas is the bounded, cursor-ed live-change window Watch reads
	// (ADR 0017 decision 6). Producers push translated queue, agent, action,
	// and memory changes onto it. It is exported from object so the gateway's
	// stream binding uses the same discipline as the in-process surface.
	deltas *object.DeltaWindow
}

var _ object.Surface = (*objectSurface)(nil)
var _ object.Notifier = (*objectSurface)(nil)

func newObjectSurface(types *TypeCollection, auth portal.Authorizer, identity string) *objectSurface {
	return &objectSurface{
		types:    types,
		auth:     auth,
		identity: identity,
		deltas:   object.NewDeltaWindow(0),
	}
}

// Types returns every exposed type descriptor, in declaration order. The
// in-process collection cannot fail, so the error is always nil; it exists
// so a stream binding can report a mid-flight failure (ADR 0019 decision 8).
func (s *objectSurface) Types() ([]object.Type, error) {
	regs := s.types.All()
	out := make([]object.Type, 0, len(regs))
	for _, r := range regs {
		out = append(out, r.Type)
	}
	return out, nil
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

// Act performs a declared action on one object (ADR 0017 decision 4): the
// type and action must be declared, the caller must be authorized (fail
// closed), and the arguments must satisfy the action's schema before the
// registered handler applies the mutation. The resulting change is published
// as an object delta.
func (s *objectSurface) Act(typeName, id, action string, args map[string]any) error {
	reg, err := s.registration(typeName)
	if err != nil {
		return err
	}
	a, ok := reg.Type.Action(action)
	if !ok {
		return fmt.Errorf("%w: %s.%s", object.ErrUnknownAction, typeName, action)
	}
	// Authorization precedes validation: an unauthorized caller learns
	// nothing about the arguments an action expects.
	if err := s.authorize(action); err != nil {
		return err
	}
	if err := validateActionArgs(a, args); err != nil {
		return err
	}
	if reg.Act == nil {
		return fmt.Errorf("%w: %s.%s", object.ErrUnsupported, typeName, action)
	}
	if err := reg.Act(id, action, args); err != nil {
		return err
	}
	s.emitObject(typeName, id, timeNow())
	return nil
}

// authorize consults the navi's authorizer for one action. No authorizer is
// wired means no action is permitted (fail closed) — the same discipline
// portal.Control follows.
func (s *objectSurface) authorize(action string) error {
	if s.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", portal.ErrUnauthorized, action)
	}
	return s.auth.Authorize(portal.Action(action), s.identity)
}

// Watch returns the object deltas after a cursor, the next cursor to use,
// and whether deltas before the cursor were dropped (a slow consumer fell
// behind the bounded window). The cursor is the mailbox sequence, so a
// portal resumes without gaps and learns when it has a gap (ADR 0017
// decision 6).
func (s *objectSurface) Watch(after int) ([]object.Delta, int, bool, error) {
	deltas, next, lost := s.deltas.ReadAfter(after)
	return deltas, next, lost, nil
}

// Notify implements object.Notifier: a stream binding is woken when a delta
// lands instead of polling Watch on a timer (ADR 0019 decision 5).
func (s *objectSurface) Notify() <-chan struct{} {
	return s.deltas.Notify()
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
