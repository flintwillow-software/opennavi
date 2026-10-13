package navi

import (
	"fmt"
	"strings"
)

// This file is the capability vocabulary of ADR 0021 decision 11: a *closed*
// set of verbs over *referential* targets. A capability names something that
// already exists — a tool name, a type name, a field name, an action name —
// rather than introducing a parallel namespace, so what a policy can say is
// bounded by what the navi declares, and `Run` can check that every declared
// capability names something real.
//
// The vocabulary is core navi, not a component contract: a portal never sees a
// verb or a target (ADR 0021, "Consequences"; ADR 0022 decision 8). The navi
// asks the engine on the portal's behalf and answers with allow or refuse.

// Verb is the closed set of capability verbs (ADR 0021 decision 11).
type Verb string

const (
	// VerbConnect authorizes a portal attachment: the connection itself.
	VerbConnect Verb = "connect"
	// VerbRead authorizes reading a type, or one of its fields.
	VerbRead Verb = "read"
	// VerbAct authorizes performing a declared action on a type.
	VerbAct Verb = "act"
	// VerbCall authorizes calling a declared tool.
	VerbCall Verb = "call"
	// VerbEnqueue authorizes scheduling a thought: the fifth verb, checked at
	// the portal boundary (ADR 0022 decision 1).
	VerbEnqueue Verb = "enqueue"
)

// verbs is the closed set. A verb not listed here is not a capability verb,
// and Parse refuses it rather than inventing a namespace.
var verbs = map[Verb]struct{}{
	VerbConnect: {},
	VerbRead:    {},
	VerbAct:     {},
	VerbCall:    {},
	VerbEnqueue: {},
}

// Verbs returns the closed verb set, for diagnostics and tests.
func Verbs() []Verb {
	return []Verb{VerbConnect, VerbRead, VerbAct, VerbCall, VerbEnqueue}
}

// Wildcard is the one non-referential target: it names any target of its verb
// (`read:*`, `call:*`). It exists so a ceiling can permit a whole verb without
// enumerating the catalog; everything else a capability names must exist.
const Wildcard = "*"

// Capability is one closed, referential capability (ADR 0021 decision 11): a
// verb and the existing name it applies to.
//
//	connect             a portal attachment
//	enqueue             scheduling a thought
//	read:<type>         reading a type
//	read:<type>.<field> reading one field (a sensitive field needs its own
//	                    grant; a type grant does not imply it)
//	act:<type>.<action> performing a declared action on a type
//	call:<tool>         calling a declared tool
type Capability struct {
	// Verb is the capability verb.
	Verb Verb
	// Target is the referential name the verb applies to; empty for connect
	// and enqueue, and Wildcard for "any target of this verb".
	Target string
}

// Connect is the attachment capability.
func Connect() Capability { return Capability{Verb: VerbConnect} }

// Enqueue is the scheduling capability.
func Enqueue() Capability { return Capability{Verb: VerbEnqueue} }

// Read is the capability to read a type.
func Read(typeName string) Capability {
	return Capability{Verb: VerbRead, Target: strings.TrimSpace(typeName)}
}

// ReadField is the capability to read one field of a type — the grant a
// sensitive field requires (ADR 0022 decision 7).
func ReadField(typeName, field string) Capability {
	return Capability{Verb: VerbRead, Target: joinTarget(typeName, field)}
}

// Act is the capability to perform a declared action on a type.
func Act(typeName, action string) Capability {
	return Capability{Verb: VerbAct, Target: joinTarget(typeName, action)}
}

// Call is the capability to call a declared tool.
func Call(toolName string) Capability {
	return Capability{Verb: VerbCall, Target: strings.TrimSpace(toolName)}
}

// Any is the wildcard capability for a verb: every target of it.
func Any(verb Verb) Capability { return Capability{Verb: verb, Target: Wildcard} }

// String renders the capability as `verb` or `verb:target`.
func (c Capability) String() string {
	if c.Target == "" {
		return string(c.Verb)
	}
	return string(c.Verb) + ":" + c.Target
}

// IsZero reports whether c names no capability.
func (c Capability) IsZero() bool { return c.Verb == "" && c.Target == "" }

// Validate checks the verb is closed and the target has the shape the verb
// requires. It does NOT check that the named thing exists — that is the
// assembly-time referential check, which needs the navi's declarations.
func (c Capability) Validate() error {
	if _, ok := verbs[c.Verb]; !ok {
		return fmt.Errorf("%w: capability %q has no verb", ErrInvalid, c.String())
	}
	target := c.Target
	switch c.Verb {
	case VerbConnect, VerbEnqueue:
		if target != "" && target != Wildcard {
			return fmt.Errorf("%w: %s takes no target, got %q", ErrInvalid, c.Verb, target)
		}
		return nil
	case VerbCall:
		if target == "" {
			return fmt.Errorf("%w: %s needs a tool name", ErrInvalid, c.Verb)
		}
		if target == Wildcard {
			return nil
		}
		if strings.Contains(target, ".") {
			return fmt.Errorf("%w: %s target %q is a tool name, not a path", ErrInvalid, c.Verb, target)
		}
		return nil
	case VerbRead:
		switch segments := strings.Split(target, "."); len(segments) {
		case 1:
			if target == "" {
				return fmt.Errorf("%w: %s needs a type name", ErrInvalid, c.Verb)
			}
		case 2:
			if segments[0] == "" || segments[1] == "" {
				return fmt.Errorf("%w: %s target %q needs a type and a field", ErrInvalid, c.Verb, target)
			}
		default:
			return fmt.Errorf("%w: %s target %q is not <type> or <type>.<field>", ErrInvalid, c.Verb, target)
		}
		return nil
	case VerbAct:
		segments := strings.Split(target, ".")
		if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
			return fmt.Errorf("%w: %s target %q is not <type>.<action>", ErrInvalid, c.Verb, target)
		}
		return nil
	}
	return nil
}

// covers reports whether a grant c authorizes the request want: same verb, and
// either the same target or the wildcard. A `read:<type>` grant does NOT cover
// `read:<type>.<field>` — a sensitive field needs its own grant.
func (c Capability) covers(want Capability) bool {
	if c.Verb != want.Verb {
		return false
	}
	return c.Target == Wildcard || c.Target == want.Target
}

// ParseCapability parses `verb` or `verb:target` into a validated capability.
func ParseCapability(s string) (Capability, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return Capability{}, fmt.Errorf("%w: empty capability", ErrInvalid)
	}
	verb, target, _ := strings.Cut(t, ":")
	c := Capability{Verb: Verb(strings.TrimSpace(verb)), Target: strings.TrimSpace(target)}
	if err := c.Validate(); err != nil {
		return Capability{}, err
	}
	return c, nil
}

// MustCapability is ParseCapability for a compile-time constant.
func MustCapability(s string) Capability {
	c, err := ParseCapability(s)
	if err != nil {
		panic(err)
	}
	return c
}

// joinTarget builds a dotted referential target.
func joinTarget(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return strings.TrimSpace(a + b)
	}
	return a + "." + b
}

// Grant is one allowance or denial: a capability, and the conditions under
// which it applies (ADR 0021 decision 11; ADR 0022 decision 6 — "every
// condition must pass"). Conditions only ever narrow, so a grant with
// conditions permits a subset of the same grant without them.
type Grant struct {
	// Capability is what the grant is about.
	Capability Capability
	// When lists the conditions that must all pass for the grant to apply.
	// Empty means the grant applies whenever its capability matches.
	When []Condition
}

// When builds a grant with conditions — the form a transport- or
// network-scoped allowance takes:
//
//	When(Enqueue(), OnTransport("lan"))
func When(c Capability, conds ...Condition) Grant {
	return Grant{Capability: c, When: conds}
}

// Allows builds unconditioned grants, so the common case stays terse:
//
//	CapabilitySet{Allow: Allows(Read("thought"), Call("todo_read"))}
func Allows(caps ...Capability) []Grant {
	out := make([]Grant, 0, len(caps))
	for _, c := range caps {
		out = append(out, Grant{Capability: c})
	}
	return out
}

// applies reports whether the grant covers the request and every one of its
// conditions passes.
func (g Grant) applies(c Capability, ctx RequestContext) bool {
	return g.Capability.covers(c) && conditionsPass(g.When, ctx)
}

// CapabilitySet is the permission side of a role or group (ADR 0021 decision
// 5): what holding it permits. Deny wins over allow, allow is required, every
// condition must pass, and the set can only narrow the navi's compiled ceiling
// — Run checks that with Exceeds.
type CapabilitySet struct {
	// Allow lists what the set permits. Empty permits nothing: the evaluation
	// rule is allow-required, so an empty set is a denial, never "everything".
	Allow []Grant
	// Deny removes what Allow would otherwise permit. Deny wins.
	Deny []Grant
}

// Permits reports whether the set authorizes c for this request on its own: an
// explicit deny whose conditions pass wins, then an allow whose conditions
// pass must be present. The zero set permits nothing.
func (s CapabilitySet) Permits(c Capability, ctx RequestContext) bool {
	for _, d := range s.Deny {
		if d.applies(c, ctx) {
			return false
		}
	}
	for _, a := range s.Allow {
		if a.applies(c, ctx) {
			return true
		}
	}
	return false
}

// Exceeds reports whether the set permits anything the ceiling does not, and
// names the first violation. Only Allow can widen — a deny only narrows — and
// a condition only narrows further, so the capability is checked and the
// conditions are not: a conditioned grant can never exceed an unconditioned
// one. That is what makes a role's grants composable with the compiled
// ceiling, and it is the intersection rule enforced at assembly.
func (s CapabilitySet) Exceeds(ceiling CapabilitySet) error {
	for _, a := range s.Allow {
		if !ceiling.Permits(a.Capability, RequestContext{}) {
			return fmt.Errorf("%w: capability %s is not permitted by the compiled ceiling", ErrInvalid, a.Capability)
		}
	}
	return nil
}

// Validate checks every capability and condition in the set.
func (s CapabilitySet) Validate() error {
	for _, g := range append(append([]Grant(nil), s.Allow...), s.Deny...) {
		if err := g.Capability.Validate(); err != nil {
			return err
		}
		if err := validateConditions(g.When); err != nil {
			return err
		}
	}
	return nil
}

// String renders a set for diagnostics.
func (s CapabilitySet) String() string {
	render := func(grants []Grant) string {
		out := make([]string, 0, len(grants))
		for _, g := range grants {
			if len(g.When) == 0 {
				out = append(out, g.Capability.String())
				continue
			}
			names := make([]string, 0, len(g.When))
			for _, c := range g.When {
				names = append(names, c.Name())
			}
			out = append(out, g.Capability.String()+" when "+strings.Join(names, "+"))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	parts := []string{"allow=" + render(s.Allow)}
	if len(s.Deny) > 0 {
		parts = append(parts, "deny="+render(s.Deny))
	}
	return strings.Join(parts, " ")
}
