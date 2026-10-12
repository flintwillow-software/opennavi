package navi

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
)

// ── test doubles ────────────────────────────────────────────────────────

// failingSource is a trust source that cannot answer — an unreachable
// identity provider.
type failingSource struct{ err error }

func (f failingSource) Resolve(principal.Ref) (*Principal, error) { return nil, f.err }

func TestTrustSourcesResolveOrderAndBreakGlass(t *testing.T) {
	inline := principal.MustParse("service:relay")
	p, err := NewPrincipal(inline)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	compiled, err := NewPrincipals(p)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}

	// An unreachable provider is declared FIRST: the compiled principal must
	// still resolve, which is the accepted break-glass (ADR 0021).
	ts, err := NewTrustSources(
		failingSource{err: fmt.Errorf("%w: idp is down", ErrTrustSource)},
		compiled,
	)
	if err != nil {
		t.Fatalf("NewTrustSources: %v", err)
	}
	got, err := ts.Resolve(inline)
	if err != nil {
		t.Fatalf("Resolve past an unreachable source: %v", err)
	}
	if got != p {
		t.Error("Resolve returned a different principal")
	}

	// Nothing resolves: fail closed, and the outage is named.
	_, err = ts.Resolve(principal.MustParse("service:absent"))
	if !errors.Is(err, ErrUnknownPrincipal) {
		t.Fatalf("unknown ref = %v, want ErrUnknownPrincipal", err)
	}
	if !errors.Is(err, ErrTrustSource) {
		t.Errorf("an unreachable source should be reported: %v", err)
	}

	if _, err := NewTrustSources(nil); !errors.Is(err, ErrTrustSource) {
		t.Errorf("nil source = %v, want ErrTrustSource", err)
	}
}

func TestTrustSourcesVerifyTokenWithoutBearerSource(t *testing.T) {
	compiled, err := NewPrincipals()
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	ts, err := NewTrustSources(compiled)
	if err != nil {
		t.Fatalf("NewTrustSources: %v", err)
	}
	if _, err := ts.VerifyToken(context.Background(), "a.b.c", time.Now()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("no bearer source = %v, want ErrUnauthenticated", err)
	}
	if _, err := ts.VerifyToken(context.Background(), "  ", time.Now()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("empty token = %v, want ErrUnauthenticated", err)
	}
}

// ── keyed trust store ───────────────────────────────────────────────────

func TestFileTrustStoreResolvesKeysClaimsAndGroups(t *testing.T) {
	pub, priv := keypair(t)
	doc := fmt.Sprintf(`
principals:
  - ref: service:relay
    claims:
      team: infra
    groups:
      - group:eng
    keys:
      - id: k1
        type: ed25519
        public_key: %s
`, base64.StdEncoding.EncodeToString(pub))

	dir := t.TempDir()
	path := filepath.Join(dir, "trust.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewFileTrustStore(FileTrustStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	ref := principal.MustParse("service:relay")
	p, err := store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Ref() != ref {
		t.Errorf("Ref = %s, want %s", p.Ref(), ref)
	}
	if p.Claims()["team"] != "infra" {
		t.Errorf("Claims = %v, want team=infra", p.Claims())
	}
	if !p.InGroup(principal.MustParse("group:eng")) {
		t.Errorf("Groups = %v, want group:eng", p.Groups())
	}
	if len(p.Keys()) != 1 {
		t.Fatalf("Keys = %d, want 1", len(p.Keys()))
	}

	challenge := []byte("challenge")
	if err := p.Verify(challenge, ed25519.Sign(priv, challenge), time.Now()); err != nil {
		t.Errorf("stored key does not verify: %v", err)
	}

	if _, err := store.Resolve(principal.MustParse("service:absent")); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("unknown ref = %v, want ErrUnknownPrincipal", err)
	}
}

func TestFileTrustStoreDropsRevokedAndExpiredKeys(t *testing.T) {
	now := time.Now().UTC()
	pub, _ := keypair(t)
	encoded := base64.StdEncoding.EncodeToString(pub)
	doc := fmt.Sprintf(`
principals:
  - ref: service:relay
    keys:
      - id: revoked
        type: ed25519
        public_key: %s
        revoked: true
      - id: expired
        type: ed25519
        public_key: %s
        not_after: %s
      - id: live
        type: ed25519
        public_key: %s
`, encoded, encoded, now.Add(-time.Hour).Format(time.RFC3339), encoded)

	path := filepath.Join(t.TempDir(), "trust.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileTrustStore(FileTrustStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	p, err := store.Resolve(principal.MustParse("service:relay"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	keys := p.Keys()
	if len(keys) != 1 || keys[0].ID() != "live" {
		t.Fatalf("Keys = %v, want only the live key", keys)
	}
}

func TestFileTrustStoreReloadNarrowsRevocation(t *testing.T) {
	pub, _ := keypair(t)
	encoded := base64.StdEncoding.EncodeToString(pub)
	path := filepath.Join(t.TempDir(), "trust.yaml")
	write := func(revoked bool) {
		t.Helper()
		doc := fmt.Sprintf(`
principals:
  - ref: service:relay
    keys:
      - id: k1
        type: ed25519
        public_key: %s
        revoked: %t
`, encoded, revoked)
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(false)

	store, err := NewFileTrustStore(FileTrustStoreConfig{Path: path, ReloadTTL: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	ref := principal.MustParse("service:relay")
	p, err := store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(p.Keys()) != 1 {
		t.Fatalf("Keys = %d, want 1", len(p.Keys()))
	}

	// Revoke on disk: the next resolution narrows (ADR 0021 decision 8).
	write(true)
	p, err = store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve after revoke: %v", err)
	}
	if len(p.Keys()) != 0 {
		t.Fatalf("Keys = %d after revocation, want 0", len(p.Keys()))
	}
}

func TestFileTrustStoreFailures(t *testing.T) {
	if _, err := NewFileTrustStore(FileTrustStoreConfig{}); !errors.Is(err, ErrTrustSource) {
		t.Errorf("empty path = %v, want ErrTrustSource", err)
	}
	missing, err := NewFileTrustStore(FileTrustStoreConfig{Path: filepath.Join(t.TempDir(), "nope.yaml")})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	if _, err := missing.Resolve(principal.MustParse("service:relay")); !errors.Is(err, ErrTrustSource) {
		t.Errorf("missing file = %v, want ErrTrustSource", err)
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("principals:\n  - ref: \"not qualified\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileTrustStore(FileTrustStoreConfig{Path: bad})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	if _, err := store.Resolve(principal.MustParse("service:relay")); !errors.Is(err, ErrInvalidTrustStore) {
		t.Errorf("malformed document = %v, want ErrInvalidTrustStore", err)
	}
}

func TestFileTrustStoreResolvesPathReference(t *testing.T) {
	pub, _ := keypair(t)
	path := filepath.Join(t.TempDir(), "trust.yaml")
	doc := fmt.Sprintf("principals:\n  - ref: service:relay\n    keys:\n      - id: k1\n        type: ed25519\n        public_key: %s\n",
		base64.StdEncoding.EncodeToString(pub))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NAVI_TEST_TRUST_STORE", path)

	// An environment variable name resolves to the path it holds (ADR 0008).
	store, err := NewFileTrustStore(FileTrustStoreConfig{Path: "NAVI_TEST_TRUST_STORE"})
	if err != nil {
		t.Fatalf("NewFileTrustStore: %v", err)
	}
	if store.Path() != path {
		t.Errorf("Path = %q, want %q", store.Path(), path)
	}
	if _, err := store.Resolve(principal.MustParse("service:relay")); err != nil {
		t.Errorf("Resolve through the env reference: %v", err)
	}
}

// ── OIDC identity provider ──────────────────────────────────────────────

// idp is a fake OIDC provider: a discovery document and a JWKS.
type idp struct {
	srv    *httptest.Server
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	kid    string
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	p := &idp{rsaKey: rsaKey, ecKey: ecKey, kid: "rsa-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": p.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p.jwks())
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) jwks() map[string]any {
	return map[string]any{"keys": []map[string]any{
		{
			"kty": "RSA", "kid": p.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(p.rsaKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.rsaKey.E)).Bytes()),
		},
		{
			"kty": "EC", "kid": "ec-1", "use": "sig", "alg": "ES256", "crv": "P-256",
			"x": base64.RawURLEncoding.EncodeToString(pad32(p.ecKey.X.Bytes())),
			"y": base64.RawURLEncoding.EncodeToString(pad32(p.ecKey.Y.Bytes())),
		},
	}}
}

func (p *idp) claims(t *testing.T, now time.Time) map[string]any {
	t.Helper()
	return map[string]any{
		"iss":   p.srv.URL,
		"aud":   "navi-client",
		"sub":   "alice",
		"email": "alice@example.com",
		"name":  "Alice",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
}

func (p *idp) signRS256(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signJWT(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": p.kid}, claims, func(signed []byte) []byte {
		digest := sha256.Sum256(signed)
		sig, err := rsa.SignPKCS1v15(rand.Reader, p.rsaKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("SignPKCS1v15: %v", err)
		}
		return sig
	})
}

func (p *idp) signES256(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signJWT(t, map[string]any{"alg": "ES256", "typ": "JWT", "kid": "ec-1"}, claims, func(signed []byte) []byte {
		digest := sha256.Sum256(signed)
		r, s, err := ecdsa.Sign(rand.Reader, p.ecKey, digest[:])
		if err != nil {
			t.Fatalf("ecdsa.Sign: %v", err)
		}
		return append(pad32(r.Bytes()), pad32(s.Bytes())...)
	})
}

// signJWT assembles a compact JWS with the given header and claims.
func signJWT(t *testing.T, header, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(signed)))
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func newOIDC(t *testing.T, p *idp, now time.Time) *OIDC {
	t.Helper()
	o, err := NewOIDC(OIDCConfig{
		Issuer:   p.srv.URL,
		Audience: "navi-client",
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	return o
}

func TestOIDCVerifiesRS256AndDerivesAPrincipal(t *testing.T) {
	now := time.Now().UTC()
	p := newIDP(t)
	o := newOIDC(t, p, now)

	got, err := o.VerifyToken(context.Background(), p.signRS256(t, p.claims(t, now)), now)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if want := principal.MustParse("user:alice"); got.Ref() != want {
		t.Errorf("Ref = %s, want %s", got.Ref(), want)
	}
	if got.Claims()["email"] != "alice@example.com" {
		t.Errorf("Claims = %v, want email from the token", got.Claims())
	}
	// The provider supplies identity; it supplies no keys and no policy.
	if len(got.Keys()) != 0 {
		t.Errorf("Keys = %d, want none (a token is the credential)", len(got.Keys()))
	}

	// Resolve by reference is not how an OIDC principal is found.
	if _, err := o.Resolve(principal.MustParse("user:alice")); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("Resolve = %v, want ErrUnknownPrincipal", err)
	}
}

func TestOIDCVerifiesES256(t *testing.T) {
	now := time.Now().UTC()
	p := newIDP(t)
	o := newOIDC(t, p, now)
	if _, err := o.VerifyToken(context.Background(), p.signES256(t, p.claims(t, now)), now); err != nil {
		t.Fatalf("ES256 VerifyToken: %v", err)
	}
}

func TestOIDCRefusesBadTokens(t *testing.T) {
	now := time.Now().UTC()
	p := newIDP(t)
	o := newOIDC(t, p, now)
	ctx := context.Background()

	// A signature from another key.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := p.claims(t, now)
	foreign := signJWT(t, map[string]any{"alg": "RS256", "kid": p.kid}, claims, func(signed []byte) []byte {
		digest := sha256.Sum256(signed)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, other, crypto.SHA256, digest[:])
		return sig
	})

	// `none` and an HMAC algorithm are refused by name (no algorithm
	// confusion), and a token minted for another audience is not ours.
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"`+p.srv.URL+`","aud":"navi-client","sub":"x"}`)) + "."

	expired := p.claims(t, now)
	expired["exp"] = now.Add(-2 * time.Hour).Unix()
	wrongAud := p.claims(t, now)
	wrongAud["aud"] = "someone-else"
	wrongIss := p.claims(t, now)
	wrongIss["iss"] = "https://evil.example.com"
	noSubject := p.claims(t, now)
	delete(noSubject, "sub")

	cases := map[string]string{
		"foreign signature": foreign,
		"alg none":          none,
		"not a jws":         "not-a-token",
		"expired":           p.signRS256(t, expired),
		"wrong audience":    p.signRS256(t, wrongAud),
		"wrong issuer":      p.signRS256(t, wrongIss),
		"no subject":        p.signRS256(t, noSubject),
	}
	for name, token := range cases {
		if _, err := o.VerifyToken(ctx, token, now); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestOIDCRejectsAlgorithmConfusion(t *testing.T) {
	now := time.Now().UTC()
	p := newIDP(t)
	o := newOIDC(t, p, now)
	// HS256 signed with the RSA public key's bytes: the classic confusion
	// attack. Refused before any signature work.
	hs := signJWT(t, map[string]any{"alg": "HS256", "kid": p.kid}, p.claims(t, now), func([]byte) []byte { return []byte("sig") })
	if _, err := o.VerifyToken(context.Background(), hs, now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("HS256 = %v, want ErrUnauthenticated", err)
	}
}

func TestOIDCUnreachableProviderIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	o, err := NewOIDC(OIDCConfig{Issuer: srv.URL, Audience: "navi-client"})
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	// A syntactically valid token, so verification reaches the key fetch the
	// provider cannot answer.
	token := signJWT(t, map[string]any{"alg": "RS256", "kid": "k"}, map[string]any{"iss": srv.URL, "aud": "navi-client"}, func([]byte) []byte { return []byte("sig") })
	if _, err := o.VerifyToken(context.Background(), token, time.Now()); !errors.Is(err, ErrTrustSource) {
		t.Errorf("unreachable provider = %v, want ErrTrustSource", err)
	}
}

func TestOIDCRequiresIssuerAndAudience(t *testing.T) {
	if _, err := NewOIDC(OIDCConfig{Audience: "x"}); !errors.Is(err, ErrTrustSource) {
		t.Errorf("missing issuer = %v, want ErrTrustSource", err)
	}
	if _, err := NewOIDC(OIDCConfig{Issuer: "https://idp.example.com"}); !errors.Is(err, ErrTrustSource) {
		t.Errorf("missing audience = %v, want ErrTrustSource", err)
	}
}

func TestNaviDeclaresTrustSources(t *testing.T) {
	ref := principal.MustParse("service:relay")
	p, err := NewPrincipal(ref)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	compiled, err := NewPrincipals(p)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}

	n := New("trust-test", WithTrustSource(compiled))
	if n.Trust() == nil {
		t.Fatal("a declared trust source was not compiled into the navi")
	}
	if _, err := n.Trust().Resolve(ref); err != nil {
		t.Errorf("Resolve through the navi's trust set: %v", err)
	}

	// A nil declaration is refused rather than silently skipped.
	if err := New("trust-bad", WithTrustSource(nil)).Validate(); err == nil {
		t.Fatal("a nil trust source must fail validation")
	}

	// A navi that declares none has an empty (fail-closed) set, not a nil one.
	bare := New("trust-bare")
	if bare.Trust() == nil {
		t.Fatal("the compiled trust set is always present")
	}
	if _, err := bare.Trust().Resolve(ref); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("empty trust set = %v, want ErrUnknownPrincipal", err)
	}
}

// ── claims and groups on a principal ────────────────────────────────────

func TestResolvedPrincipalCarriesClaimsAndGroups(t *testing.T) {
	ref := principal.MustParse("user:alice")
	eng := principal.MustParse("group:eng")
	claims := map[string]string{"email": "alice@example.com"}
	p, err := NewResolvedPrincipal(ref, claims, []principal.Ref{eng})
	if err != nil {
		t.Fatalf("NewResolvedPrincipal: %v", err)
	}
	if p.Claims()["email"] != "alice@example.com" {
		t.Errorf("Claims = %v", p.Claims())
	}
	if !p.InGroup(eng) || p.InGroup(principal.MustParse("group:ops")) {
		t.Errorf("Groups = %v, want only %s", p.Groups(), eng)
	}

	// The identity data is not aliased: mutating the input or the output
	// cannot change the principal.
	claims["email"] = "mallory@example.com"
	got := p.Claims()
	got["email"] = "mallory@example.com"
	if p.Claims()["email"] != "alice@example.com" {
		t.Error("claims were aliased")
	}

	if _, err := NewResolvedPrincipal(ref, nil, []principal.Ref{{}}); !errors.Is(err, ErrInvalidPrincipal) {
		t.Errorf("zero group = %v, want ErrInvalidPrincipal", err)
	}
}

// ── the portal seam ─────────────────────────────────────────────────────

func TestPortalAuthenticatorResolvesCredentials(t *testing.T) {
	now := time.Now().UTC()
	ctx := context.Background()
	ref := principal.MustParse("service:cli")
	pub, priv := keypair(t)
	key, err := NewEd25519Key("k1", pub)
	if err != nil {
		t.Fatalf("NewEd25519Key: %v", err)
	}
	who, err := NewPrincipal(ref, key)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	compiled, err := NewPrincipals(who)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	trust, err := NewTrustSources(compiled)
	if err != nil {
		t.Fatalf("NewTrustSources: %v", err)
	}
	auth := NewPortalAuthenticator(trust)

	message := []byte("POST\n/portal/control\n1\nabc")
	cred := portal.Credential{
		Scheme: portal.SchemeEd25519, Principal: ref, KeyID: "k1",
		Message: message, Proof: ed25519.Sign(priv, message), Now: now,
	}
	got, err := auth.Authenticate(ctx, cred)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != ref {
		t.Errorf("principal = %s, want %s", got, ref)
	}

	bad := cred
	bad.Proof = ed25519.Sign(priv, []byte("another message"))
	if _, err := auth.Authenticate(ctx, bad); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("bad signature = %v, want ErrUnauthenticated", err)
	}

	absent := cred
	absent.Principal = principal.MustParse("service:absent")
	if _, err := auth.Authenticate(ctx, absent); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("unknown principal = %v, want ErrUnauthenticated", err)
	}

	// No credential, an unknown scheme, and a nil trust set are all refused.
	if _, err := auth.Authenticate(ctx, portal.Credential{Now: now}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("no credential = %v, want ErrUnauthenticated", err)
	}
	if _, err := auth.Authenticate(ctx, portal.Credential{Scheme: "kerberos", Now: now}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("unknown scheme = %v, want ErrUnauthenticated", err)
	}
	if _, err := NewPortalAuthenticator(nil).Authenticate(ctx, cred); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("nil trust set = %v, want ErrUnauthenticated", err)
	}
}

func TestPortalAuthenticatorVerifiesBearerTokens(t *testing.T) {
	now := time.Now().UTC()
	ctx := context.Background()
	p := newIDP(t)
	trust, err := NewTrustSources(newOIDC(t, p, now))
	if err != nil {
		t.Fatalf("NewTrustSources: %v", err)
	}
	auth := NewPortalAuthenticator(trust)

	got, err := auth.Authenticate(ctx, portal.Credential{
		Scheme: portal.SchemeBearer, Token: p.signRS256(t, p.claims(t, now)), Now: now,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if want := principal.MustParse("user:alice"); got != want {
		t.Errorf("principal = %s, want %s", got, want)
	}

	if _, err := auth.Authenticate(ctx, portal.Credential{Scheme: portal.SchemeBearer, Token: "not-a-token", Now: now}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("bad token = %v, want ErrUnauthenticated", err)
	}
}

// depsPortal records the Deps the navi hands a portal at Start, delegating the
// rest of the portal contract to the shared fakePortal.
type depsPortal struct {
	*fakePortal
	deps portal.Deps
}

func (p *depsPortal) Start(ctx context.Context, deps portal.Deps) error {
	p.deps = deps
	return p.fakePortal.Start(ctx, deps)
}

// TestNaviWiresPortalAuthenticator pins the seam NAVI-402 consumes: the
// composition root hands every portal an authenticator backed by the declared
// trust sources.
func TestNaviWiresPortalAuthenticator(t *testing.T) {
	ref := principal.MustParse("service:relay")
	who, err := NewPrincipal(ref)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	compiled, err := NewPrincipals(who)
	if err != nil {
		t.Fatalf("NewPrincipals: %v", err)
	}
	trust, err := NewTrustSources(compiled)
	if err != nil {
		t.Fatalf("NewTrustSources: %v", err)
	}

	p := &depsPortal{fakePortal: newFakePortal(":0")}
	n := New("auth-wired", WithDefaultModel("ollama"), WithTrustSource(trust))
	if err := n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"}); err != nil {
		t.Fatal(err)
	}
	if err := n.Portals.Add(p); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()
	waitFor(t, p.started, "portal start")

	if p.deps.Authenticator == nil {
		t.Fatal("the navi wired no portal authenticator")
	}

	// The wired authenticator resolves through the declared trust source: no
	// credential is refused, and an Ed25519 claim for a compiled principal
	// with a bad proof is refused too.
	if _, err := p.deps.Authenticator.Authenticate(ctx, portal.Credential{}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("no credential = %v, want ErrUnauthenticated", err)
	}
	if _, err := p.deps.Authenticator.Authenticate(ctx, portal.Credential{
		Scheme: portal.SchemeEd25519, Principal: ref, KeyID: "absent",
	}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Errorf("unproven claim = %v, want ErrUnauthenticated", err)
	}

	cancel()
	<-done
}
