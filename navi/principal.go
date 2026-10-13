package navi

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the principal model of ADR 0021 decisions 1-3: a principal is
// an identity (a principal.Ref) with authenticators attached. It is core navi,
// not a component contract — a component only ever sees a reference (the
// principal package); the identity, its keys, and the trust that resolves it
// live here. Ed25519 is the first authenticator (services and peer navis);
// the Authenticator interface is the seam an OIDC session (ADR 0021 decision
// 8) and a keyed trust store (decision 6) attach through, so neither reshapes
// the principal.
//
// Nothing here grants capability. A verified authenticator establishes *who*
// acted; the compiled policy that decides what that identity may do arrives
// with the policy engine (NAVI-399). Trust mutation is a build-time
// declaration or an operator action, never a runtime path an agent can reach
// (ADR 0006, ADR 0013, ADR 0021 decision 3).

// Errors reported by the principal model. Callers test with errors.Is.
var (
	// ErrInvalidPrincipal is a zero or malformed principal reference.
	ErrInvalidPrincipal = errors.New("navi: invalid principal")
	// ErrUnknownPrincipal is a reference no trust source resolves. Lookup
	// fails closed with it rather than returning a zero principal.
	ErrUnknownPrincipal = errors.New("navi: unknown principal")
	// ErrUnauthenticated is a credential that did not verify, or a
	// principal with no usable key. It is the fail-closed answer.
	ErrUnauthenticated = errors.New("navi: unauthenticated")
	// ErrInvalidAuthenticator is a malformed key (empty id, wrong key
	// length, an inverted validity period).
	ErrInvalidAuthenticator = errors.New("navi: invalid authenticator")
	// ErrUnknownAuthenticator is a rotation that names a key the principal
	// does not hold.
	ErrUnknownAuthenticator = errors.New("navi: unknown authenticator")
	// ErrDuplicateAuthenticator is a second key with an id the principal
	// already holds.
	ErrDuplicateAuthenticator = errors.New("navi: duplicate authenticator")
	// ErrDuplicatePrincipal is a second declaration of a principal the set
	// already holds.
	ErrDuplicatePrincipal = errors.New("navi: duplicate principal")
	// ErrDuplicateMember is a principal added to a group twice.
	ErrDuplicateMember = errors.New("navi: duplicate group member")
	// ErrInvalidRole is an unnamed role.
	ErrInvalidRole = errors.New("navi: invalid role")
)

// Authenticator is a credential attached to a principal (ADR 0021 decisions
// 1 and 8). The key system is pluggable: Ed25519Key is the first
// implementation, for services and peer navis. An OIDC session (decision 8,
// the human authenticator) and a keyed trust store (decision 6) implement
// this interface without changing Principal.
//
// An authenticator verifies a proof and carries a validity period. It never
// grants capability: verification establishes identity, and compiled policy
// decides what that identity may do (ADR 0006, ADR 0021 decision 3).
type Authenticator interface {
	// ID is a stable identifier for this key. Rotation adds and removes
	// keys by ID, so identity does not change when a key does.
	ID() string
	// Type names the credential type ("ed25519", "oidc", ...).
	Type() string
	// ValidAt reports whether the credential is within its validity period
	// at t. Expiry and revocation are runtime narrowing (ADR 0021
	// decision 8).
	ValidAt(t time.Time) bool
	// Verify reports nil when proof authenticates challenge under this key.
	// A non-nil error means the proof did not verify or the key is not
	// valid at now. An authenticator that carries its own claims (an OIDC
	// token) may treat challenge as a nonce it must bind, and must document
	// what it binds.
	Verify(challenge, proof []byte, now time.Time) error
}

// Ed25519Key is an Ed25519 public-key authenticator (ADR 0021 decision 8):
// services and peer navis present a keypair. Only the public half is held;
// the private half never enters the navi.
type Ed25519Key struct {
	id        string
	pub       ed25519.PublicKey
	notBefore time.Time
	notAfter  time.Time
}

// KeyOption adjusts an Ed25519Key at construction.
type KeyOption func(*Ed25519Key)

// KeyValidFrom sets the start of the key's validity period (zero = no start
// bound). A key outside its period never verifies.
func KeyValidFrom(t time.Time) KeyOption { return func(k *Ed25519Key) { k.notBefore = t } }

// KeyValidUntil sets the end of the key's validity period (zero = never
// expires). A key outside its period never verifies.
func KeyValidUntil(t time.Time) KeyOption { return func(k *Ed25519Key) { k.notAfter = t } }

// NewEd25519Key builds an Ed25519 authenticator from a public key. id must be
// non-empty and pub must be a valid Ed25519 public key.
func NewEd25519Key(id string, pub ed25519.PublicKey, opts ...KeyOption) (*Ed25519Key, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: ed25519 key has an empty id", ErrInvalidAuthenticator)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: ed25519 key %s is %d bytes, want %d",
			ErrInvalidAuthenticator, id, len(pub), ed25519.PublicKeySize)
	}
	k := &Ed25519Key{id: id, pub: append(ed25519.PublicKey(nil), pub...)}
	for _, opt := range opts {
		opt(k)
	}
	if !k.notBefore.IsZero() && !k.notAfter.IsZero() && k.notAfter.Before(k.notBefore) {
		return nil, fmt.Errorf("%w: ed25519 key %s expires before it starts", ErrInvalidAuthenticator, id)
	}
	return k, nil
}

// ID implements Authenticator.
func (k *Ed25519Key) ID() string { return k.id }

// Type implements Authenticator.
func (k *Ed25519Key) Type() string { return "ed25519" }

// ValidAt implements Authenticator.
func (k *Ed25519Key) ValidAt(t time.Time) bool {
	if !k.notBefore.IsZero() && t.Before(k.notBefore) {
		return false
	}
	if !k.notAfter.IsZero() && t.After(k.notAfter) {
		return false
	}
	return true
}

// PublicKey returns a copy of the held public key.
func (k *Ed25519Key) PublicKey() ed25519.PublicKey { return append(ed25519.PublicKey(nil), k.pub...) }

// Verify implements Authenticator: proof must be a valid Ed25519 signature
// over challenge by this key's public half, and the key must be valid at now.
func (k *Ed25519Key) Verify(challenge, proof []byte, now time.Time) error {
	if !k.ValidAt(now) {
		return fmt.Errorf("%w: ed25519 key %s is outside its validity period", ErrUnauthenticated, k.id)
	}
	if !ed25519.Verify(k.pub, challenge, proof) {
		return fmt.Errorf("%w: ed25519 key %s", ErrUnauthenticated, k.id)
	}
	return nil
}

// Principal is an identity with authenticators attached (ADR 0021 decision 1).
// The identity is the Ref; the keys are a mutable set, so rotation and
// revocation never change identity. Claims and group membership are identity
// data a trust source supplied (ADR 0021 decision 4): they are never policy.
type Principal struct {
	ref principal.Ref

	// claims and groups are supplied by a trust source at resolution and are
	// immutable afterwards, so they are read without the key mutex.
	claims map[string]string
	groups []principal.Ref

	mu   sync.RWMutex
	keys []Authenticator
}

// NewPrincipal builds a principal for ref and attaches each key. A zero or
// malformed ref, a nil key, or two keys sharing an id is refused.
func NewPrincipal(ref principal.Ref, keys ...Authenticator) (*Principal, error) {
	return NewResolvedPrincipal(ref, nil, nil, keys...)
}

// NewResolvedPrincipal builds a principal with the identity data a trust
// source resolved: claims and group membership alongside the keys (ADR 0021
// decision 4). It is the constructor trust sources use; an author declaring a
// principal inline uses NewPrincipal.
func NewResolvedPrincipal(ref principal.Ref, claims map[string]string, groups []principal.Ref, keys ...Authenticator) (*Principal, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	p := &Principal{ref: ref, claims: cloneClaims(claims)}
	for _, group := range groups {
		if err := group.Validate(); err != nil {
			return nil, fmt.Errorf("%w: group: %w", ErrInvalidPrincipal, err)
		}
		p.groups = append(p.groups, group)
	}
	for _, key := range keys {
		if err := p.Attach(key); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Ref returns the principal's identity.
func (p *Principal) Ref() principal.Ref { return p.ref }

// Claims returns a copy of the identity claims a trust source supplied; nil
// when there are none. Claims are identity data, never policy (ADR 0021
// decision 4).
func (p *Principal) Claims() map[string]string { return cloneClaims(p.claims) }

// Groups returns a copy of the group references the principal belongs to, as
// the trust source supplied them (ADR 0021 decision 4).
func (p *Principal) Groups() []principal.Ref { return append([]principal.Ref(nil), p.groups...) }

// InGroup reports whether the trust source placed the principal in group.
func (p *Principal) InGroup(group principal.Ref) bool {
	for _, g := range p.groups {
		if g == group {
			return true
		}
	}
	return false
}

// cloneClaims copies a claims map so a caller cannot mutate a principal's
// identity through the map it passed or received.
func cloneClaims(claims map[string]string) map[string]string {
	if len(claims) == 0 {
		return nil
	}
	out := make(map[string]string, len(claims))
	for k, v := range claims {
		out[k] = v
	}
	return out
}

// Keys returns a snapshot of the principal's authenticators.
func (p *Principal) Keys() []Authenticator {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Authenticator(nil), p.keys...)
}

// Authenticator returns the key with the given id, if held.
func (p *Principal) Authenticator(id string) (Authenticator, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, key := range p.keys {
		if key.ID() == id {
			return key, true
		}
	}
	return nil, false
}

// Attach adds a key. Rotation adds a key without changing the principal's
// identity; a key whose id is already held is refused rather than replacing
// the existing one.
func (p *Principal) Attach(key Authenticator) error {
	if key == nil {
		return fmt.Errorf("%w: nil authenticator", ErrInvalidAuthenticator)
	}
	if strings.TrimSpace(key.ID()) == "" {
		return fmt.Errorf("%w: authenticator with an empty id", ErrInvalidAuthenticator)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, held := range p.keys {
		if held.ID() == key.ID() {
			return fmt.Errorf("%w: %s", ErrDuplicateAuthenticator, key.ID())
		}
	}
	p.keys = append(p.keys, key)
	return nil
}

// Detach removes the key with the given id — rotation and revocation remove a
// key without changing the principal's identity. An unknown id is refused.
func (p *Principal) Detach(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, held := range p.keys {
		if held.ID() != id {
			continue
		}
		p.keys = append(p.keys[:i], p.keys[i+1:]...)
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnknownAuthenticator, id)
}

// Verify reports nil when proof authenticates the principal for challenge
// under any held key valid at now. A principal with no usable key is
// unauthenticated — never defaulted open.
func (p *Principal) Verify(challenge, proof []byte, now time.Time) error {
	keys := p.Keys()
	if len(keys) == 0 {
		return fmt.Errorf("%w: principal %s holds no authenticators", ErrUnauthenticated, p.ref)
	}
	var last error
	for _, key := range keys {
		if err := key.Verify(challenge, proof, now); err != nil {
			last = err
			continue
		}
		return nil
	}
	return fmt.Errorf("%w: principal %s: %w", ErrUnauthenticated, p.ref, last)
}

// Group is a named collection of principals, optionally authenticatable
// itself (ADR 0021 decision 2). A group used as a policy subject attributes
// the action to the group rather than to a member — weaker attribution,
// accepted rather than hidden.
type Group struct {
	ref principal.Ref

	mu      sync.RWMutex
	members []principal.Ref
	shared  *Principal
}

// NewGroup builds a group from a group-kind reference and its members.
func NewGroup(ref principal.Ref, members ...principal.Ref) (*Group, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	if ref.Kind() != principal.KindGroup {
		return nil, fmt.Errorf("%w: %s is not a group reference", ErrInvalidPrincipal, ref)
	}
	g := &Group{ref: ref}
	for _, member := range members {
		if err := g.Add(member); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// Ref returns the group's reference.
func (g *Group) Ref() principal.Ref { return g.ref }

// Members returns a snapshot of the group's member references.
func (g *Group) Members() []principal.Ref {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]principal.Ref(nil), g.members...)
}

// Add puts a principal in the group. The reference must name a principal and
// not be the group itself; adding a member twice is refused.
func (g *Group) Add(ref principal.Ref) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	if ref == g.ref {
		return fmt.Errorf("%w: %s cannot be a member of itself", ErrInvalidPrincipal, ref)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, held := range g.members {
		if held == ref {
			return fmt.Errorf("%w: %s is already a member of %s", ErrDuplicateMember, ref, g.ref)
		}
	}
	g.members = append(g.members, ref)
	return nil
}

// Remove takes a principal out of the group; an unknown member is refused.
func (g *Group) Remove(ref principal.Ref) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, held := range g.members {
		if held != ref {
			continue
		}
		g.members = append(g.members[:i], g.members[i+1:]...)
		return nil
	}
	return fmt.Errorf("%w: %s is not a member of %s", ErrUnknownPrincipal, ref, g.ref)
}

// Shared returns the group's own authenticatable identity, or nil when the
// group is a policy subject only (ADR 0021 decision 2).
func (g *Group) Shared() *Principal {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.shared
}

// SetShared makes the group authenticatable itself — a shared identity with
// its own keys — and returns that principal. The group's reference is the
// shared principal's identity, so an action authenticated through it
// attributes to the group.
func (g *Group) SetShared(keys ...Authenticator) (*Principal, error) {
	p, err := NewPrincipal(g.ref, keys...)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.shared = p
	g.mu.Unlock()
	return p, nil
}

// Role is a compiled capability set (ADR 0021 decisions 2 and 3). It is NOT a
// principal: it holds no identity and no authenticators, and a trust source
// never resolves one. The capability vocabulary and the compiled policy that
// maps a role to capability arrive with the policy engine (NAVI-399); this
// type fixes the vocabulary so a role can never be mistaken for a principal.
type Role struct{ name string }

// NewRole names a compiled role.
func NewRole(name string) (Role, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return Role{}, fmt.Errorf("%w: role has an empty name", ErrInvalidRole)
	}
	return Role{name: n}, nil
}

// Name returns the role's name.
func (r Role) Name() string { return r.name }

// IsZero reports whether r names no role.
func (r Role) IsZero() bool { return r == Role{} }

// TrustSource resolves principals at runtime (ADR 0021 decisions 4 and 6). A
// navi declares where trust comes from at compile time; the source resolves
// the contents at runtime. Resolution never introduces a capability or a
// policy (decisions 4 and 7): it returns an identity, and compiled policy
// decides what that identity may do. Inline declarations, an OIDC identity
// provider, and a keyed trust store are the declared forms (decision 6); the
// first concrete sources arrive with NAVI-401, and this interface is the seam.
type TrustSource interface {
	// Resolve returns the principal for ref. An unknown principal is an
	// error (fail closed), never a zero principal.
	Resolve(ref principal.Ref) (*Principal, error)
}

// Principals is a compiled set of declared principals: the inline form of a
// trust source (ADR 0021 decision 6). It is populated at build time and read
// at runtime, and its lookup fails closed for a reference it does not hold.
// It satisfies TrustSource.
type Principals struct {
	mu    sync.RWMutex
	byRef map[principal.Ref]*Principal
}

// NewPrincipals builds a set from declared principals.
func NewPrincipals(principals ...*Principal) (*Principals, error) {
	ps := &Principals{byRef: map[principal.Ref]*Principal{}}
	for _, p := range principals {
		if err := ps.Add(p); err != nil {
			return nil, err
		}
	}
	return ps, nil
}

// Add declares a principal. A nil principal, a zero reference, or a reference
// already declared is refused — a declaration never silently replaces one.
func (ps *Principals) Add(p *Principal) error {
	if p == nil {
		return fmt.Errorf("%w: nil principal", ErrInvalidPrincipal)
	}
	if err := p.Ref().Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, ok := ps.byRef[p.Ref()]; ok {
		return fmt.Errorf("%w: %s is already declared", ErrDuplicatePrincipal, p.Ref())
	}
	ps.byRef[p.Ref()] = p
	return nil
}

// Resolve implements TrustSource: it returns the declared principal for ref,
// or ErrUnknownPrincipal. A zero reference is refused rather than defaulted.
func (ps *Principals) Resolve(ref principal.Ref) (*Principal, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnknownPrincipal, err)
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	p, ok := ps.byRef[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPrincipal, ref)
	}
	return p, nil
}

// Refs returns the declared references, sorted for a deterministic surface.
func (ps *Principals) Refs() []principal.Ref {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]principal.Ref, 0, len(ps.byRef))
	for ref := range ps.byRef {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// Compile-time checks: the compiled inline set is a trust source.
var _ TrustSource = (*Principals)(nil)
