package navi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file gates the navi's peer host (NAVI-629 piece 3, ADR 0041 decision 1;
// ADR 0044). The properties that matter: the host is enabled by the ADDRESS and
// not by the roster, its PeerID IS the navi identity's PeerID, two navis get
// distinct PeerIDs, and a real libp2p connection between two hosts shows up as
// a connected peer in each.

// startPeerHost builds and starts a navi's declared peer host the way serve
// does, and stops it at test end. It returns the running host.
func startPeerHost(t *testing.T, n *Navi, addr string) *peerHost {
	t.Helper()
	if err := n.assembleIdentity(); err != nil {
		t.Fatalf("assembleIdentity: %v", err)
	}
	ph, err := n.peerHostFor()
	if err != nil {
		t.Fatalf("peerHostFor: %v", err)
	}
	if ph == nil {
		t.Fatalf("no peer host built for addr %q", addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ph.Start(ctx, connector.Deps{}); err != nil {
		t.Fatalf("peer host start: %v", err)
	}
	t.Cleanup(func() { _ = ph.Stop() })
	n.peerHost = ph
	return ph
}

// TestPeerHostIsEnabledByTheAddressNotTheRoster pins ADR 0044 decision 4: a
// navi that declares a ROSTER but no host starts nothing, and a navi that
// declares a host address gets one. Listing is not reachability.
func TestPeerHostIsEnabledByTheAddressNotTheRoster(t *testing.T) {
	clearProviderEnv(t)
	peer, err := NewPeer(principal.MustParse("navi:bastilli"), WithScopes("team/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	// Roster, no host address: no host.
	rosterOnly := testNavi(t, WithPeers(peer))
	ph, err := rosterOnly.peerHostFor()
	if err != nil {
		t.Fatalf("peerHostFor: %v", err)
	}
	if ph != nil {
		t.Fatal("declaring a roster must not build a host — listing is not reachability")
	}

	// Host address: a host.
	withHost := testNavi(t, WithIdentity(filepath.Join(t.TempDir(), "id.pem")),
		WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	started := startPeerHost(t, withHost, "/ip4/127.0.0.1/tcp/0")
	if started.PeerID() == "" {
		t.Fatal("a started host must report a PeerID")
	}
}

// TestPeerHostRequiresAnIdentity pins the fail-closed wiring: a declared host
// with no identity is an error at assembly, naming the missing declaration
// rather than failing later at the first connection.
func TestPeerHostRequiresAnIdentity(t *testing.T) {
	clearProviderEnv(t)
	n := testNavi(t, WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	if err := n.assembleIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := n.peerHostFor(); err == nil {
		t.Fatal("WithPeerHost without WithIdentity must be an error")
	}
}

// TestPeerHostIDIsTheNaviPeerID pins the identity binding of ADR 0031 decision
// 2: the running libp2p host's PeerID is EXACTLY the string our stdlib-only
// Identity.PeerID() reports. If these ever disagree, the navi has two network
// identities, which is the thing the record forbids.
func TestPeerHostIDIsTheNaviPeerID(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "id.pem")
	n := testNavi(t, WithIdentity(path), WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	ph := startPeerHost(t, n, "/ip4/127.0.0.1/tcp/0")

	want := n.Identity().PeerID()
	if got := ph.PeerID(); got != want {
		t.Fatalf("host PeerID = %s, identity PeerID = %s — they must be the same key", got, want)
	}
	if !strings.HasPrefix(ph.PeerID(), "12D3KooW") {
		t.Fatalf("PeerID = %q, want the Ed25519 12D3KooW prefix", ph.PeerID())
	}
}

// TestPeerHostStartsWithListeners pins that the host actually binds the
// declared address: a started host reports at least one listen address, so a
// test (or an operator) can learn the port when the declaration used port 0.
func TestPeerHostStartsWithListeners(t *testing.T) {
	clearProviderEnv(t)
	n := testNavi(t, WithIdentity(filepath.Join(t.TempDir(), "id.pem")),
		WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	ph := startPeerHost(t, n, "/ip4/127.0.0.1/tcp/0")
	if addrs := ph.ListenAddrs(); len(addrs) == 0 {
		t.Fatal("a started host must report at least one listen address")
	}
}

// TestTwoNavisConnectAndSeeEachOther is the slice's headline test and the
// NAVI-629 verification bar: two navis with distinct identities and distinct
// PeerIDs establish a real libp2p connection, and the connection is visible as
// a connected peer in each. It would fail without the host — there would be
// nothing to dial.
func TestTwoNavisConnectAndSeeEachOther(t *testing.T) {
	clearProviderEnv(t)
	dir := t.TempDir()

	// Two navis, each its own identity and its own host on loopback.
	a := testNavi(t, WithIdentity(filepath.Join(dir, "a.pem")), WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	b := testNavi(t, WithIdentity(filepath.Join(dir, "b.pem")), WithPeerHost("/ip4/127.0.0.1/tcp/0"))
	ha := startPeerHost(t, a, "/ip4/127.0.0.1/tcp/0")
	hb := startPeerHost(t, b, "/ip4/127.0.0.1/tcp/0")

	// Distinct identities, distinct PeerIDs — the property the roster depends on.
	if ha.PeerID() == hb.PeerID() {
		t.Fatal("two navis share a PeerID")
	}

	// B dials A, the way a declared peer would.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info := peer.AddrInfo{ID: ha.Host().ID(), Addrs: ha.Host().Addrs()}
	if err := hb.Host().Connect(ctx, info); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Each host now sees the other connected.
	if got := hb.Host().Network().Connectedness(ha.Host().ID()); got != network.Connected {
		t.Fatalf("B sees A as %v, want Connected", got)
	}
	if got := ha.Host().Network().Connectedness(hb.Host().ID()); got != network.Connected {
		t.Fatalf("A sees B as %v, want Connected", got)
	}
}

// TestPeerHostIsOptional pins the default: a navi with no WithPeerHost runs no
// host, exactly as before this existed — no listener, no PeerID.
func TestPeerHostIsOptional(t *testing.T) {
	clearProviderEnv(t)
	n := testNavi(t)
	if err := n.assembleIdentity(); err != nil {
		t.Fatal(err)
	}
	ph, err := n.peerHostFor()
	if err != nil {
		t.Fatalf("peerHostFor: %v", err)
	}
	if ph != nil {
		t.Fatal("a navi with no WithPeerHost must have no host")
	}
	if n.PeerHostID() != "" {
		t.Fatal("a navi with no host must report no PeerID")
	}
}
