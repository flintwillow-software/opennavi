package navi

import (
	"context"
	"fmt"
	"strings"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/flintwillow-software/opennavi/connector"
)

// This file is the navi's peer host (NAVI-629 piece 3, ADR 0041 decision 1;
// ADR 0044): ONE libp2p host per navi, built from the navi's own identity and
// started by serve, following the webhookListener precedent (navi/webhook.go).
//
// It is deliberately minimal. ADR 0044 decision 5 scopes the first host to
// QUIC + TCP and nothing else: no pnet, no circuit relay, no DCUtR, no
// rendezvous, no pubsub. The host LISTENS and knows its own PeerID; discovery,
// relay, and the peer protocol are later slices (ADR 0031 decisions 3-6,
// NAVI-412).
//
// The three layers stay separate, which is the whole point of ADR 0044
// decision 2: WithPeers declares the ROSTER (who you can reach), WithPeerHost
// declares the HOST (that you are reachable). A navi may have either without
// the other.

// WithPeerHost declares the navi's peer host on addr, a libp2p multiaddress
// (e.g. "/ip4/0.0.0.0/udp/4001/quic-v1" or "/ip4/127.0.0.1/tcp/4001"). It
// builds the libp2p host from the navi's own identity (WithIdentity) and
// starts it from serve.
//
// The ADDRESS enables the host, not the roster (ADR 0044 decision 4): declaring
// peers with WithPeers does NOT open a socket, and an empty addr is no host —
// the fail-closed default. This is ADR 0031 decision 3's opt-in rule applied to
// reachability, and it means a navi that only wants the roster on the object
// surface pays nothing for it.
//
// The host requires an identity (WithIdentity): the navi's Ed25519 principal
// key IS its libp2p PeerID (ADR 0031 decision 2), so a host with no identity is
// a configuration error, reported at assembly rather than at the first
// connection.
//
// One host carries every transport (ADR 0042 decision 10): this option takes
// one address in the first host, but the host is one PeerID and one connection
// gater, not a listener per transport. The QUIC and TCP listeners are both
// bound from the declared address.
func WithPeerHost(addr string) Option {
	return func(n *Navi) { n.peerHostAddr = strings.TrimSpace(addr) }
}

// peerHost is the navi's one libp2p host. It is a connector.Connector so it
// composes, starts, stops, and reports health exactly like every other door —
// but it serves the navi's PEER transport rather than work of its own, which is
// why the navi builds it and never an author.
type peerHost struct {
	addr string
	id   *Identity

	h host.Host
}

// Name identifies the host in diagnostics and the health view.
func (h *peerHost) Name() string { return "peers" }

// Addr reports the configured listen address.
func (h *peerHost) Addr() string { return h.addr }

// PeerID reports the host's libp2p PeerID as a string, or "" when the host is
// not started. It is the navi's own PeerID — the one an operator pins out of
// band so a peer can be trusted before TOFU (ADR 0042 decision 3).
func (h *peerHost) PeerID() string {
	if h.h == nil {
		return ""
	}
	return h.h.ID().String()
}

// ListenAddrs reports the multiaddresses the host is actually listening on
// after Start, or nil when it is not serving. It is the diagnostic that makes
// "where can this navi be reached?" answerable from the running host rather
// than from the declared address (which may name port 0).
func (h *peerHost) ListenAddrs() []string {
	if h.h == nil {
		return nil
	}
	addrs := h.h.Addrs()
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// Host returns the underlying libp2p host, or nil before Start. It is the seam
// a later slice (the peer protocol, NAVI-412) builds streams and pubsub on, and
// the seam a test asserts a connection through. It is unexported to the package
// on purpose: the host is the navi's, and a component reaches the peer network
// through the navi rather than by holding a libp2p host (ADR 0004).
func (h *peerHost) Host() host.Host { return h.h }

// Start builds the libp2p host from the navi's identity and binds the declared
// address. Building here rather than at assembly is the webhookListener
// discipline: a bind or key failure is a Start failure, reported through the
// same health path as every other door, rather than a surprise at the first
// connection.
func (h *peerHost) Start(ctx context.Context, deps connector.Deps) error {
	key, err := h.privKey()
	if err != nil {
		return err
	}
	// QUIC + TCP only (ADR 0044 decision 5). NoTransports clears the default
	// set (which also includes WebSocket, WebTransport, and relay); the two
	// explicit transports are what this host runs. DisableRelay is required
	// because libp2p enables circuit relay by default and ADR 0044 decision 5
	// defers it.
	opts := []libp2p.Option{
		libp2p.Identity(key),
		libp2p.NoTransports,
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.DisableRelay(),
		libp2p.ListenAddrStrings(h.addr),
	}
	host, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("navi: peer host: %w", err)
	}
	h.h = host
	return nil
}

// Stop shuts the host down with the run, closing its listeners and connections.
func (h *peerHost) Stop() error {
	if h.h == nil {
		return nil
	}
	err := h.h.Close()
	h.h = nil
	return err
}

// privKey converts the navi's Ed25519 identity key into the libp2p crypto key
// the host is built from. The conversion is the identity binding ADR 0031
// decision 2 requires: the same key yields the same PeerID our stdlib-only
// Identity.PeerID reports (identity_test.go cross-checks the two), so a peer
// that learns the navi's PeerID has learned a fact about its principal key.
func (h *peerHost) privKey() (crypto.PrivKey, error) {
	if h.id == nil {
		return nil, fmt.Errorf("%w: WithPeerHost requires WithIdentity (the navi's key IS its PeerID)", ErrNoIdentity)
	}
	key, err := crypto.UnmarshalEd25519PrivateKey(h.id.priv)
	if err != nil {
		return nil, fmt.Errorf("navi: peer host key: %w", err)
	}
	return key, nil
}

// peerHostFor builds the host a navi declared, or nil when it declared none.
// It returns an error when a host was declared without an identity, because a
// host with no key has no PeerID and would fail at Start anyway — failing at
// assembly names the missing declaration rather than the symptom.
func (n *Navi) peerHostFor() (*peerHost, error) {
	addr := strings.TrimSpace(n.peerHostAddr)
	if addr == "" {
		return nil, nil
	}
	if n.identity == nil {
		return nil, fmt.Errorf("%w: WithPeerHost(%q) requires WithIdentity", ErrNoIdentity, addr)
	}
	return &peerHost{addr: addr, id: n.identity}, nil
}

// PeerHostID reports the running peer host's PeerID, or "" when the navi runs
// no host. It is the operator-facing answer to "what is this navi's PeerID?",
// which is the fact to pin out of band before trusting the peer (ADR 0042
// decision 3).
func (n *Navi) PeerHostID() string {
	if n.peerHost == nil {
		return ""
	}
	return n.peerHost.PeerID()
}

// PeerHostAddrs reports the addresses the running peer host is listening on,
// or nil when the navi runs no host. A declared address of port 0 is resolved
// here to the port actually bound, which is what a test needs.
func (n *Navi) PeerHostAddrs() []string {
	if n.peerHost == nil {
		return nil
	}
	return n.peerHost.ListenAddrs()
}

// compile-time checks.
var (
	_ connector.Connector = (*peerHost)(nil)
	_ fmt.Stringer        = (*peerHost)(nil)
)

// String names the host and its address for logs.
func (h *peerHost) String() string {
	if id := h.PeerID(); id != "" {
		return fmt.Sprintf("peers(%s, %s)", h.addr, id)
	}
	return fmt.Sprintf("peers(%s)", h.addr)
}
