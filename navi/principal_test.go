package navi

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/principal"
)

// keypair generates fresh key material. Tests never carry a key literal
// (ADR 0008); a key is generated, used, and dropped.
func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func ed25519Key(t *testing.T, id string, opts ...KeyOption) (*Ed25519Key, ed25519.PrivateKey) {
	t.Helper()
	pub, priv := keypair(t)
	key, err := NewEd25519Key(id, pub, opts...)
	if err != nil {
		t.Fatalf("NewEd25519Key: %v", err)
	}
	return key, priv
}

func TestEd25519KeyVerifies(t *testing.T) {
	key, priv := ed25519Key(t, "k1")
	if key.Type() != "ed25519" {
		t.Errorf("Type = %q, want ed25519", key.Type())
	}
	if key.ID() != "k1" {
		t.Errorf("ID = %q, want k1", key.ID())
	}
	challenge := []byte("a challenge the peer signs")
	sig := ed25519.Sign(priv, challenge)
	if err := key.Verify(challenge, sig, time.Now()); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// A different message or a mangled signature is unauthenticated.
	if err := key.Verify([]byte("another challenge"), sig, time.Now()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Verify wrong message = %v, want ErrUnauthenticated", err)
	}
	bad := append([]byte(nil), sig...)
	bad[0] ^= 0xff
	if err := key.Verify(challenge, bad, time.Now()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Verify mangled proof = %v, want ErrUnauthenticated", err)
	}
}

func TestEd25519KeyValidity(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	expired, priv := ed25519Key(t, "old", KeyValidUntil(past))
	sig := ed25519.Sign(priv, []byte("m"))
	if expired.ValidAt(now) {
		t.Error("an expired key reports valid")
	}
	if err := expired.Verify([]byte("m"), sig, now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expired Verify = %v, want ErrUnauthenticated", err)
	}

	early, priv2 := ed25519Key(t, "new", KeyValidFrom(future))
	if err := early.Verify([]byte("m"), ed25519.Sign(priv2, []byte("m")), now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("not-yet-valid Verify = %v, want ErrUnauthenticated", err)
	}

	if _, err := NewEd25519Key("bad", make(ed25519.PublicKey, 3)); !errors.Is(err, ErrInvalidAuthenticator) {
		t.Errorf("short public key = %v, want ErrInvalidAuthenticator", err)
	}
	if _, err := NewEd25519Key("", mustPub(t)); !errors.Is(err, ErrInvalidAuthenticator) {
		t.Errorf("empty id = %v, want ErrInvalidAuthenticator", err)
	}
	if _, err := NewEd25519Key("x", mustPub(t), KeyValidFrom(future), KeyValidUntil(past)); !errors.Is(err, ErrInvalidAuthenticator) {
		t.Errorf("inverted validity = %v, want ErrInvalidAuthenticator", err)
	}
}

func mustPub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _ := keypair(t)
	return pub
}

func TestPrincipalHoldsSeveralKeys(t *testing.T) {
	k1, priv1 := ed25519Key(t, "k1")
	k2, priv2 := ed25519Key(t, "k2")
	ref := principal.MustParse("service:relay")

	p, err := NewPrincipal(ref, k1, k2)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if p.Ref() != ref {
		t.Errorf("Ref = %s, want %s", p.Ref(), ref)
	}
	if len(p.Keys()) != 2 {
		t.Fatalf("Keys = %d, want 2", len(p.Keys()))
	}
	if _, ok := p.Authenticator("k2"); !ok {
		t.Error("Authenticator(k2) not found")
	}

	// Either key authenticates the principal.
	now := time.Now()
	for i, priv := range []ed25519.PrivateKey{priv1, priv2} {
		challenge := []byte("challenge")
		if err := p.Verify(challenge, ed25519.Sign(priv, challenge), now); err != nil {
			t.Errorf("Verify with key %d: %v", i+1, err)
		}
	}

	// A key from another principal does not.
	_, otherPriv := keypair(t)
	if err := p.Verify([]byte("challenge"), ed25519.Sign(otherPriv, []byte("challenge")), now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Verify with a foreign key = %v, want ErrUnauthenticated", err)
	}
}

func TestPrincipalRotationKeepsIdentity(t *testing.T) {
	k1, priv1 := ed25519Key(t, "k1")
	ref := principal.MustParse("service:relay")
	p, err := NewPrincipal(ref, k1)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	identity := p.Ref()

	// Rotate: add a new key, remove the old one.
	k2, priv2 := ed25519Key(t, "k2")
	if err := p.Attach(k2); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if p.Ref() != identity {
		t.Errorf("identity changed on attach: %s -> %s", identity, p.Ref())
	}
	if err := p.Detach("k1"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if p.Ref() != identity {
		t.Errorf("identity changed on detach: %s -> %s", identity, p.Ref())
	}

	now := time.Now()
	challenge := []byte("challenge")
	if err := p.Verify(challenge, ed25519.Sign(priv2, challenge), now); err != nil {
		t.Errorf("rotated key does not verify: %v", err)
	}
	if err := p.Verify(challenge, ed25519.Sign(priv1, challenge), now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("revoked key still verifies: %v", err)
	}

	// Rotation is strict: no duplicate ids, no removing an absent key.
	if err := p.Attach(k2); !errors.Is(err, ErrDuplicateAuthenticator) {
		t.Errorf("duplicate attach = %v, want ErrDuplicateAuthenticator", err)
	}
	if err := p.Detach("k1"); !errors.Is(err, ErrUnknownAuthenticator) {
		t.Errorf("detach unknown = %v, want ErrUnknownAuthenticator", err)
	}
	if err := p.Attach(nil); !errors.Is(err, ErrInvalidAuthenticator) {
		t.Errorf("attach nil = %v, want ErrInvalidAuthenticator", err)
	}
}

func TestPrincipalFailsClosedWithoutKeys(t *testing.T) {
	p, err := NewPrincipal(principal.MustParse("service:bare"))
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if err := p.Verify([]byte("m"), []byte("sig"), time.Now()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("keyless Verify = %v, want ErrUnauthenticated", err)
	}
	if _, err := NewPrincipal(principal.Ref{}); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("zero ref = %v, want ErrInvalidPrincipal", err)
	}
}

func TestGroupIsACollectionOfPrincipals(t *testing.T) {
	eng := principal.MustParse("group:eng")
	alice := principal.MustParse("user:alice")
	bob := principal.MustParse("user:bob")

	g, err := NewGroup(eng, alice)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	if g.Ref() != eng {
		t.Errorf("Ref = %s, want %s", g.Ref(), eng)
	}
	if err := g.Add(bob); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(g.Members()) != 2 {
		t.Fatalf("Members = %d, want 2", len(g.Members()))
	}
	if err := g.Remove(alice); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(g.Members()) != 1 || g.Members()[0] != bob {
		t.Errorf("Members after remove = %v, want [%s]", g.Members(), bob)
	}
	if err := g.Add(bob); !errors.Is(err, ErrDuplicateMember) {
		t.Errorf("duplicate Add = %v, want ErrDuplicateMember", err)
	}
	if err := g.Remove(alice); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("Remove absent = %v, want ErrUnknownPrincipal", err)
	}

	// A group reference must be group-kind, and a group cannot contain itself.
	if _, err := NewGroup(principal.MustParse("user:not-a-group"), alice); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("non-group ref = %v, want ErrInvalidPrincipal", err)
	}
	if err := g.Add(eng); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("self membership = %v, want ErrInvalidPrincipal", err)
	}
}

func TestGroupMayBeAuthenticatableItself(t *testing.T) {
	g, err := NewGroup(principal.MustParse("group:eng"))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	if g.Shared() != nil {
		t.Error("a group is not shared until it is made authenticatable")
	}

	key, priv := ed25519Key(t, "shared")
	shared, err := g.SetShared(key)
	if err != nil {
		t.Fatalf("SetShared: %v", err)
	}
	if shared.Ref() != g.Ref() {
		t.Errorf("shared identity = %s, want the group ref %s", shared.Ref(), g.Ref())
	}
	challenge := []byte("challenge")
	if err := g.Shared().Verify(challenge, ed25519.Sign(priv, challenge), time.Now()); err != nil {
		t.Errorf("shared identity Verify: %v", err)
	}
}

func TestRoleIsNotAPrincipal(t *testing.T) {
	r, err := NewRole("admin")
	if err != nil {
		t.Fatalf("NewRole: %v", err)
	}
	if r.Name() != "admin" || r.IsZero() {
		t.Errorf("NewRole = %q (zero %v)", r.Name(), r.IsZero())
	}
	if _, err := NewRole("  "); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("blank role = %v, want ErrInvalidRole", err)
	}
	var zero Role
	if !zero.IsZero() {
		t.Error("the zero Role must report zero")
	}
}

func TestPrincipalsResolveFailsClosed(t *testing.T) {
	ref := principal.MustParse("service:relay")
	p, err := NewPrincipal(ref)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	ps, err := NewPrincipals(p)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	if err := ps.Add(p); !errors.Is(err, ErrDuplicatePrincipal) {
		t.Errorf("duplicate declaration = %v, want ErrDuplicatePrincipal", err)
	}
	if err := ps.Add(nil); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("nil declaration = %v, want ErrInvalidPrincipal", err)
	}

	got, err := ps.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != p {
		t.Error("Resolve returned a different principal")
	}

	if _, err := ps.Resolve(principal.MustParse("service:absent")); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("unknown Resolve = %v, want ErrUnknownPrincipal", err)
	}
	if _, err := ps.Resolve(principal.Ref{}); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("zero Resolve = %v, want ErrUnknownPrincipal", err)
	}

	refs := ps.Refs()
	if len(refs) != 1 || refs[0] != ref {
		t.Errorf("Refs = %v, want [%s]", refs, ref)
	}

	// The compiled inline set is a trust source.
	var source TrustSource = ps
	if _, err := source.Resolve(ref); err != nil {
		t.Errorf("Resolve through TrustSource: %v", err)
	}
}
