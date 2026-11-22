package navi

import (
	"testing"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file gates the peer declaration and the read-only `peer` object type
// (NAVI-629 slice 2, ADR 0031 decision 7; ADR 0042 decisions 2-3, 6). The
// properties that matter: a peer is a reachability record (no key, no grant),
// it is declared at composition like a type or a tool, the ceiling derives
// send:/pub:/sub: from it, and the roster renders on the object surface with no
// transport in the picture.

// TestPeerIsAReachabilityRecordOnly pins the scoping decision: a Peer carries a
// reference, addresses, and scopes — and NOTHING that could be mistaken for a
// key or a permission. The struct is deliberately unexported-field, so this
// test asserts the accessor surface rather than a field set; the point is that
// there is no PublicKey()/PeerID()/Grants() to call.
func TestPeerIsAReachabilityRecordOnly(t *testing.T) {
	p, err := NewPeer(
		principal.MustParse("navi:bastilli"),
		WithAddrs("/ip4/10.0.0.51/udp/4001/quic-v1"),
		WithScopes("team/alpha", "fleet"),
	)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	if p.Ref() != principal.MustParse("navi:bastilli") {
		t.Fatalf("Ref = %s", p.Ref())
	}
	if p.Name() != "bastilli" {
		t.Fatalf("Name = %q", p.Name())
	}
	if got := p.Addrs(); len(got) != 1 || got[0] != "/ip4/10.0.0.51/udp/4001/quic-v1" {
		t.Fatalf("Addrs = %v", got)
	}
	if got := p.Scopes(); len(got) != 2 || got[0] != "team/alpha" || got[1] != "fleet" {
		t.Fatalf("Scopes = %v", got)
	}
}

// TestPeerRefusesANonNaviReference pins that a peer must be a navi: a user or
// group reference is a category error worth refusing, not a shape to tolerate.
func TestPeerRefusesANonNaviReference(t *testing.T) {
	for _, ref := range []string{"user:alice", "group:eng"} {
		if _, err := NewPeer(principal.MustParse(ref)); err == nil {
			t.Errorf("NewPeer(%s) must refuse a non-navi reference", ref)
		}
	}
}

// TestPeerRefusesAWildcardOrEmptyScope pins that a scope is a DECLARATION, not
// a wildcard expression (ADR 0031 decision 7): `*`, an empty scope, and a
// wildcard or empty segment are all refused.
func TestPeerRefusesAWildcardOrEmptyScope(t *testing.T) {
	// ValidateScope is the shape check; a blank entry in a declaration list is
	// dropped before it (a list may not carry an empty scope), so the empty
	// case is asserted against ValidateScope directly.
	if err := ValidateScope(""); err == nil {
		t.Error("an empty scope must be refused")
	}
	for _, scope := range []string{"*", "team/*", "team//alpha", "/fleet", "team/"} {
		if _, err := NewPeer(principal.MustParse("navi:x"), WithScopes(scope)); err == nil {
			t.Errorf("scope %q must be refused", scope)
		}
	}
	// A blank entry is dropped, not an error: the declaration is simply empty.
	p, err := NewPeer(principal.MustParse("navi:x"), WithScopes(""))
	if err != nil {
		t.Fatalf("a blank scope entry must be dropped, not refused: %v", err)
	}
	if len(p.Scopes()) != 0 {
		t.Fatalf("Scopes() = %v, want empty", p.Scopes())
	}
}

// TestScopeMatchesBySegmentPrefix pins the namespace-prefix rule: `team/alpha`
// covers `team/alpha` and `team/alpha/notices`, but NOT `team/alphabet` — the
// boundary is a segment, not a byte, which is what keeps a namespace from
// becoming an accidental wildcard.
func TestScopeMatchesBySegmentPrefix(t *testing.T) {
	cases := []struct {
		namespace, name string
		want            bool
	}{
		{"team/alpha", "team/alpha", true},
		{"team/alpha", "team/alpha/notices", true},
		{"team/alpha", "team/alphabet", false},
		{"fleet", "fleet", true},
		{"fleet", "fleet/notices", true},
		{"fleet", "fleetwood", false},
		{"team/alpha", "", false},
		{"", "team/alpha", false},
	}
	for _, c := range cases {
		if got := ScopeMatches(c.namespace, c.name); got != c.want {
			t.Errorf("ScopeMatches(%q, %q) = %v, want %v", c.namespace, c.name, got, c.want)
		}
	}
}

// TestPeerCapabilitiesAreSeparateGrants pins ADR 0031 decision 7's independence
// rule: `sub` on a namespace does not imply `pub` on it, and neither implies
// `send` to the peer. The three verbs are distinct capabilities at independent
// granularity.
func TestPeerCapabilitiesAreSeparateGrants(t *testing.T) {
	subOnly := CapabilitySet{Allow: Allows(Sub("team/alpha"))}
	if !subOnly.Permits(Sub("team/alpha/notices"), RequestContext{}) {
		t.Error("sub on a namespace must cover a topic under it")
	}
	if subOnly.Permits(Pub("team/alpha/notices"), RequestContext{}) {
		t.Error("sub must NOT imply pub — they are separate grants")
	}
	if subOnly.Permits(Send("navi:bastilli"), RequestContext{}) {
		t.Error("sub must NOT imply send")
	}
}

// TestCeilingDerivesPeerCapabilities pins that declaring a peer puts send:,
// pub: and sub: inside the derived ceiling — and that a topic outside every
// declared scope stays outside it (the referential rule, by construction).
func TestCeilingDerivesPeerCapabilities(t *testing.T) {
	peer, err := NewPeer(principal.MustParse("navi:bastilli"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	c := CeilingFromDeclarations(nil, nil, []*Peer{peer})

	for _, want := range []Capability{
		Send("navi:bastilli"),
		Pub("team/alpha"),
		Sub("team/alpha"),
		Pub("team/alpha/notices"), // a namespace grant covers a topic under it
		Sub("team/alpha/notices"),
	} {
		if !c.Permits(want, RequestContext{}) {
			t.Errorf("%s must be inside the derived ceiling", want)
		}
	}
	for _, outside := range []Capability{
		Send("navi:someone-else"), // an undeclared peer
		Pub("team/beta"),          // an undeclared scope
		Sub("fleet"),
		Pub("team/alphabet"), // a prefix, not a segment boundary
	} {
		if c.Permits(outside, RequestContext{}) {
			t.Errorf("%s must be outside the derived ceiling", outside)
		}
	}
}

// TestPeerlessCeilingHasNoPeerCapabilities pins the fail-closed default: a navi
// with no peers derives no peer capabilities at all, so a peerless navi's
// ceiling is exactly what it was before this slice.
func TestPeerlessCeilingHasNoPeerCapabilities(t *testing.T) {
	c := CeilingFromDeclarations(nil, nil, nil)
	for _, outside := range []Capability{Send("navi:anyone"), Pub("fleet"), Sub("fleet")} {
		if c.Permits(outside, RequestContext{}) {
			t.Errorf("%s must be outside a peerless ceiling", outside)
		}
	}
}

// TestPeerCapabilityValidateRejectsBadTargets pins the shape checks: pub/sub
// refuse a wildcard segment, send refuses a non-navi target.
func TestPeerCapabilityValidateRejectsBadTargets(t *testing.T) {
	for _, s := range []string{"pub:team/*", "sub:team//alpha", "send:user:alice", "pub:", "send:"} {
		if _, err := ParseCapability(s); err == nil {
			t.Errorf("ParseCapability(%q) must fail", s)
		}
	}
	for _, s := range []string{"pub:team/alpha", "sub:fleet", "send:navi:bastilli", "pub:*", "sub:*"} {
		if _, err := ParseCapability(s); err != nil {
			t.Errorf("ParseCapability(%q) must succeed: %v", s, err)
		}
	}
}

// TestWithPeersDeclaresTheRoster pins the wiring: WithPeers appends to the
// collection, in declaration order, and the collection is revalidated at Run.
func TestWithPeersDeclaresTheRoster(t *testing.T) {
	clearProviderEnv(t)
	bastilli, err := NewPeer(principal.MustParse("navi:bastilli"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	tutore, err := NewPeer(principal.MustParse("navi:tutore"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	n := testNavi(t, WithPeers(bastilli, tutore))
	if got := n.Peers(); len(got) != 2 {
		t.Fatalf("Peers() = %d, want 2", len(got))
	}
	if n.Peers()[0].Ref() != principal.MustParse("navi:bastilli") {
		t.Fatal("declaration order must be preserved")
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestPeersAreOptional pins the default: a navi with no WithPeers has no
// roster, exactly as before this slice.
func TestPeersAreOptional(t *testing.T) {
	clearProviderEnv(t)
	n := testNavi(t)
	if got := n.Peers(); len(got) != 0 {
		t.Fatalf("Peers() = %d, want 0", len(got))
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestPeerObjectTypeRendersTheRoster is the slice's headline test: the declared
// roster is readable through the OBJECT SURFACE, with no transport in the
// picture. It would fail without the `peer` type registration — the surface
// would report ErrUnknownType.
func TestPeerObjectTypeRendersTheRoster(t *testing.T) {
	clearProviderEnv(t)
	bastilli, err := NewPeer(principal.MustParse("navi:bastilli"),
		WithAddrs("/ip4/10.0.0.51/udp/4001/quic-v1"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	n := New("roster", WithPlanCloseCheck(false),
		WithObjectAuthorizer(portal.AllowAll(), principal.MustParse("service:test")),
		WithPeers(bastilli))
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// The type is declared on the surface.
	types, err := n.objects.Types()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ty := range types {
		if ty.Name == TypePeer {
			found = true
			if len(ty.Actions) != 0 {
				t.Error("the peer type must declare no actions — the roster is read-only")
			}
		}
	}
	if !found {
		t.Fatal("the peer type is not exposed on the surface")
	}

	// The roster reads through the surface.
	objs, _, err := n.objects.List(TypePeer, object.Query{})
	if err != nil {
		t.Fatalf("List(peer): %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("roster = %d objects, want 1", len(objs))
	}
	got := objs[0]
	if got.ID != "navi:bastilli" {
		t.Fatalf("peer id = %q", got.ID)
	}
	if got.Fields["name"] != "bastilli" {
		t.Fatalf("peer name = %v", got.Fields["name"])
	}
	if got.Fields["ref"] != "navi:bastilli" {
		t.Fatalf("peer ref = %v", got.Fields["ref"])
	}
	scopes, _ := got.Fields["scopes"].([]string)
	if len(scopes) != 1 || scopes[0] != "team/alpha" {
		t.Fatalf("peer scopes = %v", got.Fields["scopes"])
	}

	// Get by reference works too.
	one, err := n.objects.Get(TypePeer, "navi:bastilli")
	if err != nil {
		t.Fatalf("Get(peer): %v", err)
	}
	if one.Fields["name"] != "bastilli" {
		t.Fatalf("Get peer name = %v", one.Fields["name"])
	}
}

// TestPeerTypeIsAbsentWithoutPeers pins the fail-closed direction: a peerless
// navi does not expose an (always empty) peer type. Adding a peer network to a
// navi that declared none is exactly what ADR 0031 decision 3 forbids.
func TestPeerTypeIsAbsentWithoutPeers(t *testing.T) {
	clearProviderEnv(t)
	n := New("peerless", WithPlanCloseCheck(false),
		WithObjectAuthorizer(portal.AllowAll(), principal.MustParse("service:test")))
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, _, err := n.objects.List(TypePeer, object.Query{}); err == nil {
		t.Fatal("a peerless navi must not expose the peer type")
	}
}

// TestDeclaredPeersReachTheCompiledPolicy pins the end-to-end wiring: a peer
// declared at composition puts send:/pub:/sub: inside the COMPILED policy's
// ceiling, so a role may grant them. This is the property that makes the
// declaration load-bearing rather than decorative.
func TestDeclaredPeersReachTheCompiledPolicy(t *testing.T) {
	clearProviderEnv(t)
	peer, err := NewPeer(principal.MustParse("navi:bastilli"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	n := testNavi(t, WithPeers(peer))
	if err := n.CompilePolicy(); err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	pol := n.Policy()
	if pol == nil {
		t.Fatal("no policy compiled")
	}
	if !pol.Ceiling().Permits(Send("navi:bastilli"), RequestContext{}) {
		t.Fatal("send to a declared peer must be inside the compiled ceiling")
	}
	if !pol.Ceiling().Permits(Pub("team/alpha/notices"), RequestContext{}) {
		t.Fatal("pub to a declared scope must be inside the compiled ceiling")
	}
}
