package navi

import (
	"errors"
	"testing"
	"time"
)

// TestExceedsRefusesAnUnconditionedGrantAgainstAConditionedCeiling is the
// soundness case: capability-only comparison would call these equal and hand
// the principal an unconditioned capability the ceiling never granted.
func TestExceedsRefusesAnUnconditionedGrantAgainstAConditionedCeiling(t *testing.T) {
	ceiling := CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport("lan"))}}

	bare := CapabilitySet{Allow: Allows(Enqueue())}
	if err := bare.Exceeds(ceiling); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bare grant against a conditioned ceiling = %v, want ErrInvalid (escalation)", err)
	}

	// The same capability under the same condition is within it.
	same := CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport("lan"))}}
	if err := same.Exceeds(ceiling); err != nil {
		t.Errorf("the same condition must be within the ceiling: %v", err)
	}

	// A strictly narrower one is too.
	narrower := CapabilitySet{Allow: []Grant{
		When(Enqueue(), OnTransport("lan"), OnClaim("tier", "field")),
	}}
	if err := narrower.Exceeds(ceiling); err != nil {
		t.Errorf("a narrower grant must be within the ceiling: %v", err)
	}
}

// TestExceedsRefusesABareGrantAgainstAConditionedTimeCeiling is the case a
// sample-context check gets wrong: a window that happens to contain "now"
// evaluates as passing, so comparing by evaluating rather than by implication
// would accept an unconditioned grant.
func TestExceedsRefusesABareGrantAgainstAConditionedTimeCeiling(t *testing.T) {
	window := Between(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	ceiling := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), window)}}

	if !ceiling.Permits(Call("shell_exec"), RequestContext{}) {
		t.Fatal("fixture: the window must contain now, or the test proves nothing")
	}
	bare := CapabilitySet{Allow: Allows(Call("shell_exec"))}
	if err := bare.Exceeds(ceiling); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bare grant against a time-conditioned ceiling = %v, want ErrInvalid", err)
	}
}

func TestExceedsComparesConditionsStructurally(t *testing.T) {
	// A narrower subnet is within a wider one; a wider subnet is not.
	ceiling := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnNetwork("10.0.0.0/8"))}}
	inner := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnNetwork("10.1.0.0/16"))}}
	outer := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnNetwork("10.0.0.0/4"))}}

	if err := inner.Exceeds(ceiling); err != nil {
		t.Errorf("a narrower subnet must be within the ceiling: %v", err)
	}
	if err := outer.Exceeds(ceiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("a wider subnet must exceed the ceiling: %v", err)
	}

	// A narrower window is within a wider one; a wider window is not.
	base := time.Now()
	wide := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), Between(base, base.Add(2*time.Hour)))}}
	tight := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), Between(base.Add(time.Minute), base.Add(time.Hour)))}}
	loose := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), Between(base.Add(-time.Hour), base.Add(time.Hour)))}}

	if err := tight.Exceeds(wide); err != nil {
		t.Errorf("a tighter window must be within the ceiling: %v", err)
	}
	if err := loose.Exceeds(wide); !errors.Is(err, ErrInvalid) {
		t.Errorf("a looser window must exceed the ceiling: %v", err)
	}

	// An open-start window is wider than a bounded one, so it exceeds it. The
	// zero bound is built directly: Between refuses two unbounded sides, because
	// such a condition can never narrow anything.
	openStart := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), timeWindowCondition{to: base.Add(time.Hour)})}}
	bounded := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), Between(base, base.Add(time.Hour)))}}
	if err := bounded.Exceeds(openStart); err != nil {
		t.Errorf("a bounded window must be within an open-start one: %v", err)
	}
	if err := openStart.Exceeds(bounded); !errors.Is(err, ErrInvalid) {
		t.Errorf("an open-start window must exceed a bounded one: %v", err)
	}
}

// TestExceedsRefusesUncomparableConditions pins the fail-closed direction: a
// custom condition is an opaque predicate, so it is never assumed to be
// narrower than something it cannot be shown to imply.
func TestExceedsRefusesUncomparableConditions(t *testing.T) {
	custom := ConditionFunc{ConditionName: "odd-minute", MatchFunc: func(RequestContext) bool { return true }}
	ceiling := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnTransport("lan"))}}
	claimed := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), custom)}}
	if err := claimed.Exceeds(ceiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("an uncomparable condition = %v, want ErrInvalid (fail closed)", err)
	}

	// A custom condition on the ceiling side is equally uncomparable.
	otherCeiling := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), custom)}}
	if err := (CapabilitySet{Allow: Allows(Call("shell_exec"))}).Exceeds(otherCeiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bare grant against an opaque condition = %v, want ErrInvalid", err)
	}
}

// TestChannelIsSeparateFromTransport is the input-channel case: a console and a
// voice portal may both be in-process with no transport, and only the channel
// tells them apart — which is what makes the console's structural root
// expressible as policy rather than only as prose.
func TestChannelIsSeparateFromTransport(t *testing.T) {
	console := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnChannel("console"))}}
	microphone := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnChannel("microphone"))}}

	inProcessConsole := RequestContext{Channel: "console"}
	inProcessMic := RequestContext{Channel: "microphone"}

	if !console.Permits(Call("shell_exec"), inProcessConsole) {
		t.Error("the console grant must permit the in-process console")
	}
	if console.Permits(Call("shell_exec"), inProcessMic) {
		t.Error("the console grant must not permit a microphone")
	}
	if !microphone.Permits(Call("shell_exec"), inProcessMic) {
		t.Error("the microphone grant must permit the microphone")
	}

	// A transport condition does not match a binding that has no transport:
	// a requirement that cannot be shown to hold is not met.
	lan := CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport("lan"))}}
	if lan.Permits(Enqueue(), inProcessConsole) {
		t.Error("a transport condition must not match an in-process binding with no transport")
	}
	// The same binding is reachable by an unconditioned or channel-scoped grant.
	if !(CapabilitySet{Allow: Allows(Enqueue())}).Permits(Enqueue(), inProcessConsole) {
		t.Error("an unconditioned grant must reach an in-process binding")
	}

	// The channel and the transport are independent facts: a console over the
	// network is a console, and it is also on the LAN.
	ssh := RequestContext{Channel: "console", Transport: "lan"}
	if !console.Permits(Call("shell_exec"), ssh) {
		t.Error("an SSH console is a console")
	}
	if !lan.Permits(Enqueue(), ssh) {
		t.Error("an SSH console arrives over the LAN transport")
	}
}

func TestOnChannelValidates(t *testing.T) {
	if err := OnChannel("console").Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if err := OnChannel("  ").Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty label = %v, want ErrInvalid", err)
	}
	if !OnChannel("CONSOLE").Match(RequestContext{Channel: " console "}) {
		t.Error("the channel match must trim and fold case")
	}
}

// TestChannelLabelsAreNotReusableAcrossTransports pins the hazard OnChannel
// documents. "Local only" cannot be expressed as the absence of a transport (a
// transport condition correctly fails when there is none), so locality has to
// be a channel — which makes the label load-bearing. A label reused across
// transports widens the grant silently, and naming both facts is the fix.
func TestChannelLabelsAreNotReusableAcrossTransports(t *testing.T) {
	// The recommended form: channel AND transport, so "this device's own
	// microphone" cannot drift into "any audio, anywhere".
	ownMic := CapabilitySet{Allow: []Grant{
		When(Call("shell_exec"), OnChannel("microphone"), OnTransport("in-process")),
	}}
	relayed := RequestContext{Channel: "microphone", Transport: "lora"}
	if ownMic.Permits(Call("shell_exec"), relayed) {
		t.Error("the grant must not reach mesh-relayed audio")
	}
	if !ownMic.Permits(Call("shell_exec"), RequestContext{Channel: "microphone", Transport: "in-process"}) {
		t.Error("the grant must reach this device's own microphone")
	}

	// The hazard, made explicit: a channel-only grant cannot tell the two
	// apart, because the label is the only fact it names.
	labelOnly := CapabilitySet{Allow: []Grant{When(Call("shell_exec"), OnChannel("microphone"))}}
	if !labelOnly.Permits(Call("shell_exec"), relayed) {
		t.Fatal("fixture: a reused label is expected to widen — that is the hazard being documented")
	}
}
