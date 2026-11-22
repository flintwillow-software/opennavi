package navi

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the peer declaration (NAVI-629 slice 2, ADR 0031 decision 7;
// ADR 0042 decisions 2, 3 and 6): a PEER IS A REACHABILITY RECORD, declared
// at composition like a type or a tool.
//
// It is deliberately thin, and the thinness is the design:
//
//   - It holds NO KEY. The key lives in the trust store (navi/trust.go), which
//     is the one place a peer's identity is pinned and the one place ADR 0042
//     decision 3 lets trust-on-first-use write. A key on the declaration would
//     be a second, conflicting answer to "what key is this peer", which is
//     exactly the second-source mistake ADR 0021 decision 4 forbids for group
//     membership.
//
//   - It GRANTS NOTHING. What a peer may do is decided by the compiled policy
//     (navi/policy.go), re-derived from the ceiling every time. ADR 0042
//     decision 5 states the three layers plainly: being listed is not
//     permission, and permission is not reachability. A peer record that
//     carried a grant would collapse two of those layers into one.
//
// What a peer DOES carry is what makes it reachable and discoverable: its
// reference, the addresses it may be dialed at, and the scopes it publishes
// to. Scopes reuse ADR 0031 decision 7's namespace vocabulary (`team/alpha`,
// `fleet`) rather than inventing a second one, so a scope named here is the
// same string a `pub:`/`sub:` grant names in a policy.

// Peer is a declared peer's reachability record (ADR 0042 decisions 2-3): who
// it is (its principal reference), where it can be dialed, and what it
// publishes. It holds no key and grants nothing — see the file comment.
//
// A Peer is immutable once built; NewPeer validates its shape.
type Peer struct {
	ref    principal.Ref
	addrs  []string
	scopes []string
}

// NewPeer builds a peer declaration from its reference and optional address
// and scope lists.
//
// The reference must be a navi-kind principal (`navi:<name>`): a peer is
// another navi (ADR 0031 decision 2), so a user or group reference is a
// mistake worth refusing rather than a shape to tolerate.
func NewPeer(ref principal.Ref, opts ...PeerOption) (*Peer, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: peer reference: %w", ErrInvalidPrincipal, err)
	}
	if ref.Kind() != principal.KindNavi {
		return nil, fmt.Errorf("%w: peer %s is not a navi", ErrInvalidPrincipal, ref)
	}
	p := &Peer{ref: ref}
	for _, o := range opts {
		if o != nil {
			o(p)
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// MustPeer is NewPeer for a compile-time constant, panicking on a malformed
// declaration. It exists so the common `navi.MustPeer("navi:bastilli")` stays
// terse in a composition root.
func MustPeer(ref string, opts ...PeerOption) *Peer {
	parsed, err := principal.Parse(ref)
	if err != nil {
		panic(fmt.Errorf("navi: MustPeer(%q): %w", ref, err))
	}
	p, err := NewPeer(parsed, opts...)
	if err != nil {
		panic(err)
	}
	return p
}

// PeerOption adjusts a peer declaration.
type PeerOption func(*Peer)

// WithAddrs sets the addresses the peer may be dialed at. They are opaque
// multiaddress strings (ADR 0031 decision 1: libp2p), kept unparsed here so
// the declaration slice imports no transport — the host slice validates them
// when it can actually dial. An empty list is legal: a peer known only through
// discovery has no declared address (ADR 0042 decision 9, a relay answer is a
// hint, and a peer with prior knowledge can still reach it).
func WithAddrs(addrs ...string) PeerOption {
	return func(p *Peer) { p.addrs = cleanList(addrs) }
}

// WithScopes sets the scopes the peer publishes to (ADR 0042 decision 6):
// `team/alpha`, `fleet`. A scope is the SAME namespace vocabulary ADR 0031
// decision 7 gives `pub:`/`sub:` grants, so a peer that publishes to
// `team/alpha` is discoverable by exactly the grants that name `team/alpha`.
// An empty list is legal: a peer that publishes to nothing is reachable but
// not discoverable, which is the fail-closed default (ADR 0031 decision 3).
func WithScopes(scopes ...string) PeerOption {
	return func(p *Peer) { p.scopes = cleanList(scopes) }
}

// Ref is the peer's principal reference (`navi:<name>`).
func (p *Peer) Ref() principal.Ref { return p.ref }

// Name returns the peer's principal name, for a Collection key and a label.
func (p *Peer) Name() string { return p.ref.Name() }

// Addrs returns a copy of the declared addresses.
func (p *Peer) Addrs() []string { return append([]string(nil), p.addrs...) }

// Scopes returns a copy of the declared scopes.
func (p *Peer) Scopes() []string { return append([]string(nil), p.scopes...) }

// Validate checks the declaration's shape: a navi reference, and scopes and
// addresses that are well formed. It does NOT check reachability or trust —
// neither is a property of the declaration.
func (p *Peer) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: nil peer", ErrInvalid)
	}
	if err := p.ref.Validate(); err != nil {
		return fmt.Errorf("%w: peer reference: %w", ErrInvalidPrincipal, err)
	}
	if p.ref.Kind() != principal.KindNavi {
		return fmt.Errorf("%w: peer %s is not a navi", ErrInvalidPrincipal, p.ref)
	}
	for _, s := range p.scopes {
		if err := ValidateScope(s); err != nil {
			return fmt.Errorf("%w: peer %s: %w", ErrInvalid, p.ref, err)
		}
	}
	for _, a := range p.addrs {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("%w: peer %s has an empty address", ErrInvalid, p.ref)
		}
	}
	return nil
}

// String renders a peer for diagnostics: `navi:bastilli[team/alpha]`.
func (p *Peer) String() string {
	if p == nil {
		return "<nil peer>"
	}
	parts := []string{p.ref.String()}
	if len(p.scopes) > 0 {
		parts = append(parts, "["+strings.Join(p.scopes, ",")+"]")
	}
	return strings.Join(parts, "")
}

// ValidateScope checks a scope against ADR 0031 decision 7's namespace
// vocabulary: one or more non-empty slash-separated segments (`team/alpha`,
// `fleet`). A namespace is a DECLARATION, not a wildcard expression, so `*`
// and an empty segment are refused rather than treated as a pattern.
func ValidateScope(scope string) error {
	s := strings.TrimSpace(scope)
	if s == "" {
		return fmt.Errorf("scope is empty")
	}
	if s == Wildcard {
		return fmt.Errorf("scope %q is a wildcard, not a declared namespace", s)
	}
	for _, seg := range strings.Split(s, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return fmt.Errorf("scope %q has an empty segment", s)
		}
		if seg == Wildcard {
			return fmt.Errorf("scope %q has a wildcard segment", s)
		}
	}
	return nil
}

// ScopeMatches reports whether a declared namespace covers a topic or peer
// name: the namespace matches by PREFIX on segment boundaries (ADR 0031
// decision 7 — "the namespace is a declaration, not a wildcard expression").
// `team/alpha` covers `team/alpha` and `team/alpha/notices` but not
// `team/alphabet`.
func ScopeMatches(namespace, name string) bool {
	ns := strings.TrimSpace(namespace)
	n := strings.TrimSpace(name)
	if ns == "" || n == "" {
		return false
	}
	if ns == n {
		return true
	}
	return strings.HasPrefix(n, ns+"/")
}

// cleanList trims, drops blanks, and de-duplicates a declaration list while
// preserving order — so a peer declared with a repeated scope does not carry
// it twice into the ceiling.
func cleanList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		t := strings.TrimSpace(v)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ── wiring ──────────────────────────────────────────────────────────────

// Peers returns the declared peers, in declaration order.
func (n *Navi) Peers() []*Peer { return n.peerList.All() }

// WithPeers declares the navi's peers (ADR 0042 decisions 2-3): the navis this
// navi knows how to reach, and the scopes it publishes to. It is the same
// shape as every other declaration — an explicit value appended to a
// collection, visible in main.go — so the roster is a build-time fact and
// `CeilingFromDeclarations` can derive `send:`/`pub:`/`sub:` from it.
//
// A navi that declares no peers is unchanged: no roster, no `peer` type, no
// peer capabilities. The peer network is opt-in (ADR 0031 decision 3), and the
// fail-closed direction is the one that costs nothing to keep.
//
// This declares REACHABILITY only. The peer's key is not here — it belongs in
// the trust store (ADR 0042 decision 3) — and neither is any permission, which
// the compiled policy decides (ADR 0042 decision 5).
func WithPeers(peers ...*Peer) Option {
	return func(n *Navi) {
		for _, p := range peers {
			if p == nil {
				continue
			}
			n.peerList.Add(p)
		}
	}
}

// validatePeer is the collection's shape check.
func validatePeer(p *Peer) error { return p.Validate() }
