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
// request and the acting principal. The binding supplies the transport facts
// (it is the only layer that knows them); the navi supplies the principal it
// verified and the clock. A condition never consults anything else — there is
// no ambient state to reach for, which is what keeps evaluation reproducible.
type RequestContext struct {
	// Principal is the verified acting principal, with the claims and groups
	// the trust source resolved. Nil for a request that has not been
	// authenticated: a condition may then still match on transport, and the
	// allow-required rule is what refuses the request.
	Principal *Principal
	// Now is the evaluation time. Zero means "use time.Now".
	Now time.Time
	// Transport names the binding a request arrived on: "console", "lan",
	// "tailnet", "lora" — whatever the binding declares. Empty when the
	// binding has no transport notion.
	Transport string
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

// transportCondition matches the binding's transport label.
type transportCondition struct{ want string }

// OnTransport requires the request to arrive on a named transport — "lan",
// "tailnet", "lora", "console". This is how a mesh peer is told from a LAN
// client without putting transport into the principal (ADR 0021 decision 9).
// The label is the binding's; the policy decides what it means.
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
