package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
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

// objectGate decides one object-surface capability for an actor (ADR 0022
// decisions 1, 5 and 7): a read of a type, or a declared action on it. The
// navi implements it over the compiled policy, so the object path and the tool
// path ask the same engine about the same principal; an explicit
// portal.Authorizer wired with WithObjectAuthorizer is adapted to it. A nil
// gate refuses every read and action — fail closed.
type objectGate interface {
	// authorizeCapability returns nil when actor may do cap, or a refusal.
	authorizeCapability(actor portal.Actor, cap Capability) error
	// permitsCapability reports whether actor may do cap WITHOUT failing the
	// whole operation. Projection asks it about one sensitive field, where a
	// denial means "omit the field", not "refuse the read".
	permitsCapability(actor portal.Actor, cap Capability) bool
}

// authorizerGate adapts an explicit portal.Authorizer (WithObjectAuthorizer)
// to the object gate. The capability's canonical string is what the authorizer
// sees as its action, so an AllowAll dev default and a refusing test
// authorizer both keep working.
type authorizerGate struct{ auth portal.Authorizer }

func (g authorizerGate) authorizeCapability(actor portal.Actor, cap Capability) error {
	if g.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", portal.ErrUnauthorized, cap)
	}
	return g.auth.Authorize(portal.Action(cap.String()), actor)
}

func (g authorizerGate) permitsCapability(actor portal.Actor, cap Capability) bool {
	if g.auth == nil {
		return false
	}
	return g.auth.Authorize(portal.Action(cap.String()), actor) == nil
}

// objectSurface is the navi's object.Surface: it resolves a type name to its
// registered provider and enforces the descriptor's field visibility, so a
// provider may return every field and the surface still omits what the
// descriptor marks sensitive — nothing is exposed by default (ADR 0017
// decision 5).
type objectSurface struct {
	types *TypeCollection

	// gate is the authorization path for reads and actions (ADR 0017 decision
	// 4; ADR 0022 decisions 1, 5 and 7): the compiled policy by default, or an
	// explicit authorizer. actor is who the surface acts as — the in-process
	// console by default (ADR 0021 decision 9); a network binding re-scopes the
	// surface to its verified actor before a read or action.
	gate  objectGate
	actor portal.Actor

	// deltas is the bounded, cursor-ed live-change window Watch reads
	// (ADR 0017 decision 6). Producers push translated queue, agent, action,
	// and memory changes onto it. It is exported from object so the gateway's
	// stream binding uses the same discipline as the in-process surface.
	deltas *object.DeltaWindow
}

var _ object.Surface = (*objectSurface)(nil)
var _ object.Notifier = (*objectSurface)(nil)
var _ portal.Scoper = (*objectSurface)(nil)

func newObjectSurface(types *TypeCollection, gate objectGate, actor portal.Actor) *objectSurface {
	return &objectSurface{
		types:  types,
		gate:   gate,
		actor:  actor,
		deltas: object.NewDeltaWindow(0),
	}
}

// consoleActor is the actor an in-process portal acts as: the console — the
// terminal the operator is sitting at (ADR 0021 decision 9). Its facts are the
// in-process transport and the console channel, which is what makes a policy
// able to distinguish it from an ambient input embedded the same way.
func consoleActor(who principal.Ref) portal.Actor {
	return portal.Actor{
		Who:  who,
		Info: portal.RequestInfo{Transport: "in-process", Channel: "console"},
	}
}

// Scope implements portal.Scoper (ADR 0022 decision 1): a binding that
// verified an actor re-scopes the surface to it, so the object read and action
// paths authorize as the connection's principal rather than as one declared
// principal. The returned surface shares the type registry and the delta
// window, so the live path is one stream whatever the actor.
func (s *objectSurface) Scope(actor portal.Actor) object.Surface {
	scoped := *s
	scoped.actor = actor
	return &scoped
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

// List returns a bounded page of one type's objects, as the surface's actor.
func (s *objectSurface) List(typeName string, q object.Query) ([]object.Object, string, error) {
	reg, err := s.registration(typeName)
	if err != nil {
		return nil, "", err
	}
	if err := s.authorizeCap(Read(typeName)); err != nil {
		return nil, "", err
	}
	objs, next, err := reg.Provider.List(q)
	if err != nil {
		return nil, "", err
	}
	return projectAll(reg.Type, objs, s.allowedSensitive(reg.Type)), next, nil
}

// Get returns one object of one type, as the surface's actor.
func (s *objectSurface) Get(typeName, id string) (object.Object, error) {
	reg, err := s.registration(typeName)
	if err != nil {
		return object.Object{}, err
	}
	if err := s.authorizeCap(Read(typeName)); err != nil {
		return object.Object{}, err
	}
	obj, err := reg.Provider.Get(id)
	if err != nil {
		return object.Object{}, err
	}
	return project(reg.Type, obj, s.allowedSensitive(reg.Type)), nil
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
	// A detail read is a read of the detail's own type, so a grant for the
	// parent type does not cover it — the referential rule is per target.
	if err := s.authorizeCap(Read(d.Type)); err != nil {
		return nil, "", err
	}
	objs, next, err := reg.Provider.Detail(id, detail, q)
	if err != nil {
		return nil, "", err
	}
	if dt, ok := s.types.Get(d.Type); ok {
		return projectAll(dt.Type, objs, s.allowedSensitive(dt.Type)), next, nil
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
	if err := s.authorizeCap(Act(typeName, action)); err != nil {
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

// authorizeCap consults the object gate for one capability, as the surface's
// actor. A nil gate means nothing is permitted (fail closed) — the same
// discipline portal.Control follows. A zero actor is refused too: an action
// with no verified principal never defaults open (ADR 0022 decision 9).
func (s *objectSurface) authorizeCap(cap Capability) error {
	if s.gate == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", portal.ErrUnauthorized, cap)
	}
	if s.actor.IsZero() {
		return fmt.Errorf("%w: %s (no verified principal — fail closed)", portal.ErrUnauthorized, cap)
	}
	return s.gate.authorizeCapability(s.actor, cap)
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

// allowedSensitive returns the sensitive fields of t the surface's actor may
// see (ADR 0022 decision 7): a sensitive field is one a principal may see only
// with a grant, not one the surface removes for everyone. The descriptor's
// Sensitive flag is the declaration that a grant is required; the grant is
// read:<type>.<field>, which a read:<type> grant deliberately does not cover.
// A nil set means no sensitive field is visible.
func (s *objectSurface) allowedSensitive(t object.Type) map[string]bool {
	if s.gate == nil || s.actor.IsZero() {
		return nil
	}
	var allowed map[string]bool
	for _, f := range t.Fields {
		if !f.Sensitive {
			continue
		}
		if s.gate.permitsCapability(s.actor, ReadField(t.Name, f.Name)) {
			if allowed == nil {
				allowed = map[string]bool{}
			}
			allowed[f.Name] = true
		}
	}
	return allowed
}

// project strips fields the descriptor does not expose: undeclared fields are
// dropped so a provider cannot widen the surface past its descriptor, and a
// sensitive field is included only when allowedSensitive names it (ADR 0017
// decision 5; ADR 0022 decision 7). A nil set excludes every sensitive field —
// what the shared delta window uses, because a delta is read by every
// connection and must not carry a field one of them may not see.
func project(t object.Type, o object.Object, allowedSensitive map[string]bool) object.Object {
	out := object.Object{Type: t.Name, ID: o.ID, Version: o.Version, Fields: map[string]any{}}
	for _, f := range t.Fields {
		if f.Sensitive && !allowedSensitive[f.Name] {
			continue
		}
		if v, ok := o.Fields[f.Name]; ok {
			out.Fields[f.Name] = v
		}
	}
	return out
}

func projectAll(t object.Type, objs []object.Object, allowedSensitive map[string]bool) []object.Object {
	out := make([]object.Object, 0, len(objs))
	for _, o := range objs {
		out = append(out, project(t, o, allowedSensitive))
	}
	return out
}
