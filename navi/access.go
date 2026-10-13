package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the enforcement seam of ADR 0022: it maps what a binding asks
// (a portal action and the request facts it arrived with) onto what the
// compiled engine understands (a capability and a request context), and
// answers with a Decision. The vocabulary stays here — a portal supplies
// facts and receives allow or refuse, never a verb, a role, or a condition
// (ADR 0021, "Consequences"; ADR 0022 decision 8).
//
// There is one engine and one acting principal (ADR 0022 decision 1):
// attachment, object read, object action, and tool call all reach Decide
// through the helpers here, so the tool path and the object path cannot drift
// (decision 5).

// serviceRef is the navi's own service principal, navi:<name> (ADR 0021
// decision 10). System-originated work — cron, the plan-closing check, any
// inbound work whose connector declares no principal — acts as it, so
// fail-closed authorization never disables the runtime's own machinery.
func (n *Navi) serviceRef() principal.Ref {
	ref, err := principal.New(principal.KindNavi, n.name)
	if err != nil {
		// A navi with no name never reaches here: Run refuses it first.
		return principal.Ref{}
	}
	return ref
}

// Authorize implements portal.Authorizer over the compiled policy: a portal
// hands the navi a control action and the verified actor, and receives allow
// or a refusal naming the engine's reason (ADR 0022 decision 8). A refusal is
// never silent — the reason travels in the error, so an enqueue that was
// refused is loud at the call site.
func (n *Navi) Authorize(action portal.Action, actor portal.Actor) error {
	cap, ok := capabilityForAction(action)
	if !ok {
		return fmt.Errorf("%w: %s (no capability for this action)", portal.ErrUnauthorized, action)
	}
	if actor.IsZero() {
		return fmt.Errorf("%w: %s (no verified principal — fail closed)", portal.ErrUnauthorized, action)
	}
	if d := n.decideActor(actor, cap, nil); !d.Allowed {
		return fmt.Errorf("%w: %s: %s", portal.ErrUnauthorized, action, d.Reason)
	}
	return nil
}

// capabilityForAction maps a portal control action to a capability. The
// targets are referential: a control action operates on a thought, so it is
// act:thought.<action> — the same capability the object surface checks for
// the same mutation (ADR 0022 decision 5).
func capabilityForAction(action portal.Action) (Capability, bool) {
	switch action {
	case portal.ActionAttach:
		return Connect(), true
	case portal.ActionEnqueue:
		return Enqueue(), true
	case portal.ActionSteer:
		return Act("thought", "steer"), true
	case portal.ActionPause:
		return Act("thought", "pause"), true
	case portal.ActionResume:
		return Act("thought", "resume"), true
	case portal.ActionCancel:
		return Act("thought", "cancel"), true
	case portal.ActionBump:
		return Act("thought", "bump"), true
	case portal.ActionRetry:
		return Act("thought", "retry"), true
	}
	return Capability{}, false
}

// decideActor evaluates one capability for a verified actor against the
// compiled policy. The navi's own service principal holds the compiled
// ceiling by construction: it is the navi acting as itself, and the ceiling
// is exactly what this binary can do at all, so requiring a declared role for
// it would let fail-closed disable the runtime (ADR 0021 decision 10). Every
// other principal is decided by the compiled roles and groups.
func (n *Navi) decideActor(actor portal.Actor, cap Capability, session *CapabilitySet) Decision {
	if n.policy == nil {
		return Decision{Reason: "the navi has no compiled policy"}
	}
	who, err := n.principalFor(actor.Who)
	if err != nil {
		return Decision{Reason: err.Error()}
	}
	ctx := n.actorContext(actor, who)
	if who.Ref() == n.serviceRef() {
		if !n.policy.Ceiling().Permits(cap, ctx) {
			return Decision{Reason: fmt.Sprintf("%s is outside the compiled ceiling", cap)}
		}
		return Decision{Allowed: true, Matched: "service:" + n.name}
	}
	return n.policy.Decide(AccessRequest{
		Principal:  who,
		Capability: cap,
		Context:    ctx,
		Session:    session,
	})
}

// actorContext builds the engine's request context from the binding's facts
// and the resolved principal (ADR 0022 decision 1). Only the binding knows
// transport and channel, so they travel on the actor; the navi supplies the
// clock and the resolved claims.
func (n *Navi) actorContext(actor portal.Actor, who *Principal) RequestContext {
	return RequestContext{
		Principal:  who,
		Now:        actor.Info.At,
		Transport:  actor.Info.Transport,
		Channel:    actor.Info.Channel,
		RemoteAddr: actor.Info.RemoteAddr,
	}
}

// principalFor resolves an actor's reference to the principal the engine
// evaluates: the claims and group membership the trust source supplied. The
// navi's own service principal resolves without a trust source — it is
// compiled, not looked up — and an unknown reference fails closed.
func (n *Navi) principalFor(ref principal.Ref) (*Principal, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnknownPrincipal, err)
	}
	if ref == n.serviceRef() {
		return NewPrincipal(ref)
	}
	if n.trust == nil {
		return nil, fmt.Errorf("%w: %s (no trust source declared)", ErrUnknownPrincipal, ref)
	}
	return n.trust.Resolve(ref)
}

// originActor is the actor a thought's recorded origin describes: the
// principal that caused it, and the request facts the binding supplied at the
// door. A thought with no recorded origin is system-originated and acts as the
// navi's service principal (ADR 0022 decision 3).
func (n *Navi) originActor(t *thought.Thought) portal.Actor {
	if t == nil || t.Origin.IsZero() {
		return portal.Actor{Who: n.serviceRef()}
	}
	return portal.Actor{
		Who: t.Origin,
		Info: portal.RequestInfo{
			Transport:  t.OriginInfo.Transport,
			Channel:    t.OriginInfo.Channel,
			RemoteAddr: t.OriginInfo.RemoteAddr,
			At:         t.OriginInfo.At,
		},
	}
}

// Compile-time check: the composition root is the policy authorizer a portal
// receives (ADR 0022 decision 8).
var _ portal.Authorizer = (*Navi)(nil)
