package navi

import (
	"errors"
	"testing"
	"time"
)

func TestOnTransport(t *testing.T) {
	c := OnTransport("LAN")
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !c.Match(RequestContext{Transport: "lan"}) {
		t.Error("the label match must be case-insensitive")
	}
	if !c.Match(RequestContext{Transport: " LAN "}) {
		t.Error("the label match must trim")
	}
	if c.Match(RequestContext{Transport: "lora"}) {
		t.Error("a different transport must not match")
	}
	if c.Match(RequestContext{}) {
		t.Error("an absent transport must not match a named one")
	}
	if err := OnTransport("  ").Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty label = %v, want ErrInvalid", err)
	}
}

func TestOnNetwork(t *testing.T) {
	c := OnNetwork("10.0.0.0/8")
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, addr := range []string{"10.1.2.3", "10.1.2.3:4567"} {
		if !c.Match(RequestContext{RemoteAddr: addr}) {
			t.Errorf("%s must be inside 10.0.0.0/8", addr)
		}
	}
	for _, addr := range []string{"192.168.1.5", "", "not-an-ip"} {
		if c.Match(RequestContext{RemoteAddr: addr}) {
			t.Errorf("%q must not match", addr)
		}
	}
	if err := OnNetwork("nonsense").Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad CIDR = %v, want ErrInvalid", err)
	}
	if err := OnNetwork("").Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty CIDR = %v, want ErrInvalid", err)
	}
	// A malformed CIDR never matches, even unvalidated.
	if OnNetwork("nonsense").Match(RequestContext{RemoteAddr: "10.0.0.1"}) {
		t.Error("a malformed CIDR must not match")
	}
}

func TestBetween(t *testing.T) {
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := Between(base, base.Add(time.Hour))
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !c.Match(RequestContext{Now: base.Add(30 * time.Minute)}) {
		t.Error("inside the window must match")
	}
	if c.Match(RequestContext{Now: base.Add(-time.Minute)}) {
		t.Error("before the window must not match")
	}
	if c.Match(RequestContext{Now: base.Add(2 * time.Hour)}) {
		t.Error("after the window must not match")
	}

	// One bound is enough; neither is a malformed condition.
	if err := Between(base, time.Time{}).Validate(); err != nil {
		t.Errorf("open-ended window: %v", err)
	}
	if err := Between(time.Time{}, time.Time{}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("no bounds = %v, want ErrInvalid", err)
	}
	if err := Between(base.Add(time.Hour), base).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("inverted window = %v, want ErrInvalid", err)
	}
}

func TestOnClaim(t *testing.T) {
	who := principalWithClaims(t, "user:alice", map[string]string{"role": "field", "team": "voice"})
	c := OnClaim("role", "field")
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !c.Match(RequestContext{Principal: who}) {
		t.Error("a matching claim must pass")
	}
	if OnClaim("role", "admin").Match(RequestContext{Principal: who}) {
		t.Error("a different value must not pass")
	}
	if OnClaim("absent", "x").Match(RequestContext{Principal: who}) {
		t.Error("an absent claim must not pass")
	}
	if c.Match(RequestContext{}) {
		t.Error("no principal must not pass a claim condition")
	}
	if err := OnClaim("", "x").Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty claim name = %v, want ErrInvalid", err)
	}
}

func TestConditionsPass(t *testing.T) {
	// No conditions is the widest form of a grant: it passes.
	if !conditionsPass(nil, RequestContext{}) {
		t.Error("no conditions must pass")
	}
	// Every condition must pass (ADR 0022 decision 6).
	both := []Condition{OnTransport("lan"), OnClaim("role", "field")}
	who := principalWithClaims(t, "user:alice", map[string]string{"role": "field"})
	if !conditionsPass(both, RequestContext{Transport: "lan", Principal: who}) {
		t.Error("both conditions hold")
	}
	if conditionsPass(both, RequestContext{Transport: "lora", Principal: who}) {
		t.Error("one failing condition must fail the grant")
	}
	// A nil condition is never satisfied, so a malformed grant cannot widen.
	if conditionsPass([]Condition{nil}, RequestContext{}) {
		t.Error("a nil condition must not pass")
	}
	if err := validateConditions([]Condition{nil}); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil condition = %v, want ErrInvalid", err)
	}
}

func TestRequestContextUsesItsClock(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := (RequestContext{Now: fixed}).at(); !got.Equal(fixed) {
		t.Errorf("at() = %s, want the fixed clock", got)
	}
	if got := (RequestContext{}).at(); got.IsZero() {
		t.Error("at() must fall back to the wall clock")
	}
}
