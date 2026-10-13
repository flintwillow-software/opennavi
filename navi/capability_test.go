package navi

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/principal"
)

func TestCapabilityConstructors(t *testing.T) {
	cases := map[string]Capability{
		"connect":                   Connect(),
		"enqueue":                   Enqueue(),
		"read:thought":              Read("thought"),
		"read:thought.assembled":    ReadField("thought", "assembled"),
		"act:thought.pause":         Act("thought", "pause"),
		"call:github_issue_comment": Call("github_issue_comment"),
		"read:*":                    Any(VerbRead),
	}
	for want, got := range cases {
		if got.String() != want {
			t.Errorf("capability = %q, want %q", got.String(), want)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: %v", want, err)
		}
	}
	if !(Capability{}).IsZero() {
		t.Error("the zero Capability must report zero")
	}
	if Connect().IsZero() {
		t.Error("connect must not be zero")
	}
}

func TestParseCapability(t *testing.T) {
	for _, s := range []string{"connect", "enqueue", "read:thought", "read:thought.title", "act:todo.complete", "call:shell_exec", "read:*", "call:*"} {
		c, err := ParseCapability(s)
		if err != nil {
			t.Errorf("ParseCapability(%q): %v", s, err)
			continue
		}
		if c.String() != s {
			t.Errorf("round trip %q -> %q", s, c.String())
		}
	}

	bad := []string{
		"",                      // empty
		"read",                  // read needs a target
		"act",                   // act needs a target
		"call",                  // call needs a tool name
		"invoke:thing",          // not a verb
		"connect:portal",        // connect takes no target
		"enqueue:queue",         // enqueue takes no target
		"read:a.b.c",            // read is <type> or <type>.<field>
		"read:.field",           // empty type
		"read:type.",            // empty field
		"act:thought",           // act is <type>.<action>
		"act:thought.pause.now", // too many segments
		"act:.pause",            // empty type
		"call:github.comment",   // a tool name, not a path
	}
	for _, s := range bad {
		if _, err := ParseCapability(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseCapability(%q) = %v, want ErrInvalid", s, err)
		}
	}
}

func TestCapabilityCovers(t *testing.T) {
	// A grant covers the same target, or any target with the wildcard.
	if !Call("github_issue_comment").covers(Call("github_issue_comment")) {
		t.Error("an exact grant must cover its own request")
	}
	if !Any(VerbCall).covers(Call("shell_exec")) {
		t.Error("a wildcard grant must cover any target of its verb")
	}
	// It never covers another verb, or a field via its type.
	if Any(VerbRead).covers(Call("shell_exec")) {
		t.Error("a wildcard read must not cover a call")
	}
	if Read("thought").covers(ReadField("thought", "assembled")) {
		t.Error("a type grant must not imply a sensitive-field grant")
	}
	if ReadField("thought", "assembled").covers(Read("thought")) {
		t.Error("a field grant must not cover the whole type")
	}
}

func TestCapabilitySetPermits(t *testing.T) {
	set := CapabilitySet{
		Allow: Allows(Read("thought"), Call("todo_read"), Any(VerbEnqueue)),
		Deny:  Allows(ReadField("thought", "assembled"), Call("shell_exec")),
	}

	// Allow-required: what is listed is permitted, what is not is denied.
	if !set.Permits(Read("thought"), RequestContext{}) {
		t.Error("an allowed capability must be permitted")
	}
	if !set.Permits(Enqueue(), RequestContext{}) {
		t.Error("a wildcard verb grant must permit the verb")
	}
	if set.Permits(Act("thought", "pause"), RequestContext{}) {
		t.Error("an unlisted capability must be denied (allow-required)")
	}
	if set.Permits(ReadField("thought", "title"), RequestContext{}) {
		t.Error("a type grant must not permit a field read")
	}

	// Deny wins over allow.
	if set.Permits(ReadField("thought", "assembled"), RequestContext{}) {
		t.Error("an explicit deny must win")
	}

	// The zero set permits nothing.
	var zero CapabilitySet
	if zero.Permits(Connect(), RequestContext{}) {
		t.Error("the zero set must permit nothing")
	}
}

func TestCapabilitySetExceedsCeiling(t *testing.T) {
	ceiling := CapabilitySet{Allow: Allows(Read("thought"), Call("todo_read"))}

	within := CapabilitySet{Allow: Allows(Read("thought")), Deny: Allows(Read("thought"))}
	if err := within.Exceeds(ceiling); err != nil {
		t.Errorf("narrowing must be allowed: %v", err)
	}
	if err := (CapabilitySet{}).Exceeds(ceiling); err != nil {
		t.Errorf("an empty set must be allowed: %v", err)
	}

	wider := CapabilitySet{Allow: Allows(Call("shell_exec"))}
	if err := wider.Exceeds(ceiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("a grant outside the ceiling = %v, want ErrInvalid", err)
	}

	// A deny can never widen, so it is never a violation even when it names
	// something the ceiling does not carry.
	denyOnly := CapabilitySet{Deny: Allows(Call("shell_exec"))}
	if err := denyOnly.Exceeds(ceiling); err != nil {
		t.Errorf("a deny must not be an excess: %v", err)
	}
}

func TestCapabilitySetValidate(t *testing.T) {
	good := CapabilitySet{Allow: Allows(Connect(), Act("todo", "complete"))}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	bad := CapabilitySet{Allow: []Grant{{Capability: Capability{Verb: "invoke", Target: "x"}}}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate = %v, want ErrInvalid", err)
	}
	badCondition := CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport(""))}}
	if err := badCondition.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate with a malformed condition = %v, want ErrInvalid", err)
	}
}

func TestVerbsAreClosed(t *testing.T) {
	want := map[Verb]bool{VerbConnect: true, VerbRead: true, VerbAct: true, VerbCall: true, VerbEnqueue: true}
	got := Verbs()
	if len(got) != len(want) {
		t.Fatalf("Verbs() = %v, want %d entries", got, len(want))
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("Verbs() carries %q, which is not in the closed set", v)
		}
	}
}

// TestGrantsCarryConditions is the worked shape ADR 0021 decision 9 needs: the
// same verb permitted under different conditions, so a LAN client and a mesh
// peer are told apart by a fact about the request rather than by putting
// transport into the principal.
func TestGrantsCarryConditions(t *testing.T) {
	lan := CapabilitySet{Allow: []Grant{When(Enqueue(), OnTransport("lan"))}}
	mesh := CapabilitySet{Allow: []Grant{
		When(Enqueue(), OnTransport("lora"), OnClaim("role", "field")),
	}}

	alice := principalWithClaims(t, "user:alice", map[string]string{"role": "field"})

	// The LAN grant applies only on the LAN transport.
	if !lan.Permits(Enqueue(), RequestContext{Transport: "lan", Principal: alice}) {
		t.Error("the LAN grant must permit enqueue on the LAN")
	}
	if lan.Permits(Enqueue(), RequestContext{Transport: "lora", Principal: alice}) {
		t.Error("the LAN grant must not permit enqueue over LoRa")
	}
	// The mesh grant needs BOTH conditions.
	if !mesh.Permits(Enqueue(), RequestContext{Transport: "lora", Principal: alice}) {
		t.Error("the mesh grant must permit the field role over LoRa")
	}
	if mesh.Permits(Enqueue(), RequestContext{Transport: "lan", Principal: alice}) {
		t.Error("the mesh grant must not permit the LAN")
	}
	notField := principalWithClaims(t, "user:bob", map[string]string{"role": "guest"})
	if mesh.Permits(Enqueue(), RequestContext{Transport: "lora", Principal: notField}) {
		t.Error("the mesh grant must not permit a principal without the claim")
	}

	// A condition only narrows, so a conditioned grant never exceeds the
	// ceiling that permits the bare capability.
	ceiling := CapabilitySet{Allow: Allows(Enqueue())}
	if err := lan.Exceeds(ceiling); err != nil {
		t.Errorf("a conditioned grant must not exceed a ceiling that permits the capability: %v", err)
	}
}

// TestConditionedDenyWins pins that a deny's conditions are honoured too: a
// conditional deny narrows only when its condition holds.
func TestConditionedDenyWins(t *testing.T) {
	set := CapabilitySet{
		Allow: Allows(Enqueue()),
		Deny:  []Grant{When(Enqueue(), OnTransport("lora"))},
	}
	if !set.Permits(Enqueue(), RequestContext{Transport: "lan"}) {
		t.Error("the deny must not apply on the LAN")
	}
	if set.Permits(Enqueue(), RequestContext{Transport: "lora"}) {
		t.Error("the deny must apply over LoRa")
	}
}

// principalWithClaims builds a resolved principal carrying claims.
func principalWithClaims(t *testing.T, ref string, claims map[string]string) *Principal {
	t.Helper()
	p, err := NewResolvedPrincipal(principal.MustParse(ref), claims, nil)
	if err != nil {
		t.Fatalf("NewResolvedPrincipal: %v", err)
	}
	return p
}
