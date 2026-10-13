package navi

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the compiled policy of ADR 0021 decisions 3, 5 and 11: roles and
// groups, and the evaluation that decides a capability request.
//
// The layering is the design. A role is a TRUST policy plus a PERMISSION policy
// (decision 5): who may hold it, and what holding it permits. Identity data —
// which principals exist, their group membership, their claims — comes from a
// trust source at runtime (decision 4) and never from here, so a group declares
// what its members may DO and never who its members ARE.
//
// Evaluation intersects every layer against the SAME request: the compiled
// ceiling, the principal's roles and groups, and a session policy when one
// applies. Conditions therefore compose by conjunction, and a bare layer can
// never widen a conditioned one — the meet is the mechanism, not a comparison
// somebody has to get right (decision 11; ADR 0022 decisions 4 and 6).

// TrustPolicy is the trust side of a role (ADR 0021 decision 5): who may hold
// it. It names principals and groups directly and may require claims.
//
// Group MEMBERSHIP is deliberately absent: it comes from the trust source
// (ADR 0021 decision 4) and arrives on the resolved principal, so this names a
// group as a policy subject and the source decides who is in it. A compiled
// membership list would be a second, conflicting answer to the same question.
type TrustPolicy struct {
	// Principals may hold the role directly.
	Principals []principal.Ref
	// Groups: a principal the trust source places in one of these groups may
	// hold the role.
	Groups []principal.Ref
	// Claims: a principal must carry every listed claim to hold the role. A
	// claim is not a role (ADR 0021 decisions 2 and 5) — this reads a fact the
	// identity provider asserted, it does not mint a capability.
	Claims map[string]string
}

// Holds reports whether the principal may hold a role carrying this trust
// policy: named directly, placed by the trust source in a named group, or
// carrying every required claim.
func (t TrustPolicy) Holds(who *Principal) bool {
	if who == nil {
		return false
	}
	for _, ref := range t.Principals {
		if who.Ref() == ref {
			return true
		}
	}
	for _, group := range t.Groups {
		if who.InGroup(group) {
			return true
		}
	}
	if len(t.Claims) == 0 {
		return false
	}
	claims := who.Claims()
	for name, want := range t.Claims {
		if claims[name] != want {
			return false
		}
	}
	return true
}

// Validate checks every reference and that at least one holder is named — a
// trust policy nobody can satisfy is a role nobody can hold, which is far more
// likely a mistake than an intent.
func (t TrustPolicy) Validate() error {
	for _, ref := range t.Principals {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: trust policy principal: %w", ErrInvalid, err)
		}
	}
	for _, ref := range t.Groups {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: trust policy group: %w", ErrInvalid, err)
		}
		if ref.Kind() != principal.KindGroup {
			return fmt.Errorf("%w: trust policy names %s, which is not a group", ErrInvalid, ref)
		}
	}
	if len(t.Principals) == 0 && len(t.Groups) == 0 && len(t.Claims) == 0 {
		return fmt.Errorf("%w: trust policy names no holder", ErrInvalid)
	}
	return nil
}

// String renders a trust policy for diagnostics.
func (t TrustPolicy) String() string {
	var parts []string
	for _, ref := range t.Principals {
		parts = append(parts, ref.String())
	}
	for _, ref := range t.Groups {
		parts = append(parts, "in "+ref.String())
	}
	for name, value := range t.Claims {
		parts = append(parts, "claim "+name+"="+value)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Role is a compiled capability set split into its two sides (ADR 0021
// decisions 2, 3 and 5): a trust policy saying who may hold it, and a
// permission policy saying what holding it permits. It is NOT a principal: it
// holds no identity and no authenticators, and a trust source never resolves
// one.
type Role struct {
	// Name is the role's identifier, as a navi author writes it.
	Name string
	// Holds is the trust side: who may hold the role.
	Holds TrustPolicy
	// Grants is the permission side: what holding it permits.
	Grants CapabilitySet
}

// NewRole names a compiled role and gives it its two policies.
func NewRole(name string, holds TrustPolicy, grants CapabilitySet) (Role, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return Role{}, fmt.Errorf("%w: role has an empty name", ErrInvalidRole)
	}
	if err := holds.Validate(); err != nil {
		return Role{}, err
	}
	if err := grants.Validate(); err != nil {
		return Role{}, err
	}
	return Role{Name: n, Holds: holds, Grants: grants}, nil
}

// IsZero reports whether r names no role.
func (r Role) IsZero() bool { return strings.TrimSpace(r.Name) == "" }

// Group is the PERMISSION side of a named collection of principals (ADR 0021
// decisions 2 and 5): the capabilities its members hold.
//
// Membership is the TRUST side and comes from the trust source — a resolved
// principal carries the groups it belongs to (Principal.Groups) — so a group
// never declares members here. A compiled membership list would be a second,
// conflicting source of the same fact (ADR 0021 decision 4).
//
// A group may also be authenticatable itself: a shared identity with its own
// keys, which attributes an action to the group rather than to a member —
// weaker attribution, accepted rather than hidden (decision 2).
type Group struct {
	ref    principal.Ref
	grants CapabilitySet

	shared *Principal
}

// NewGroup builds the permission side of a group from its group-kind reference
// and the capabilities its members hold.
func NewGroup(ref principal.Ref, grants CapabilitySet) (*Group, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	if ref.Kind() != principal.KindGroup {
		return nil, fmt.Errorf("%w: %s is not a group reference", ErrInvalidPrincipal, ref)
	}
	if err := grants.Validate(); err != nil {
		return nil, err
	}
	return &Group{ref: ref, grants: grants}, nil
}

// Ref returns the group's reference.
func (g *Group) Ref() principal.Ref { return g.ref }

// Grants returns the capabilities the group's members hold.
func (g *Group) Grants() CapabilitySet { return g.grants }

// Shared returns the group's own authenticatable identity, or nil when the
// group is a policy subject only (ADR 0021 decision 2).
func (g *Group) Shared() *Principal { return g.shared }

// SetShared makes the group authenticatable itself — a shared identity with
// its own keys — and returns that principal. The group's reference is the
// shared principal's identity, so an action authenticated through it
// attributes to the group.
func (g *Group) SetShared(keys ...Authenticator) (*Principal, error) {
	p, err := NewPrincipal(g.ref, keys...)
	if err != nil {
		return nil, err
	}
	g.shared = p
	return p, nil
}

// ── evaluation ──────────────────────────────────────────────────────────

// AccessRequest is one capability question (ADR 0022 decision 1: every
// enforcement point asks the same engine about the same principal).
type AccessRequest struct {
	// Principal is the verified acting principal.
	Principal *Principal
	// Capability is what it wants to do.
	Capability Capability
	// Context carries the request facts conditions are evaluated against.
	Context RequestContext
	// Session, when non-nil, is a further narrowing layer — the mode chosen at
	// enqueue (ADR 0022 decision 4). It can never widen, because every layer
	// must permit.
	Session *CapabilitySet
}

// Decision is the outcome of an evaluation, with the reason a refusal can be
// reported loudly rather than looking like nothing happened.
type Decision struct {
	// Allowed is the answer.
	Allowed bool
	// Reason explains it, for the refusal and the audit record.
	Reason string
	// Matched names the role or group that granted or denied it.
	Matched string
}

// Policy is the navi's compiled authorization policy (ADR 0021 decisions 3, 5
// and 11): the roles and groups a navi declares, checked against the ceiling
// derived from what the navi declares. It is built at assembly and immutable
// afterwards.
type Policy struct {
	roles   []Role
	groups  []Group
	ceiling CapabilitySet
}

// NewPolicy compiles a policy. The ceiling is the navi's derived capability
// set; every role and group grant must be inside it, which is the intersection
// rule enforced once, at assembly, rather than per request.
func NewPolicy(ceiling CapabilitySet, roles []Role, groups []Group) (*Policy, error) {
	if err := ceiling.Validate(); err != nil {
		return nil, fmt.Errorf("%w: ceiling: %w", ErrInvalid, err)
	}
	seenRoles := map[string]bool{}
	for _, r := range roles {
		if r.IsZero() {
			return nil, fmt.Errorf("%w: unnamed role", ErrInvalid)
		}
		if seenRoles[r.Name] {
			return nil, fmt.Errorf("%w: role %q is declared twice", ErrDuplicate, r.Name)
		}
		seenRoles[r.Name] = true
		if err := r.Holds.Validate(); err != nil {
			return nil, err
		}
		if err := r.Grants.Validate(); err != nil {
			return nil, err
		}
		if err := r.Grants.Exceeds(ceiling); err != nil {
			return nil, fmt.Errorf("role %q: %w", r.Name, err)
		}
	}
	seenGroups := map[principal.Ref]bool{}
	for _, g := range groups {
		if err := g.Ref().Validate(); err != nil {
			return nil, fmt.Errorf("%w: unnamed group", ErrInvalid)
		}
		if seenGroups[g.Ref()] {
			return nil, fmt.Errorf("%w: group %s is declared twice", ErrDuplicate, g.Ref())
		}
		seenGroups[g.Ref()] = true
		if err := g.Grants().Validate(); err != nil {
			return nil, err
		}
		if err := g.Grants().Exceeds(ceiling); err != nil {
			return nil, fmt.Errorf("group %s: %w", g.Ref(), err)
		}
	}
	return &Policy{roles: roles, groups: groups, ceiling: ceiling}, nil
}

// Roles returns the compiled roles.
func (p *Policy) Roles() []Role { return append([]Role(nil), p.roles...) }

// Groups returns the compiled groups.
func (p *Policy) Groups() []Group { return append([]Group(nil), p.groups...) }

// Ceiling returns the compiled capability ceiling.
func (p *Policy) Ceiling() CapabilitySet { return p.ceiling }

// grant is one applicable permission layer, named for the decision's reason.
type grant struct {
	name string
	set  CapabilitySet
}

// Decide evaluates a request (ADR 0021 decision 11; ADR 0022 decision 6):
// default deny; an explicit deny wins; an allow is required; the result is
// intersected with the compiled ceiling and with a session policy when one
// applies; every condition must pass.
//
// The role and group layers UNION — a principal holds every capability its
// roles and groups grant — while the ceiling and the session policy are
// REQUIRED layers that every request must also satisfy. That asymmetry is the
// model: grants accumulate, and only a deny or a narrowing layer restricts. It
// is also why a bare session can never widen a conditioned role — the role
// still has to permit, so the meet is the narrower condition.
func (p *Policy) Decide(req AccessRequest) Decision {
	ctx := req.Context
	ctx.Principal = req.Principal

	// The ceiling is the outer bound: a capability outside it is unreachable
	// whatever any policy says (ADR 0006, ADR 0021 decision 11).
	if !p.ceiling.Permits(req.Capability, ctx) {
		return Decision{Reason: fmt.Sprintf("%s is outside the compiled ceiling", req.Capability)}
	}

	layers := p.applicable(req.Principal)

	// Deny wins, across every granting layer and the session policy.
	for _, l := range layers {
		if l.set.Denies(req.Capability, ctx) {
			return Decision{Reason: fmt.Sprintf("%s is explicitly denied by %s", req.Capability, l.name), Matched: l.name}
		}
	}
	if req.Session != nil && req.Session.Denies(req.Capability, ctx) {
		return Decision{Reason: fmt.Sprintf("%s is explicitly denied by the session policy", req.Capability), Matched: "session"}
	}

	// An allow is required, from the union of the principal's roles and groups.
	matched := ""
	for _, l := range layers {
		if l.set.Permits(req.Capability, ctx) {
			matched = l.name
			break
		}
	}
	if matched == "" {
		if len(layers) == 0 {
			return Decision{Reason: fmt.Sprintf("no role or group applies to %s", principalOrNone(req.Principal))}
		}
		return Decision{Reason: fmt.Sprintf("no role or group grants %s", req.Capability)}
	}

	// A session policy is a narrowing layer: it must permit too, so it can
	// never widen what the roles and groups granted (ADR 0022 decision 4).
	if req.Session != nil && !req.Session.Permits(req.Capability, ctx) {
		return Decision{Reason: fmt.Sprintf("the session policy does not permit %s", req.Capability), Matched: "session"}
	}
	return Decision{Allowed: true, Matched: matched}
}

// applicable returns the permission layers a principal holds: one per role it
// may hold and one per group the trust source placed it in.
func (p *Policy) applicable(who *Principal) []grant {
	if who == nil {
		return nil
	}
	var out []grant
	for _, r := range p.roles {
		if r.Holds.Holds(who) {
			out = append(out, grant{name: "role:" + r.Name, set: r.Grants})
		}
	}
	for _, g := range p.groups {
		if who.InGroup(g.Ref()) {
			out = append(out, grant{name: "group:" + g.Ref().String(), set: g.Grants()})
		}
	}
	return out
}

func principalOrNone(who *Principal) string {
	if who == nil {
		return "an unauthenticated request"
	}
	return who.Ref().String()
}

// Denies reports whether the set explicitly denies c for this request. Deny
// wins across layers, so an evaluation asks this before it asks Permits.
func (s CapabilitySet) Denies(c Capability, ctx RequestContext) bool {
	for _, d := range s.Deny {
		if d.applies(c, ctx) {
			return true
		}
	}
	return false
}

// ── the derived ceiling ─────────────────────────────────────────────────

// CeilingFromDeclarations derives the capability ceiling from what a navi
// actually declares (ADR 0021 decision 11): every declared tool is call:<tool>,
// every exposed type is read:<type> and its fields and act:<type>.<action>.
//
// The ceiling is DERIVED, not declared, and that is what makes the intersection
// well defined: it is unconditioned by construction, so no policy can be wider
// than it and no condition comparison is needed to prove it. A capability
// naming something undeclared is outside the ceiling — which is exactly the
// referential rule, enforced by construction rather than by a list somebody
// maintains.
func CeilingFromDeclarations(types []object.Type, toolNames []string) CapabilitySet {
	allow := make([]Grant, 0, len(types)*2+len(toolNames))
	for _, name := range toolNames {
		allow = append(allow, Grant{Capability: Call(name)})
	}
	for _, t := range types {
		allow = append(allow, Grant{Capability: Read(t.Name)})
		for _, f := range t.Fields {
			allow = append(allow, Grant{Capability: ReadField(t.Name, f.Name)})
		}
		for _, a := range t.Actions {
			allow = append(allow, Grant{Capability: Act(t.Name, a.Name)})
		}
	}
	// The two target-less verbs are always part of the ceiling: every navi
	// accepts connections and schedules work.
	allow = append(allow, Grant{Capability: Connect()}, Grant{Capability: Enqueue()})
	return CapabilitySet{Allow: allow}
}
