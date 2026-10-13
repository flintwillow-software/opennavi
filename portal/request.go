package portal

import (
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the request half of the portal seam (ADR 0021 decision 9; ADR
// 0022 decisions 1 and 8). A portal presents a credential, the navi resolves
// it to a verified principal, and the portal supplies the FACTS about how the
// request arrived. The navi maps principal + facts onto a capability and a
// request context and asks the compiled engine; the portal never sees a
// capability verb, a role, or a condition (ADR 0022 decision 8).
//
// The facts travel with the principal as one value so the seam does not grow a
// parameter per fact on every method. Only the binding knows them: transport
// is how the request arrived, the input channel is what carried the intent,
// and the remote address is where it came from — three independent facts
// (ADR 0021 decision 9 makes transport and network conditions on a request,
// never identities).

// RequestInfo carries the facts about a request that a policy condition is
// evaluated against. The binding supplies it; the navi evaluates it.
type RequestInfo struct {
	// Transport names how the request ARRIVED — the binding's own identity:
	// "lan", "tailnet", "lora", "in-process". Every binding declares one; the
	// in-process binding's label is "in-process", not an absence. A binding
	// must not reuse a label for a different path, or every transport-scoped
	// grant widens silently.
	Transport string
	// Channel names what carried the intent — the INPUT CHANNEL: "console",
	// "microphone", "sensor", "api". It is deliberately separate from
	// Transport: an SSH session is a console over a network transport, and a
	// voice portal is a microphone in-process. One label, one input path.
	Channel string
	// RemoteAddr is the source address when the binding has one (host:port or
	// host). Empty for an in-process console or a serial link.
	RemoteAddr string
	// At is the request time. Zero means "the evaluator's clock".
	At time.Time
}

// Actor is a VERIFIED principal together with the facts about the request it
// arrived with. It is the value the portal seam carries in place of a bare
// reference, so an authorization decision sees both who is asking and when,
// from where, and over what.
//
// The principal is never a value a client asserted: a portal obtains it from
// the navi's Authenticator (ADR 0021 decisions 6-8). A zero Actor — no
// verified principal — is refused, never defaulted open (ADR 0022 decision 9).
type Actor struct {
	// Who is the verified principal.
	Who principal.Ref
	// Info is how the request arrived.
	Info RequestInfo
}

// IsZero reports whether no principal was verified.
func (a Actor) IsZero() bool { return a.Who.IsZero() }

// Scoper binds an object surface to the actor a binding verified (ADR 0022
// decision 1). The gateway calls it after authenticating, so the object read
// and action paths authorize as the connection's principal instead of as one
// declared principal; the in-process console uses the surface as handed to it.
// A surface that does not implement it acts as its own declared actor.
type Scoper interface {
	// Scope returns the surface as actor sees it: reads are authorized as
	// actor and actions are authorized as actor. It does not re-authenticate —
	// the actor is already verified.
	Scope(actor Actor) object.Surface
}

// recordOrigin attributes t to actor and records the request facts (ADR 0022
// decisions 2 and 3). It is the one place a thought's origin is established,
// and it never overwrites an origin the runtime already set: attribution is
// recorded at the door, not chosen by the thought's author or a later hop.
func recordOrigin(t *thought.Thought, actor Actor) {
	if t == nil || !t.Origin.IsZero() {
		return
	}
	t.Origin = actor.Who
	t.OriginInfo = thought.OriginInfo{
		Transport:  actor.Info.Transport,
		Channel:    actor.Info.Channel,
		RemoteAddr: actor.Info.RemoteAddr,
		At:         actor.Info.At,
	}
}
