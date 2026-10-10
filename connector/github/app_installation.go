package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultInstallationTokenTTL is the fallback lifetime used when GitHub's mint
// response carries no parsable expires_at. An installation token's real
// lifetime is one hour; the fallback is deliberately shorter so a response
// missing the field cannot make the source keep a token past its expiry.
const DefaultInstallationTokenTTL = 55 * time.Minute

// installationTokenRefreshSkew is how long before expiry a cached token is
// re-minted. Re-minting early means a token that is still valid is never sent
// on a request that could outlive it, and it absorbs a small clock difference
// between this host and GitHub.
const installationTokenRefreshSkew = 5 * time.Minute

const (
	// jwtBackdate is how far the JWT's iat is moved into the past. GitHub
	// recommends this to survive clock drift: a host whose clock is slightly
	// ahead would otherwise sign a token that is "not yet valid" to GitHub.
	jwtBackdate = 60 * time.Second
	// jwtLifetime is the JWT's lifetime. GitHub accepts at most 10 minutes, so
	// this is the longest a single exchange can use.
	jwtLifetime = 10 * time.Minute
)

// AppInstallation is a TokenSource over a GitHub App installation (ADR 0020
// decision 8): it signs an RS256 JWT with the app's private key, exchanges it
// for an installation access token, and re-mints before that token expires. It
// is the production-shaped credential for a long-lived integration — the token
// is short-lived, automatic-expiry, fine-grained, and scoped to one
// installation, rather than a long-lived token tied to a person.
//
// It is added through the existing Config.Token, so nothing else in the
// connector changes:
//
//	gh := github.New(github.Config{
//		Token: &github.AppInstallation{
//			AppID:          123456,
//			InstallationID: 987654,
//			KeyRef:         "/etc/navi/github-app.pem", // or an env var name
//		},
//		Repository: "acme/widgets",
//	})
//
// The installation ID is declared rather than discovered. GET /app/installations
// returns every installation of the app — an app may be installed on several
// accounts — so discovery would only relocate the choice into a config rule and
// make "the first one" a silent wrong-target bug; declaring it keeps the acted-as
// installation auditable in main.go and the credential path to one exchange.
//
// KeyRef is a reference, never the key (ADR 0008): an environment variable name
// holding the PEM, else a file path to it, tried in that order — the same
// environment-over-file precedence as the config merge chain. The key is the
// real secret and outlives every token it mints, so it is read at the point of
// use and never stored: only the short-lived token is cached, and no key or
// token value ever reaches an error, a log, or the transcript.
//
// An AppInstallation holds a cache and must not be copied; hold it by pointer.
// The zero value is not usable: it needs an app ID, an installation ID, and a
// key reference.
type AppInstallation struct {
	// AppID is the GitHub App's numeric id ("App ID" in the app's settings).
	AppID int64
	// InstallationID is the installation to act as. Declared, not discovered;
	// see the type comment.
	InstallationID int64
	// KeyRef names where the app's PEM private key lives — an environment
	// variable holding the PEM, or a file path to it, never the key (ADR 0008).
	KeyRef string
	// BaseURL overrides the API root (GitHub Enterprise, tests).
	// Empty = DefaultBaseURL.
	BaseURL string
	// HTTPClient is the transport used for the token exchange. Nil = a client
	// with a 20s timeout.
	HTTPClient *http.Client
	// Env resolves KeyRef when it names an environment variable. Nil = os.Getenv.
	Env func(string) string
	// Now is the clock, so a test can drive expiry. Nil = time.Now.
	Now func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Token implements TokenSource. It returns the cached installation token while
// it is usable, and otherwise mints one.
//
// The mutex is held across the mint so concurrent egress calls do not each
// mint: the first caller mints, the others wait, and the re-check finds the
// fresh token rather than starting a second exchange. That is the single-flight
// the seam requires — a token per call would be an exchange per call, and
// GitHub rate-limits the exchange.
func (a *AppInstallation) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tok, ok := a.cachedLocked(); ok {
		return tok, nil
	}
	tok, expires, err := a.mint(ctx)
	if err != nil {
		return "", err
	}
	a.token, a.expires = tok, expires
	return tok, nil
}

// Invalidate implements TokenInvalidator. The egress path calls it when GitHub
// answers 401, because an installation token can be revoked before its hour is
// up. Only the named token is discarded, so a 401 on a token that has since
// been replaced does not throw the replacement away.
func (a *AppInstallation) Invalidate(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == token {
		a.token = ""
		a.expires = time.Time{}
	}
}

// String reports the installation's identity and never its cached token: a
// struct dump is the shape a log line or a debug print produces, and a direct
// `%v` of the source would otherwise print the token it is holding. The app and
// installation ids are not secrets, and the key reference is a reference, not a
// value (ADR 0008).
func (a *AppInstallation) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := "no token"
	if a.token != "" {
		state = "token cached"
	}
	return fmt.Sprintf("AppInstallation{app:%d installation:%d key:%q %s}", a.AppID, a.InstallationID, a.KeyRef, state)
}

// cachedLocked reports whether the held token is still usable. Callers must
// hold a.mu.
func (a *AppInstallation) cachedLocked() (string, bool) {
	if a.token == "" {
		return "", false
	}
	if !a.now().Before(a.expires.Add(-installationTokenRefreshSkew)) {
		return "", false
	}
	return a.token, true
}

func (a *AppInstallation) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// mint signs a JWT and exchanges it for an installation access token. The
// error names the app and installation (which are not secrets) and never the
// key or the token.
func (a *AppInstallation) mint(ctx context.Context) (string, time.Time, error) {
	if a.AppID <= 0 {
		return "", time.Time{}, errors.New("github: app installation needs a positive AppID")
	}
	if a.InstallationID <= 0 {
		return "", time.Time{}, errors.New("github: app installation needs a positive InstallationID")
	}
	key, err := a.privateKey()
	if err != nil {
		return "", time.Time{}, err
	}
	now := a.now()
	assertion, err := signAppJWT(a.AppID, key, now)
	if err != nil {
		return "", time.Time{}, err
	}

	base := strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", base, a.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github: build installation token request: %w", err)
	}
	// The JWT is the Authorization value here and nowhere else. It is a
	// short-lived assertion, not the installation token, but it is still a
	// credential: it goes in a header, never a query parameter, and a response
	// body that echoed it is scrubbed before it can reach an error.
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opennavi")

	hc := a.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Deliberately not wrapping the request: a transport error must not be
		// able to echo the Authorization header into the transcript.
		return "", time.Time{}, fmt.Errorf("github: mint installation token for app %d installation %d: %w", a.AppID, a.InstallationID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body is GitHub's own message ("Integration not found" is the
		// actionable part), scrubbed of the JWT first: a response that echoed the
		// request must not put a credential into the error the model sees.
		return "", time.Time{}, fmt.Errorf("github: mint installation token for app %d installation %d: http %d: %s",
			a.AppID, a.InstallationID, resp.StatusCode, scrub(strings.TrimSpace(string(body)), assertion))
	}

	var minted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		return "", time.Time{}, fmt.Errorf("github: mint installation token for app %d installation %d: decode response: %w", a.AppID, a.InstallationID, err)
	}
	token := strings.TrimSpace(minted.Token)
	if token == "" {
		return "", time.Time{}, fmt.Errorf("github: mint installation token for app %d installation %d: response carried no token", a.AppID, a.InstallationID)
	}
	// expires_at is authoritative when present; the fallback is shorter than
	// the real hour, so a missing field cannot keep a stale token in use.
	expires := now.Add(DefaultInstallationTokenTTL)
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(minted.ExpiresAt)); err == nil {
		expires = at
	}
	return token, expires, nil
}

// privateKey resolves KeyRef and parses the PEM. Only the RSA key it returns is
// used; the reference, the PEM bytes, and the key are never stored on the
// source.
func (a *AppInstallation) privateKey() (*rsa.PrivateKey, error) {
	ref := strings.TrimSpace(a.KeyRef)
	if ref == "" {
		return nil, errors.New("github: app installation has no private key reference; set AppInstallation.KeyRef (ADR 0008)")
	}
	pemBytes, err := a.readKeyRef(ref)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("github: private key reference %q is not a PEM block", ref)
	}
	// GitHub issues a PKCS#1 ("BEGIN RSA PRIVATE KEY") PEM; PKCS#8
	// ("BEGIN PRIVATE KEY") is accepted too, since that is what some tooling
	// produces from the same key.
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: private key reference %q is not a valid private key", ref)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github: private key reference %q is not an RSA private key; GitHub App JWTs are RS256", ref)
	}
	return key, nil
}

// readKeyRef resolves a private key reference the way the config chain does: an
// environment variable first, then a file path. Neither the resolved value nor
// the file contents can appear in the error.
func (a *AppInstallation) readKeyRef(ref string) ([]byte, error) {
	env := a.Env
	if env == nil {
		env = os.Getenv
	}
	if v := env(ref); strings.TrimSpace(v) != "" {
		return []byte(v), nil
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("github: private key reference %q is neither a set environment variable nor an existing file", ref)
		}
		return nil, fmt.Errorf("github: read private key reference %q: %w", ref, err)
	}
	return data, nil
}

// jwtHeader and jwtClaims are the JWT's two JSON segments. iss is the app ID;
// GitHub accepts it as a number.
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Iat int64 `json:"iat"`
	Exp int64 `json:"exp"`
	Iss int64 `json:"iss"`
}

// signAppJWT signs the app's RS256 JWT with stdlib primitives — no JWT
// dependency. iat is backdated by jwtBackdate so a host whose clock runs
// slightly ahead still signs a token GitHub considers valid, and exp is the
// longest GitHub accepts.
func signAppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	header, err := json.Marshal(jwtHeader{Alg: "RS256", Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("github: encode JWT header: %w", err)
	}
	iat := now.Add(-jwtBackdate)
	claims, err := json.Marshal(jwtClaims{
		Iat: iat.Unix(),
		Exp: iat.Add(jwtLifetime).Unix(),
		Iss: appID,
	})
	if err != nil {
		return "", fmt.Errorf("github: encode JWT claims: %w", err)
	}
	signing := base64URL(header) + "." + base64URL(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign app JWT: %w", err)
	}
	return signing + "." + base64URL(sig), nil
}

func base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// scrub removes each secret from s. It is how a response body can be quoted in
// an error without letting a value that echoed the request reach the model.
func scrub(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
