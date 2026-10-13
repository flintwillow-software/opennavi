package navi

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// This file is the condition vocabulary of ADR 0021 decision 11 and open
// question 4, and the request facts a condition is evaluated against (ADR 0022
// decision 6: "every condition must pass").
//
// A condition is a predicate on a REQUEST, never on identity. ADR 0021
// decision 9 is the reason the distinction is load-bearing: "source host and
// network are conditions on a request, never an identity", so a LoRa peer and
// a LAN client running the same verb are told apart by a condition on the
// request — not by inventing `user:alice@lora`, which would put transport into
// the principal and make it a trust axis the model forbids.
//
// The vocabulary and the conditions are one design moment: a capability says
// *what* may be done, a condition says *when the request qualifies*.

// RequestContext is what a condition is evaluated against: facts about the
// request and the acting principal. The binding supplies the transport and
// channel facts (it is the only layer that knows them); the navi supplies the
// principal it verified and the clock. A condition never consults anything
// else — there is no ambient state to reach for, which is what keeps
// evaluation reproducible.
type RequestContext struct {
	// Principal is the verified acting principal, with the claims and groups
	// the trust source resolved. Nil for a request that has not been
	// authenticated: a condition may then still match on transport or channel,
	// and the allow-required rule is what refuses the request.
	Principal *Principal
	// Now is the evaluation time. Zero means "use time.Now".
	Now time.Time
	// Transport names how the request ARRIVED: "lan", "tailnet", "lora",
	// "in-process" — whatever the binding declares. Empty when the binding has
	// no transport notion (a local console, a serial link).
	Transport string
	// Channel names what carried the intent — the INPUT CHANNEL: "console",
	// "microphone", "sensor", "api". It is deliberately separate from
	// Transport, because the two are independent and it is the channel that
	// trust turns on: an SSH session is a console over a network transport,
	// and a voice portal is a microphone in-process. ADR 0021 decision 9 makes
	// the in-process console root because it is the terminal the operator is
	// sitting at — an input-channel property — so a policy must be able to
	// require a channel, or a microphone could inherit the console's trust by
	// sharing its address space.
	Channel string
	// RemoteAddr is the source address when the binding has one. Empty
	// otherwise (an in-process console, a serial link).
	RemoteAddr string
}

// at resolves the context's clock.
func (c RequestContext) at() time.Time {
	if c.Now.IsZero() {
		return time.Now()
	}
	return c.Now
}

// Condition is one predicate a matched grant must satisfy (ADR 0021 decision
// 11, open question 4; ADR 0022 decision 6). It is evaluated on the request,
// never on the principal's kind, and it can only ever narrow: a grant with
// conditions permits a subset of the same grant without them.
type Condition interface {
	// Name identifies the condition kind for diagnostics and validation.
	Name() string
	// Match reports whether the request satisfies the condition.
	Match(ctx RequestContext) bool
	// Validate checks the condition is well formed — a CIDR parses, a window
	// is ordered, a name is present. It runs at assembly, so a malformed
	// condition fails the navi rather than silently never matching.
	Validate() error
}

// ConditionFunc adapts a predicate to a Condition.
//
// A custom condition is opaque to implication: Exceeds cannot show that it is
// implied by anything, so a set carrying one is never accepted as narrower than
// a condition it cannot be shown to imply. That is the fail-closed direction.
type ConditionFunc struct {
	ConditionName string
	MatchFunc     func(ctx RequestContext) bool
}

// Name implements Condition.
func (c ConditionFunc) Name() string { return c.ConditionName }

// Match implements Condition.
func (c ConditionFunc) Match(ctx RequestContext) bool {
	if c.MatchFunc == nil {
		return false
	}
	return c.MatchFunc(ctx)
}

// Validate implements Condition.
func (c ConditionFunc) Validate() error {
	if strings.TrimSpace(c.ConditionName) == "" {
		return fmt.Errorf("%w: condition has no name", ErrInvalid)
	}
	if c.MatchFunc == nil {
		return fmt.Errorf("%w: condition %q has no predicate", ErrInvalid, c.ConditionName)
	}
	return nil
}

// transportCondition matches how the request arrived.
type transportCondition struct{ want string }

// OnTransport requires the request to arrive on a named transport — "lan",
// "tailnet", "lora". This is how a mesh peer is told from a LAN client without
// putting transport into the principal (ADR 0021 decision 9). The label is the
// binding's; the policy decides what it means.
//
// An in-process binding with no transport notion leaves Transport empty, so a
// transport condition does not match it — correctly, because a condition is a
// requirement. Use OnChannel for a binding that has no transport but does have
// an input channel.
func OnTransport(label string) Condition {
	return transportCondition{want: strings.ToLower(strings.TrimSpace(label))}
}

func (c transportCondition) Name() string { return "transport" }

func (c transportCondition) Match(ctx RequestContext) bool {
	return strings.ToLower(strings.TrimSpace(ctx.Transport)) == c.want
}

func (c transportCondition) Validate() error {
	if c.want == "" {
		return fmt.Errorf("%w: transport condition needs a label", ErrInvalid)
	}
	return nil
}

// channelCondition matches the input channel the intent arrived on.
type channelCondition struct{ want string }

// OnChannel requires the request's intent to have arrived on a named input
// channel — "console", "microphone", "sensor", "api". This is the fact that
// distinguishes the console from an ambient-input portal embedded the same
// way: both may be in-process, and only the channel says which is which. It is
// what makes ADR 0021 decision 9's "the console is root because it is the
// terminal the operator is sitting at" expressible as policy rather than only
// as prose.
func OnChannel(label string) Condition {
	return channelCondition{want: strings.ToLower(strings.TrimSpace(label))}
}

func (c channelCondition) Name() string { return "channel" }

func (c channelCondition) Match(ctx RequestContext) bool {
	return strings.ToLower(strings.TrimSpace(ctx.Channel)) == c.want
}

func (c channelCondition) Validate() error {
	if c.want == "" {
		return fmt.Errorf("%w: channel condition needs a label", ErrInvalid)
	}
	return nil
}

// networkCondition matches the source address against a CIDR.
type networkCondition struct {
	cidr string
	net  *net.IPNet
}

// OnNetwork requires the request's source address to fall inside a CIDR. It is
// the address-level form of the same idea as OnTransport: a fact about the
// request, never an identity (ADR 0021 decision 9).
func OnNetwork(cidr string) Condition {
	c := networkCondition{cidr: strings.TrimSpace(cidr)}
	if _, ipnet, err := net.ParseCIDR(c.cidr); err == nil {
		c.net = ipnet
	}
	return c
}

func (c networkCondition) Name() string { return "network" }

func (c networkCondition) Match(ctx RequestContext) bool {
	if c.net == nil {
		return false
	}
	addr := strings.TrimSpace(ctx.RemoteAddr)
	if addr == "" {
		return false
	}
	// A binding may report host:port; the address is what matters.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return c.net.Contains(ip)
}

func (c networkCondition) Validate() error {
	if c.cidr == "" {
		return fmt.Errorf("%w: network condition needs a CIDR", ErrInvalid)
	}
	if c.net == nil {
		return fmt.Errorf("%w: network condition %q is not a CIDR", ErrInvalid, c.cidr)
	}
	return nil
}

// timeWindowCondition matches the evaluation time against a window.
type timeWindowCondition struct {
	from, to time.Time
}

// Between requires the request to be evaluated inside a time window. Either
// bound may be zero (unbounded). It is the first condition ADR 0021 open
// question 4 names.
func Between(from, to time.Time) Condition {
	return timeWindowCondition{from: from, to: to}
}

func (c timeWindowCondition) Name() string { return "time" }

func (c timeWindowCondition) Match(ctx RequestContext) bool {
	now := ctx.at()
	if !c.from.IsZero() && now.Before(c.from) {
		return false
	}
	if !c.to.IsZero() && now.After(c.to) {
		return false
	}
	return true
}

func (c timeWindowCondition) Validate() error {
	if c.from.IsZero() && c.to.IsZero() {
		return fmt.Errorf("%w: a time condition needs at least one bound", ErrInvalid)
	}
	if !c.from.IsZero() && !c.to.IsZero() && c.to.Before(c.from) {
		return fmt.Errorf("%w: time condition ends before it starts", ErrInvalid)
	}
	return nil
}

// claimCondition matches one claim the trust source supplied.
type claimCondition struct{ name, value string }

// OnClaim requires the principal's resolved claims to carry name=value. Claims
// come from the trust source (ADR 0021 decision 4); the provider supplies
// identity and claims, the navi supplies policy — so this reads a fact the
// provider asserted and the navi decided to require.
//
// A claim is NOT a role. ADR 0021 decision 2 defines a role as a compiled
// capability set, and decision 5 splits a role into a trust policy and a
// permission policy; neither is a claim, and a trust source can never mint one.
// Name claim keys for what they are — `tier`, `team`, `deployment` — rather
// than `role`, so the two concepts do not read as one.
func OnClaim(name, value string) Condition {
	return claimCondition{name: strings.TrimSpace(name), value: value}
}

func (c claimCondition) Name() string { return "claim:" + c.name }

func (c claimCondition) Match(ctx RequestContext) bool {
	if ctx.Principal == nil {
		return false
	}
	return ctx.Principal.Claims()[c.name] == c.value
}

func (c claimCondition) Validate() error {
	if c.name == "" {
		return fmt.Errorf("%w: claim condition needs a claim name", ErrInvalid)
	}
	return nil
}

// conditionsPass reports whether every condition holds. No conditions is a
// pass: the condition layer only narrows, so an unconditioned grant is the
// widest form of itself.
func conditionsPass(conds []Condition, ctx RequestContext) bool {
	for _, c := range conds {
		if c == nil || !c.Match(ctx) {
			return false
		}
	}
	return true
}

// validateConditions checks every condition in a grant.
func validateConditions(conds []Condition) error {
	for _, c := range conds {
		if c == nil {
			return fmt.Errorf("%w: nil condition", ErrInvalid)
		}
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ── implication ─────────────────────────────────────────────────────────

// impliesAll reports whether holding every condition in have guarantees every
// condition in want — that is, whether the request set described by have is a
// SUBSET of the one described by want. It is the comparison a narrowing check
// needs: a grant is inside a ceiling only when its conditions are at least as
// restrictive as the ceiling's.
//
// It is deliberately conservative. A pair it cannot reason about is reported as
// not implied, so an uncomparable condition makes the check refuse rather than
// assume. Conditions are opaque predicates in general; only the ones this
// package defines are comparable, so a custom ConditionFunc never narrows past
// a condition it cannot be shown to imply.
func impliesAll(have, want []Condition) bool {
	for _, w := range want {
		implied := false
		for _, h := range have {
			if conditionImplies(h, w) {
				implied = true
				break
			}
		}
		if !implied {
			return false
		}
	}
	return true
}

// conditionImplies reports whether have is at least as restrictive as want.
func conditionImplies(have, want Condition) bool {
	switch w := want.(type) {
	case transportCondition:
		h, ok := have.(transportCondition)
		return ok && h.want == w.want
	case channelCondition:
		h, ok := have.(channelCondition)
		return ok && h.want == w.want
	case claimCondition:
		h, ok := have.(claimCondition)
		return ok && h.name == w.name && h.value == w.value
	case networkCondition:
		h, ok := have.(networkCondition)
		return ok && h.net != nil && w.net != nil && subnetOf(h.net, w.net)
	case timeWindowCondition:
		h, ok := have.(timeWindowCondition)
		return ok && windowWithin(h, w)
	}
	return false
}

// subnetOf reports whether inner is contained in outer.
func subnetOf(inner, outer *net.IPNet) bool {
	innerOnes, innerBits := inner.Mask.Size()
	outerOnes, outerBits := outer.Mask.Size()
	if innerBits != outerBits || innerOnes < outerOnes {
		return false
	}
	return outer.Contains(inner.IP)
}

// windowWithin reports whether h is contained in w. A zero bound is unbounded
// in that direction, so an unbounded h is never inside a bounded w.
func windowWithin(h, w timeWindowCondition) bool {
	// Lower bound: h.from must be at or after w.from.
	if !w.from.IsZero() && (h.from.IsZero() || h.from.Before(w.from)) {
		return false
	}
	// Upper bound: h.to must be at or before w.to.
	if !w.to.IsZero() && (h.to.IsZero() || h.to.After(w.to)) {
		return false
	}
	return true
}
