package navi

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the trust half of ADR 0021 decisions 4 and 6-8: where identity
// data comes from. A navi declares its trust sources at compile time (decision
// 6); each resolves its contents at runtime. A trust source supplies identity
// and claims and NEVER policy (decision 7) — policy is compiled (NAVI-399),
// and resolution cannot introduce a capability.
//
// Three forms exist, matching decision 6: principals declared inline
// (Principals), an OIDC identity provider (OIDC), and a keyed trust store
// (FileTrustStore). A navi composes them in one ordered TrustSources set, so
// an unreachable identity provider never disables the compiled principals that
// remain — the accepted break-glass.

// Trust-source errors. Callers test with errors.Is.
var (
	// ErrTrustSource is a trust source that is misconfigured or cannot answer
	// (an unreachable identity provider, a store that cannot be read). It is
	// distinct from ErrUnknownPrincipal: the source could not decide, rather
	// than deciding "no".
	ErrTrustSource = errors.New("navi: trust source unavailable")
	// ErrInvalidTrustStore is a keyed trust store document that does not
	// describe principals.
	ErrInvalidTrustStore = errors.New("navi: invalid trust store")
)

// TrustSources is the compiled, ordered set of trust sources a navi declares
// (ADR 0021 decision 6). Resolution walks the sources in declaration order and
// returns the first hit. A source that errors never masks one that knows the
// reference — so the compiled inline principals remain the break-glass when an
// identity provider is unreachable (ADR 0021, "Consequences").
//
// A principal a CREDENTIAL resolved is remembered briefly (see
// ResolvedCacheTTL): an OIDC principal is derived from a token, not looked up,
// so its claims cannot be recovered from a bare reference — the identity has to
// survive from the authentication to the authorization that follows it.
type TrustSources struct {
	mu      sync.RWMutex
	sources []TrustSource
	// resolved caches principals a credential resolved, keyed by reference.
	// Only VerifyToken populates it; a reference resolved from a store is
	// cheap and stays uncached, so a revocation there takes effect at once.
	resolved map[principal.Ref]resolvedPrincipal
	// ledger records the trust decisions the navi makes (ADR 0021 decision 12,
	// ADR 0027). It is operator-facing and separate from the interaction
	// record; the authenticator appends to it.
	ledger *TrustLedger
}

// ResolvedCacheTTL bounds how long a principal a bearer token resolved to is
// reused for its reference. The window is short because a remembered principal
// outlives a revocation by at most this long; the gateway re-authenticates per
// request, so the entry exists to carry identity from one request's
// authentication to its own authorization, not to keep a session alive.
const ResolvedCacheTTL = 30 * time.Second

// resolvedPrincipal is one remembered principal and when it stops being used.
type resolvedPrincipal struct {
	principal *Principal
	expires   time.Time
}

// NewTrustSources builds the ordered set. A nil source is refused rather than
// skipped, so a misconfigured declaration fails loudly at assembly.
func NewTrustSources(sources ...TrustSource) (*TrustSources, error) {
	ts := &TrustSources{ledger: NewTrustLedger(0)}
	for _, s := range sources {
		if err := ts.Add(s); err != nil {
			return nil, err
		}
	}
	return ts, nil
}

// Ledger returns the trust ledger this set records decisions into (ADR 0027).
// It is never nil for a set built by NewTrustSources.
func (ts *TrustSources) Ledger() *TrustLedger {
	if ts == nil {
		return nil
	}
	return ts.ledger
}

// Add appends a source. Order is resolution order.
func (ts *TrustSources) Add(s TrustSource) error {
	if s == nil {
		return fmt.Errorf("%w: nil trust source", ErrTrustSource)
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sources = append(ts.sources, s)
	return nil
}

// Sources returns the declared sources in resolution order.
func (ts *TrustSources) Sources() []TrustSource {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]TrustSource(nil), ts.sources...)
}

// BearerSource is a trust source that can also derive a principal from a
// presented bearer token (an OIDC identity provider, ADR 0021 decisions 7-8).
// The token is the credential; there is no reference to look up first.
type BearerSource interface {
	TrustSource
	// VerifyToken returns the principal a bearer token authenticates, or an
	// ErrUnauthenticated refusal.
	VerifyToken(ctx context.Context, token string, now time.Time) (*Principal, error)
}

// VerifyToken authenticates a bearer token against the first bearer-capable
// source that accepts it. No bearer source declared, or no source accepting
// the token, is ErrUnauthenticated — never a default principal.
func (ts *TrustSources) VerifyToken(ctx context.Context, token string, now time.Time) (*Principal, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: empty bearer token", ErrUnauthenticated)
	}
	var last error
	for _, source := range ts.Sources() {
		bearer, ok := source.(BearerSource)
		if !ok {
			continue
		}
		p, err := bearer.VerifyToken(ctx, token, now)
		if err == nil {
			// Keep the resolved identity: an OIDC principal cannot be resolved
			// from its reference alone, and the authorization that follows this
			// authentication needs its claims and groups.
			ts.remember(p, now)
			return p, nil
		}
		last = err
	}
	if last != nil {
		return nil, last
	}
	return nil, fmt.Errorf("%w: no OIDC trust source is declared", ErrUnauthenticated)
}

// Resolve implements TrustSource: it returns the first source's answer, or
// ErrUnknownPrincipal when no source knows the reference. A source that fails
// (an unreachable identity provider) is reported inside ErrUnknownPrincipal
// only when nothing else resolved, so it can never mask a compiled principal.
//
// A reference a credential just resolved is answered from the remembered
// principal, so the identity data the credential carried (claims, groups) is
// available to the authorization that follows authentication.
func (ts *TrustSources) Resolve(ref principal.Ref) (*Principal, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnknownPrincipal, err)
	}
	if p, ok := ts.remembered(ref, time.Now()); ok {
		return p, nil
	}
	sources := ts.Sources()
	var unavailable error
	for _, source := range sources {
		p, err := source.Resolve(ref)
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, ErrUnknownPrincipal):
			continue
		default:
			unavailable = err
		}
	}
	if unavailable != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnknownPrincipal, ref, unavailable)
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownPrincipal, ref)
}

// remember records a principal a credential resolved, for ResolvedCacheTTL.
func (ts *TrustSources) remember(p *Principal, now time.Time) {
	if p == nil || p.Ref().IsZero() {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.resolved == nil {
		ts.resolved = map[principal.Ref]resolvedPrincipal{}
	}
	ts.resolved[p.Ref()] = resolvedPrincipal{principal: p, expires: now.Add(ResolvedCacheTTL)}
}

// remembered returns the principal remembered for ref while it is still fresh.
func (ts *TrustSources) remembered(ref principal.Ref, now time.Time) (*Principal, bool) {
	ts.mu.RLock()
	entry, ok := ts.resolved[ref]
	ts.mu.RUnlock()
	if !ok || now.After(entry.expires) {
		return nil, false
	}
	return entry.principal, true
}

// ── OIDC identity provider ──────────────────────────────────────────────

// DefaultOIDCClockSkew absorbs a small clock difference between this host and
// the identity provider. It is deliberately small: a generous skew is a
// generous acceptance window for an expired token.
const DefaultOIDCClockSkew = 60 * time.Second

// DefaultJWKSCacheTTL is how long a fetched key set is reused. Key rotation is
// also handled on a `kid` miss, so the TTL bounds staleness rather than
// correctness.
const DefaultJWKSCacheTTL = 5 * time.Minute

// OIDCConfig configures an OIDC identity-provider trust source (ADR 0021
// decisions 6-8). The provider supplies identity and claims; the navi supplies
// policy.
type OIDCConfig struct {
	// Issuer is the expected `iss` claim and, unless JWKSURL is set, the
	// base of the discovery document. Required.
	Issuer string
	// Audience is the expected `aud` claim — the client id this navi is
	// registered as. Required: a token minted for another client is not this
	// navi's to accept.
	Audience string
	// JWKSURL overrides the key-set endpoint. Empty derives it from Issuer's
	// `/.well-known/openid-configuration`.
	JWKSURL string
	// SubjectClaim names the claim that supplies the principal name. Default
	// "sub".
	SubjectClaim string
	// PrincipalKind is the kind of the resolved principal. Default KindUser:
	// a human authenticates through an OIDC session (ADR 0021 decision 8).
	PrincipalKind principal.Kind
	// ClockSkew absorbs clock difference on exp/nbf. Default
	// DefaultOIDCClockSkew.
	ClockSkew time.Duration
	// JWKSCacheTTL bounds how long a fetched key set is reused. Default
	// DefaultJWKSCacheTTL.
	JWKSCacheTTL time.Duration
	// HTTPClient fetches the discovery document and key set. Default a client
	// with a 10s timeout.
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// OIDC is an OIDC identity-provider trust source: it verifies a bearer token
// against the provider's published keys and derives a principal from the
// token's claims (ADR 0021 decisions 7-8). Only identity and claims come from
// the provider — never policy.
type OIDC struct {
	issuer       string
	audience     string
	jwksURL      string
	subjectClaim string
	kind         principal.Kind
	skew         time.Duration
	cacheTTL     time.Duration
	client       *http.Client
	now          func() time.Time

	mu        sync.Mutex
	keys      []jwk
	keysAt    time.Time
	keysURL   string
	discovery string
}

// NewOIDC builds an OIDC trust source. Issuer and Audience are required.
func NewOIDC(cfg OIDCConfig) (*OIDC, error) {
	issuer := strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("%w: OIDC issuer is required", ErrTrustSource)
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("%w: OIDC audience is required", ErrTrustSource)
	}
	o := &OIDC{
		issuer:       issuer,
		audience:     cfg.Audience,
		jwksURL:      strings.TrimSpace(cfg.JWKSURL),
		subjectClaim: strings.TrimSpace(cfg.SubjectClaim),
		kind:         cfg.PrincipalKind,
		skew:         cfg.ClockSkew,
		cacheTTL:     cfg.JWKSCacheTTL,
		client:       cfg.HTTPClient,
		now:          cfg.Now,
	}
	if o.subjectClaim == "" {
		o.subjectClaim = "sub"
	}
	if o.kind == "" {
		o.kind = principal.KindUser
	}
	if o.skew == 0 {
		o.skew = DefaultOIDCClockSkew
	}
	if o.cacheTTL == 0 {
		o.cacheTTL = DefaultJWKSCacheTTL
	}
	if o.client == nil {
		o.client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o, nil
}

// Resolve implements TrustSource. An OIDC principal exists only as a verified
// token, so a bare reference is not resolvable here — the token is the
// credential, and VerifyToken is the entry point.
func (o *OIDC) Resolve(principal.Ref) (*Principal, error) {
	return nil, fmt.Errorf("%w: an OIDC principal is resolved from a token, not a reference", ErrUnknownPrincipal)
}

// VerifyToken verifies a bearer token and returns the principal it
// authenticates: identity and claims from the provider, keys none (a token is
// the credential). Every failure is ErrUnauthenticated — a token that does not
// verify is never partially trusted.
func (o *OIDC) VerifyToken(ctx context.Context, token string, now time.Time) (*Principal, error) {
	header, claims, signed, signature, err := splitJWT(token)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = o.now()
	}

	// Only asymmetric algorithms are accepted. `none` is not a signature, and
	// an HMAC algorithm would let a public key double as a signing secret
	// (algorithm confusion), so both are refused by name.
	alg := strings.ToUpper(strings.TrimSpace(header.Alg))
	switch alg {
	case "RS256", "ES256":
	default:
		return nil, fmt.Errorf("%w: OIDC token alg %q is not accepted", ErrUnauthenticated, header.Alg)
	}

	keys, err := o.keySet(ctx, header.Kid, alg, now)
	if err != nil {
		return nil, err
	}
	if err := verifyJWTSignature(alg, keys, header.Kid, signed, signature); err != nil {
		return nil, err
	}

	if err := o.checkClaims(claims, now); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(stringClaim(claims, o.subjectClaim))
	if name == "" {
		return nil, fmt.Errorf("%w: OIDC token has no %q claim", ErrUnauthenticated, o.subjectClaim)
	}
	ref, err := principal.New(o.kind, name)
	if err != nil {
		return nil, fmt.Errorf("%w: OIDC subject: %w", ErrUnauthenticated, err)
	}
	return NewResolvedPrincipal(ref, stringClaims(claims), nil)
}

// keySet returns the provider's keys, fetching and caching the key set (and,
// when needed, the discovery document). A `kid` that is not in the cached set
// forces one refetch, so a rotated key is picked up without waiting for the
// TTL.
func (o *OIDC) keySet(ctx context.Context, kid, alg string, now time.Time) ([]jwk, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fresh := len(o.keys) > 0 && now.Sub(o.keysAt) < o.cacheTTL
	if fresh && (kid == "" || hasKeyForAlg(o.keys, kid, alg)) {
		return o.keys, nil
	}
	url := o.jwksURL
	if url == "" {
		discovered, err := o.discover(ctx)
		if err != nil {
			return nil, err
		}
		url = discovered
	}
	keys, err := o.fetchKeys(ctx, url)
	if err != nil {
		return nil, err
	}
	o.keys, o.keysAt, o.keysURL = keys, now, url
	if kid != "" && !hasKeyForAlg(keys, kid, alg) {
		return nil, fmt.Errorf("%w: OIDC key set has no %s key %q", ErrUnauthenticated, alg, kid)
	}
	return keys, nil
}

// discover reads the issuer's discovery document and returns its jwks_uri.
func (o *OIDC) discover(ctx context.Context) (string, error) {
	if o.discovery != "" {
		return o.discovery, nil
	}
	url := o.issuer + "/.well-known/openid-configuration"
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := o.getJSON(ctx, url, &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.JWKSURI) == "" {
		return "", fmt.Errorf("%w: OIDC discovery at %s has no jwks_uri", ErrTrustSource, url)
	}
	o.discovery = doc.JWKSURI
	return o.discovery, nil
}

// fetchKeys reads and parses a JWKS document.
func (o *OIDC) fetchKeys(ctx context.Context, url string) ([]jwk, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := o.getJSON(ctx, url, &doc); err != nil {
		return nil, err
	}
	if len(doc.Keys) == 0 {
		return nil, fmt.Errorf("%w: OIDC key set at %s is empty", ErrTrustSource, url)
	}
	return doc.Keys, nil
}

func (o *OIDC) getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTrustSource, url, err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTrustSource, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s returned %s", ErrTrustSource, url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTrustSource, url, err)
	}
	return nil
}

// checkClaims enforces the token's issuer, audience, and validity window.
func (o *OIDC) checkClaims(claims map[string]any, now time.Time) error {
	if got := stringClaim(claims, "iss"); got != o.issuer {
		return fmt.Errorf("%w: OIDC issuer %q is not %q", ErrUnauthenticated, got, o.issuer)
	}
	if !audienceMatches(claims["aud"], o.audience) {
		return fmt.Errorf("%w: OIDC token is not for audience %q", ErrUnauthenticated, o.audience)
	}
	if exp, ok := numericClaim(claims, "exp"); ok {
		if now.After(exp.Add(o.skew)) {
			return fmt.Errorf("%w: OIDC token expired at %s", ErrUnauthenticated, exp.UTC())
		}
	}
	if nbf, ok := numericClaim(claims, "nbf"); ok {
		if now.Add(o.skew).Before(nbf) {
			return fmt.Errorf("%w: OIDC token is not valid before %s", ErrUnauthenticated, nbf.UTC())
		}
	}
	return nil
}

// ── JWT / JWKS primitives ───────────────────────────────────────────────

// jwtHeader is the JOSE header this source reads. alg and kid are the only
// fields that affect verification.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// jwk is one JSON Web Key. Only the asymmetric shapes OIDC providers publish
// for RS256 and ES256 are read.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// splitJWT parses the compact JWS form into its header, claims, the exact
// bytes the signature covers, and the signature.
func splitJWT(token string) (jwtHeader, map[string]any, []byte, []byte, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token is not a compact JWS", ErrUnauthenticated)
	}
	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token header: %w", ErrUnauthenticated, err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token header: %w", ErrUnauthenticated, err)
	}
	claimsBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token claims: %w", ErrUnauthenticated, err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token claims: %w", ErrUnauthenticated, err)
	}
	signature, err := base64URLDecode(parts[2])
	if err != nil {
		return jwtHeader{}, nil, nil, nil, fmt.Errorf("%w: OIDC token signature: %w", ErrUnauthenticated, err)
	}
	return header, claims, []byte(parts[0] + "." + parts[1]), signature, nil
}

// verifyJWTSignature verifies signature over signed with the key set's key for
// kid. An empty kid accepts the first key whose algorithm family matches —
// providers that publish a single key omit it.
func verifyJWTSignature(alg string, keys []jwk, kid string, signed, signature []byte) error {
	digest := sha256.Sum256(signed)
	tried := 0
	for _, key := range keys {
		if kid != "" && key.Kid != kid {
			continue
		}
		if !keyMatchesAlg(key, alg) {
			continue
		}
		tried++
		switch alg {
		case "RS256":
			pub, err := rsaPublicKey(key)
			if err != nil {
				continue
			}
			if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature) == nil {
				return nil
			}
		case "ES256":
			pub, err := ecdsaPublicKey(key)
			if err != nil {
				continue
			}
			if len(signature) == 64 {
				r := new(big.Int).SetBytes(signature[:32])
				s := new(big.Int).SetBytes(signature[32:])
				if ecdsa.Verify(pub, digest[:], r, s) {
					return nil
				}
			}
		}
	}
	if tried == 0 {
		return fmt.Errorf("%w: OIDC key set has no usable %s key", ErrUnauthenticated, alg)
	}
	return fmt.Errorf("%w: OIDC token signature did not verify", ErrUnauthenticated)
}

// keyMatchesAlg reports whether a JWK can verify alg. A key's declared `alg`
// is honoured when present; its type decides otherwise.
func keyMatchesAlg(key jwk, alg string) bool {
	if key.Use != "" && key.Use != "sig" {
		return false
	}
	if key.Alg != "" && !strings.EqualFold(key.Alg, alg) {
		return false
	}
	switch alg {
	case "RS256":
		return key.Kty == "RSA"
	case "ES256":
		return key.Kty == "EC" && (key.Crv == "" || key.Crv == "P-256")
	}
	return false
}

// hasKeyForAlg reports whether the set holds a key for kid that can verify alg.
func hasKeyForAlg(keys []jwk, kid, alg string) bool {
	for _, key := range keys {
		if key.Kid == kid && keyMatchesAlg(key, alg) {
			return true
		}
	}
	return false
}

// rsaPublicKey builds an RSA public key from a JWK's n and e.
func rsaPublicKey(key jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64URLDecode(key.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64URLDecode(key.E)
	if err != nil {
		return nil, err
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, fmt.Errorf("jwk: RSA key is missing n or e")
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e < 2 {
		return nil, fmt.Errorf("jwk: RSA exponent %d is invalid", e)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

// ecdsaPublicKey builds a P-256 public key from a JWK's x and y.
func ecdsaPublicKey(key jwk) (*ecdsa.PublicKey, error) {
	xBytes, err := base64URLDecode(key.X)
	if err != nil {
		return nil, err
	}
	yBytes, err := base64URLDecode(key.Y)
	if err != nil {
		return nil, err
	}
	if len(xBytes) == 0 || len(yBytes) == 0 {
		return nil, fmt.Errorf("jwk: EC key is missing x or y")
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}
	if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
		return nil, fmt.Errorf("jwk: EC point is not on P-256")
	}
	return pub, nil
}

// base64URLDecode decodes base64url with or without padding.
func base64URLDecode(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// audienceMatches reports whether an `aud` claim (a string or a list) contains
// want.
func audienceMatches(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// stringClaim reads a string claim.
func stringClaim(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}

// numericClaim reads a NumericDate claim as a time.
func numericClaim(claims map[string]any, name string) (time.Time, bool) {
	switch v := claims[name].(type) {
	case float64:
		return time.Unix(int64(v), 0), true
	case json.Number:
		secs, err := v.Int64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

// stringClaims collects the token's string-valued top-level claims. They are
// identity data a condition or the trust ledger may consult; never policy.
func stringClaims(claims map[string]any) map[string]string {
	out := map[string]string{}
	for name, value := range claims {
		if s, ok := value.(string); ok && s != "" {
			out[name] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ── keyed trust store ───────────────────────────────────────────────────

// DefaultTrustStoreReloadTTL is how long a loaded trust store is reused. Zero
// would re-read on every resolution; this bounds the read rate while keeping a
// revocation's effect bounded by the TTL.
const DefaultTrustStoreReloadTTL = 30 * time.Second

// FileTrustStoreConfig configures a file-backed keyed trust store (ADR 0021
// decisions 6 and 8): a principals-and-keys document resolved at runtime.
type FileTrustStoreConfig struct {
	// Path is the ADR 0008 reference: an environment variable name whose
	// value is the document path, else the path itself.
	Path string
	// ReloadTTL bounds how often the document is re-read. Default
	// DefaultTrustStoreReloadTTL. A shorter TTL narrows a revocation sooner.
	ReloadTTL time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// FileTrustStore is a keyed trust store backed by a document on disk: which
// principals exist, their keys, their claims, and their group membership come
// from the file, and the file is re-read so a revocation or expiry narrows
// what is accepted at runtime (ADR 0021 decisions 4 and 8). It is the "keyed
// trust store" of decision 6 in its file form; a database-backed store
// implements the same TrustSource interface.
//
// The document is not a secret — it holds public keys and claims — but it is a
// trust anchor, so its path is a reference (an environment variable name or a
// path) resolved at runtime like every other ADR 0008 reference.
type FileTrustStore struct {
	ref       string
	reloadTTL time.Duration
	now       func() time.Time

	mu       sync.Mutex
	loaded   map[principal.Ref]*Principal
	loadedAt time.Time
}

// trustStoreDoc is the document shape.
type trustStoreDoc struct {
	Principals []trustStorePrincipal `yaml:"principals"`
}

type trustStorePrincipal struct {
	Ref    string            `yaml:"ref"`
	Claims map[string]string `yaml:"claims"`
	Groups []string          `yaml:"groups"`
	Keys   []trustStoreKey   `yaml:"keys"`
}

type trustStoreKey struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	PublicKey string `yaml:"public_key"`
	NotBefore string `yaml:"not_before"`
	NotAfter  string `yaml:"not_after"`
	Revoked   bool   `yaml:"revoked"`
}

// NewFileTrustStore builds a file-backed keyed trust store. Path is required;
// the document itself is read at resolution, so a missing file is a runtime
// refusal rather than an assembly failure (a navi still serves its compiled
// principals — the break-glass).
func NewFileTrustStore(cfg FileTrustStoreConfig) (*FileTrustStore, error) {
	ref := strings.TrimSpace(cfg.Path)
	if ref == "" {
		return nil, fmt.Errorf("%w: trust store path is required", ErrTrustSource)
	}
	s := &FileTrustStore{ref: ref, reloadTTL: cfg.ReloadTTL, now: cfg.Now}
	if s.reloadTTL == 0 {
		s.reloadTTL = DefaultTrustStoreReloadTTL
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Path reports the resolved document path, so a running navi can say where its
// trust came from.
func (s *FileTrustStore) Path() string { return resolvePathRef(s.ref) }

// Resolve implements TrustSource.
func (s *FileTrustStore) Resolve(ref principal.Ref) (*Principal, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnknownPrincipal, err)
	}
	loaded, err := s.load(s.now())
	if err != nil {
		return nil, err
	}
	p, ok := loaded[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPrincipal, ref)
	}
	return p, nil
}

// Refs returns the references the store declares, for a deterministic
// diagnostic surface. A store that cannot be read returns the read error.
func (s *FileTrustStore) Refs() ([]principal.Ref, error) {
	loaded, err := s.load(s.now())
	if err != nil {
		return nil, err
	}
	out := make([]principal.Ref, 0, len(loaded))
	for ref := range loaded {
		out = append(out, ref)
	}
	return out, nil
}

// load returns the parsed document, re-reading it when the cache is stale.
func (s *FileTrustStore) load(now time.Time) (map[principal.Ref]*Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded != nil && now.Sub(s.loadedAt) < s.reloadTTL {
		return s.loaded, nil
	}
	path := resolvePathRef(s.ref)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTrustSource, path, err)
	}
	loaded, err := parseTrustStore(data, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidTrustStore, path, err)
	}
	s.loaded, s.loadedAt = loaded, now
	return loaded, nil
}

// parseTrustStore builds the principal set from a document. Revoked and
// expired keys are dropped here, so they can never verify — revocation and
// expiry are runtime narrowing (ADR 0021 decision 8).
func parseTrustStore(data []byte, now time.Time) (map[principal.Ref]*Principal, error) {
	var doc trustStoreDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[principal.Ref]*Principal{}
	for i, entry := range doc.Principals {
		ref, err := principal.Parse(entry.Ref)
		if err != nil {
			return nil, fmt.Errorf("principal %d: %w", i, err)
		}
		groups := make([]principal.Ref, 0, len(entry.Groups))
		for _, g := range entry.Groups {
			group, err := principal.Parse(g)
			if err != nil {
				return nil, fmt.Errorf("principal %d group %q: %w", i, g, err)
			}
			if group.Kind() != principal.KindGroup {
				return nil, fmt.Errorf("principal %d: %s is not a group reference", i, group)
			}
			groups = append(groups, group)
		}
		keys, err := storeKeys(entry.Keys, now)
		if err != nil {
			return nil, fmt.Errorf("principal %d: %w", i, err)
		}
		p, err := NewResolvedPrincipal(ref, entry.Claims, groups, keys...)
		if err != nil {
			return nil, fmt.Errorf("principal %d: %w", i, err)
		}
		if _, dup := out[ref]; dup {
			return nil, fmt.Errorf("%s is declared twice", ref)
		}
		out[ref] = p
	}
	return out, nil
}

// storeKeys builds the usable authenticators from a document's key entries.
func storeKeys(entries []trustStoreKey, now time.Time) ([]Authenticator, error) {
	keys := make([]Authenticator, 0, len(entries))
	for _, entry := range entries {
		if entry.Revoked {
			continue
		}
		if kind := strings.ToLower(strings.TrimSpace(entry.Type)); kind != "ed25519" {
			return nil, fmt.Errorf("key %q: unsupported type %q", entry.ID, entry.Type)
		}
		pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(entry.PublicKey))
		if err != nil {
			return nil, fmt.Errorf("key %q: public_key: %w", entry.ID, err)
		}
		var opts []KeyOption
		if entry.NotBefore != "" {
			from, err := time.Parse(time.RFC3339, entry.NotBefore)
			if err != nil {
				return nil, fmt.Errorf("key %q: not_before: %w", entry.ID, err)
			}
			opts = append(opts, KeyValidFrom(from))
		}
		if entry.NotAfter != "" {
			until, err := time.Parse(time.RFC3339, entry.NotAfter)
			if err != nil {
				return nil, fmt.Errorf("key %q: not_after: %w", entry.ID, err)
			}
			opts = append(opts, KeyValidUntil(until))
		}
		key, err := NewEd25519Key(entry.ID, pub, opts...)
		if err != nil {
			return nil, err
		}
		if !key.ValidAt(now) {
			continue
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// resolvePathRef resolves an ADR 0008 reference: an environment variable name
// whose value is the path, else the reference itself as a path.
func resolvePathRef(ref string) string {
	if !strings.ContainsAny(ref, `/\`) {
		if v := strings.TrimSpace(os.Getenv(ref)); v != "" {
			return v
		}
	}
	return ref
}

// ── the portal seam ─────────────────────────────────────────────────────

// PortalAuthenticator adapts the navi's compiled trust sources to the portal
// seam (ADR 0022 decision 8): the navi resolves the credential a portal
// presents, and the portal receives a verified principal reference or a
// refusal — never the vocabulary, roles, or trust sources. It is the only
// place a credential is interpreted, so no portal can reimplement
// verification.
type PortalAuthenticator struct{ trust *TrustSources }

// NewPortalAuthenticator builds the authenticator a portal receives through
// portal.Deps. A nil trust set authenticates nobody (fail closed).
func NewPortalAuthenticator(trust *TrustSources) PortalAuthenticator {
	return PortalAuthenticator{trust: trust}
}

// Authenticate implements portal.Authenticator. Every failure wraps
// portal.ErrUnauthenticated: a credential that does not verify is never
// partially trusted, and an unknown scheme is refused rather than ignored.
//
// Each decision is recorded in the trust ledger (ADR 0027): an authenticated
// credential with the principal it resolved, and a refusal with what was
// presented and why. The ledger never sees the credential itself.
func (a PortalAuthenticator) Authenticate(ctx context.Context, cred portal.Credential) (principal.Ref, error) {
	now := cred.Now
	if now.IsZero() {
		now = time.Now()
	}
	if a.trust == nil {
		return a.refuse(now, cred, "no trust source is declared",
			fmt.Errorf("%w: %w: no trust source is declared", portal.ErrUnauthenticated, ErrUnauthenticated))
	}
	switch strings.ToLower(strings.TrimSpace(cred.Scheme)) {
	case portal.SchemeBearer:
		p, err := a.trust.VerifyToken(ctx, cred.Token, now)
		if err != nil {
			return a.refuse(now, cred, err.Error(), fmt.Errorf("%w: %w", portal.ErrUnauthenticated, err))
		}
		return a.grant(now, cred, p.Ref())
	case portal.SchemeEd25519:
		p, err := a.trust.Resolve(cred.Principal)
		if err != nil {
			return a.refuse(now, cred, err.Error(), fmt.Errorf("%w: %w", portal.ErrUnauthenticated, err))
		}
		if err := p.Verify(cred.Message, cred.Proof, now); err != nil {
			return a.refuse(now, cred, err.Error(), fmt.Errorf("%w: %w", portal.ErrUnauthenticated, err))
		}
		return a.grant(now, cred, p.Ref())
	case "":
		return a.refuse(now, cred, "no credential presented",
			fmt.Errorf("%w: %w: no credential presented", portal.ErrUnauthenticated, ErrUnauthenticated))
	default:
		return a.refuse(now, cred, "unsupported credential scheme",
			fmt.Errorf("%w: %w: unsupported credential scheme %q", portal.ErrUnauthenticated, ErrUnauthenticated, cred.Scheme))
	}
}

// grant records an authenticated credential and returns its principal.
func (a PortalAuthenticator) grant(now time.Time, cred portal.Credential, ref principal.Ref) (principal.Ref, error) {
	if l := a.trust.Ledger(); l != nil {
		l.Append(TrustEvent{
			At: now.UTC(), Kind: TrustAuthenticated, Principal: ref,
			Scheme: strings.ToLower(strings.TrimSpace(cred.Scheme)),
		})
	}
	return ref, nil
}

// refuse records a refusal — with the CLAIMED principal when the presentation
// named one, so an operator sees who was turned away — and returns the error.
// detail is a reason, never the presented material.
func (a PortalAuthenticator) refuse(now time.Time, cred portal.Credential, detail string, err error) (principal.Ref, error) {
	if l := a.trust.Ledger(); l != nil {
		l.Append(TrustEvent{
			At: now.UTC(), Kind: TrustRefused, Principal: cred.Principal,
			Scheme: strings.ToLower(strings.TrimSpace(cred.Scheme)), Detail: detail,
		})
	}
	return principal.Ref{}, err
}

// Compile-time checks: the declared sources satisfy the trust seam, and the
// ordered set is itself a source (a set of sets composes).
var (
	_ TrustSource          = (*TrustSources)(nil)
	_ TrustSource          = (*FileTrustStore)(nil)
	_ TrustSource          = (*OIDC)(nil)
	_ BearerSource         = (*OIDC)(nil)
	_ portal.Authenticator = PortalAuthenticator{}
)
