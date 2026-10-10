package github

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The app's key is generated once per test binary rather than embedded: an RSA
// key is a secret, and ADR 0008 forbids secret literals in tests and fixtures.
// Generating it per test would dominate the suite, so it is shared.
var (
	testAppKeyOnce sync.Once
	testAppKey     *rsa.PrivateKey
	testAppKeyErr  error
)

func appKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testAppKeyOnce.Do(func() {
		testAppKey, testAppKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testAppKeyErr != nil {
		t.Fatalf("generate test key: %v", testAppKeyErr)
	}
	return testAppKey
}

// appKeyPEM is the PKCS#1 PEM GitHub issues for an app.
func appKeyPEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(appKey(t)),
	}))
}

// appKeyFile writes the PEM to a temp file and returns its path — the file
// shape of the key reference.
func appKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, []byte(appKeyPEM(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testAppInstallation is a source pointed at a test double, with the shared
// test key. Tests that need a moving clock override Now.
func testAppInstallation(t *testing.T, srv *httptest.Server, now time.Time) *AppInstallation {
	t.Helper()
	return &AppInstallation{
		AppID:          123456,
		InstallationID: 987654,
		KeyRef:         appKeyFile(t),
		BaseURL:        srv.URL,
		HTTPClient:     srv.Client(),
		Now:            func() time.Time { return now },
	}
}

// verifyAppJWT checks the assertion the source sent, independently of the
// source's own signing code: the header is RS256/JWT, the signature verifies
// against the app's public key, and the claims are what GitHub requires — the
// app id, an iat backdated for clock drift, and an exp within GitHub's
// ten-minute maximum.
func verifyAppJWT(assertion string, pub *rsa.PublicKey, appID int64, now time.Time) (jwtClaims, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return jwtClaims{}, fmt.Errorf("JWT has %d segments, want 3", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode JWT header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return jwtClaims{}, fmt.Errorf("parse JWT header: %w", err)
	}
	if header.Alg != "RS256" || header.Typ != "JWT" {
		return jwtClaims{}, fmt.Errorf("JWT header = %+v, want alg RS256 typ JWT", header)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode JWT claims: %w", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return jwtClaims{}, fmt.Errorf("parse JWT claims: %w", err)
	}
	if claims.Iss != appID {
		return jwtClaims{}, fmt.Errorf("iss = %d, want the app id %d", claims.Iss, appID)
	}
	if backdated := now.Sub(time.Unix(claims.Iat, 0)); backdated < 50*time.Second || backdated > 70*time.Second {
		return jwtClaims{}, fmt.Errorf("iat is backdated %v, want ~%v for clock drift", backdated, jwtBackdate)
	}
	if lifetime := time.Unix(claims.Exp, 0).Sub(time.Unix(claims.Iat, 0)); lifetime != jwtLifetime {
		return jwtClaims{}, fmt.Errorf("exp-iat = %v, want %v", lifetime, jwtLifetime)
	}
	if exp := time.Unix(claims.Exp, 0); exp.After(now.Add(10 * time.Minute)) {
		return jwtClaims{}, fmt.Errorf("exp is more than GitHub's ten-minute maximum in the future")
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode JWT signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return jwtClaims{}, fmt.Errorf("JWT signature does not verify: %w", err)
	}
	return claims, nil
}

// TestAppInstallationMintsAndComments is the issue's composed acceptance
// criterion: a connector configured with an AppInstallation comments on an
// issue, against a double that verifies the JWT is RS256-signed and the mint
// call is correct.
func TestAppInstallationMintsAndComments(t *testing.T) {
	key := appKey(t)
	appID, installationID := int64(123456), int64(987654)
	base := time.Unix(1_700_000_000, 0)
	minted := "ghs-" + testToken(t)

	var mintPath, mintMethod, mintAccept, mintUserAgent, commentAuth string
	var mints, comments int
	var verifyErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			mints++
			mintPath, mintMethod = r.URL.Path, r.Method
			mintAccept, mintUserAgent = r.Header.Get("Accept"), r.Header.Get("User-Agent")
			_, verifyErr = verifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &key.PublicKey, appID, base)
			fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, minted, base.Add(time.Hour).Format(time.RFC3339))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			comments++
			commentAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, `{"id":1,"html_url":"https://example.test/c/1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := testAppInstallation(t, srv, base)
	src.AppID, src.InstallationID = appID, installationID

	_, tl := startedConnector(t, srv, Config{Token: src, Repository: "acme/widgets"})
	out, err := tl.Handler(context.Background(), map[string]any{"issue": float64(7), "body": "looked into it"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verifyErr != nil {
		t.Fatalf("the double rejected the JWT: %v", verifyErr)
	}
	if want := fmt.Sprintf("/app/installations/%d/access_tokens", installationID); mintPath != want {
		t.Fatalf("mint path = %q, want %q", mintPath, want)
	}
	if mintMethod != http.MethodPost {
		t.Fatalf("mint method = %q, want POST", mintMethod)
	}
	if mintAccept != "application/vnd.github+json" || mintUserAgent != "opennavi" {
		t.Fatalf("mint headers = accept %q user-agent %q", mintAccept, mintUserAgent)
	}
	// Start asks the source once (so a misconfiguration surfaces early) and the
	// call reuses that token: one mint, not one per call.
	if mints != 1 {
		t.Fatalf("mints = %d, want 1", mints)
	}
	if comments != 1 || commentAuth != "Bearer "+minted {
		t.Fatalf("comments = %d auth = %q, want one comment carrying the minted token", comments, commentAuth)
	}
	if strings.Contains(out, minted) {
		t.Fatalf("the minted token leaked into the tool output: %s", out)
	}
}

// TestAppInstallationCachesAndRefreshes pins the caching the source owns: a
// call inside the token's life reuses it, and one past the refresh window
// re-mints rather than sending a token that could expire mid-request.
func TestAppInstallationCachesAndRefreshes(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		fmt.Fprintf(w, `{"token":"ghs-cached","expires_at":%q}`, base.Add(time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	clock := base
	src := testAppInstallation(t, srv, base)
	src.Now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "ghs-cached" {
			t.Fatalf("token = %q, want the minted token", tok)
		}
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Fatalf("mints = %d, want 1 within the token's lifetime", got)
	}

	// Still well inside the life.
	clock = base.Add(30 * time.Minute)
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Fatalf("mints = %d, want the cache reused at 30m", got)
	}

	// Past expires_at minus the refresh skew.
	clock = base.Add(56 * time.Minute)
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := atomic.LoadInt32(&mints); got != 2 {
		t.Fatalf("mints = %d, want a re-mint past the refresh window", got)
	}
}

// TestAppInstallationFallsBackToAShorterTTLWhenExpiresAtIsMissing pins that a
// response without expires_at cannot keep a token in use past a safe point: the
// fallback is shorter than the real hour.
func TestAppInstallationFallsBackToAShorterTTLWhenExpiresAtIsMissing(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		fmt.Fprint(w, `{"token":"ghs-no-expiry"}`)
	}))
	defer srv.Close()

	clock := base
	src := testAppInstallation(t, srv, base)
	src.Now = func() time.Time { return clock }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	clock = base.Add(DefaultInstallationTokenTTL + time.Minute)
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := atomic.LoadInt32(&mints); got != 2 {
		t.Fatalf("mints = %d, want a re-mint once the fallback TTL passed", got)
	}
}

// TestAppInstallationConcurrentCallsMintOnce is the single-flight requirement:
// concurrent egress calls must not each mint a token. The double holds the first
// mint open so the other callers pile up behind it, which a source that minted
// per call would expose.
func TestAppInstallationConcurrentCallsMintOnce(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintf(w, `{"token":"ghs-once","expires_at":%q}`, base.Add(time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	src := testAppInstallation(t, srv, base)
	const n = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	toks := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			toks[i], errs[i] = src.Token(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Fatalf("mints = %d, want 1 for %d concurrent calls", got, n)
	}
	for i, tok := range toks {
		if tok != "ghs-once" {
			t.Fatalf("call %d token = %q, want the single minted token", i, tok)
		}
	}
}

// TestAppInstallationRedeemsA401Once is the acceptance criterion that a 401
// re-mints once rather than failing the call: an installation token can be
// revoked before its hour is up.
func TestAppInstallationRedeemsA401Once(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tokens := []string{"ghs-first", "ghs-second"}
	var mu sync.Mutex
	var mintCount, commentCount int
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			tok := tokens[min(mintCount, len(tokens)-1)]
			mintCount++
			fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, tok, base.Add(time.Hour).Format(time.RFC3339))
			return
		}
		commentCount++
		auths = append(auths, r.Header.Get("Authorization"))
		if commentCount == 1 {
			http.Error(w, "Bad credentials", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer srv.Close()

	src := testAppInstallation(t, srv, base)
	_, tl := startedConnector(t, srv, Config{Token: src, Repository: "acme/widgets"})
	if _, err := tl.Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"}); err != nil {
		t.Fatalf("a 401 on a revoked token must be recovered by one re-mint: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if mintCount != 2 {
		t.Fatalf("mints = %d, want 2 (the Start mint, then the re-mint after the 401)", mintCount)
	}
	if commentCount != 2 {
		t.Fatalf("comment attempts = %d, want 2", commentCount)
	}
	if auths[0] != "Bearer ghs-first" || auths[1] != "Bearer ghs-second" {
		t.Fatalf("comment auths = %v, want the first token then the re-minted one", auths)
	}
}

// TestStaticTokenIsNotRetriedOn401 pins the boundary of the re-mint: a static
// source has nothing to discard, so its 401 is reported as itself rather than
// retried.
func TestStaticTokenIsNotRetriedOn401(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	token := testToken(t)
	var mu sync.Mutex
	comments := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		comments++
		mu.Unlock()
		http.Error(w, "Bad credentials", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, tl := startedConnector(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})
	_, err := tl.Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("call = %v, want the 401 reported", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if comments != 1 {
		t.Fatalf("comment attempts = %d, want 1: a static source has nothing to re-mint", comments)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the token leaked into the error: %v", err)
	}
}

// TestThePrivateKeyAndTokenNeverLeaveTheAppInstallation is the issue's leak
// criterion, modelled on TestTheTokenNeverLeavesTheCallSite: neither the
// private key (the real, long-lived secret) nor the minted token can appear in
// the tool output, an error, or a struct dump — the shape a log line or a debug
// print produces.
func TestThePrivateKeyAndTokenNeverLeaveTheAppInstallation(t *testing.T) {
	keyPEM := appKeyPEM(t)
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	minted := "ghs-" + testToken(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, minted, time.Now().Add(time.Hour).Format(time.RFC3339))
			return
		}
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	src := &AppInstallation{AppID: 1, InstallationID: 2, KeyRef: path, BaseURL: srv.URL, HTTPClient: srv.Client()}
	c, tl := startedConnector(t, srv, Config{Token: src, Repository: "acme/widgets"})

	// A failed call: the error carries the status and the response body, never
	// the token, and never the key.
	_, err := tl.Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if err == nil {
		t.Fatal("want a failure from the 403")
	}
	if strings.Contains(err.Error(), minted) {
		t.Fatalf("the token leaked into an error: %v", err)
	}
	if strings.Contains(err.Error(), keyPEM) {
		t.Fatalf("the private key leaked into an error: %v", err)
	}

	// Structurally: the connector and its client hold the REFERENCE only, and
	// the source redacts the token it caches, so a dump cannot contain either
	// secret.
	dumps := map[string]string{
		"connector": fmt.Sprintf("%+v", c),
		"client":    fmt.Sprintf("%+v", c.newClient()),
		"source":    fmt.Sprintf("%+v", src),
	}
	for name, dumped := range dumps {
		if strings.Contains(dumped, minted) {
			t.Fatalf("the token leaked into the %s dump: %s", name, dumped)
		}
		if strings.Contains(dumped, keyPEM) {
			t.Fatalf("the private key leaked into the %s dump: %s", name, dumped)
		}
	}
	// The reference itself is not a secret and is visible when the source is the
	// operand (its String method reports it). A nested pointer prints as an
	// address, so the connector dump carries neither secret nor reference — also
	// safe, and pinned above.
	if !strings.Contains(dumps["source"], path) {
		t.Fatalf("the key reference should be present (it is not a secret): %s", dumps["source"])
	}
}

// TestMintErrorCannotEchoTheJWT pins the scrub: a response that reflects the
// Authorization header must not put the assertion into the error the model sees.
func TestMintErrorCannotEchoTheJWT(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer srv.Close()

	src := testAppInstallation(t, srv, base)
	_, err := src.Token(context.Background())
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("the echoed assertion should be scrubbed: %v", err)
	}
	if strings.Contains(err.Error(), "eyJ") {
		t.Fatalf("the JWT leaked into the error: %v", err)
	}
}

// TestAppInstallationReportsConfigurationFailures pins the misconfiguration
// paths an operator will actually hit, and that none of them quote the key.
func TestAppInstallationReportsConfigurationFailures(t *testing.T) {
	keyPEM := appKeyPEM(t)
	badPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not a key")}))
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	cases := []struct {
		name string
		src  *AppInstallation
		want string
	}{
		{"no app id", &AppInstallation{InstallationID: 2, KeyRef: "K"}, "AppID"},
		{"no installation id", &AppInstallation{AppID: 1, KeyRef: "K"}, "InstallationID"},
		{"no key reference", &AppInstallation{AppID: 1, InstallationID: 2}, "KeyRef"},
		{"reference is neither env nor file",
			&AppInstallation{AppID: 1, InstallationID: 2, KeyRef: filepath.Join(t.TempDir(), "absent.pem")},
			"neither a set environment variable nor an existing file"},
		{"not a PEM block",
			&AppInstallation{AppID: 1, InstallationID: 2, KeyRef: "K", Env: envWith("K", "not pem")},
			"not a PEM block"},
		{"PEM is not a key",
			&AppInstallation{AppID: 1, InstallationID: 2, KeyRef: "K", Env: envWith("K", badPEM)},
			"not a valid private key"},
		{"not an RSA key",
			&AppInstallation{AppID: 1, InstallationID: 2, KeyRef: "K", Env: envWith("K", ecPEM)},
			"not an RSA private key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.src.Token(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Token = %v, want an error naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), keyPEM) {
				t.Fatalf("the key leaked into the error: %v", err)
			}
		})
	}
}

// TestAppInstallationReportsMintFailures pins the exchange's failure paths.
func TestAppInstallationReportsMintFailures(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http error", http.StatusUnauthorized, `{"message":"Bad credentials"}`, "http 401"},
		{"not installed", http.StatusNotFound, `{"message":"Integration not found"}`, "Integration not found"},
		{"no token", http.StatusCreated, `{"expires_at":"2026-01-01T00:00:00Z"}`, "carried no token"},
		{"malformed json", http.StatusCreated, `not json`, "decode response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			src := testAppInstallation(t, srv, base)
			_, err := src.Token(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Token = %v, want an error naming %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "app 123456 installation 987654") {
				t.Fatalf("the error must name the app and installation (they are not secrets): %v", err)
			}
		})
	}
}

// TestAppInstallationResolvesKeyFromEnvThenFile pins the reference's two shapes
// and the precedence between them: an environment variable first, then a file
// path — the same order as the config merge chain.
func TestAppInstallationResolvesKeyFromEnvThenFile(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"token":"ghs-key","expires_at":%q}`, base.Add(time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	t.Run("environment variable", func(t *testing.T) {
		src := &AppInstallation{
			AppID: 1, InstallationID: 2,
			KeyRef:  "NAVI_TEST_APP_KEY",
			Env:     envWith("NAVI_TEST_APP_KEY", appKeyPEM(t)),
			BaseURL: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return base },
		}
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("an env-var key reference must resolve: %v", err)
		}
	})

	t.Run("file path", func(t *testing.T) {
		src := &AppInstallation{
			AppID: 1, InstallationID: 2,
			KeyRef:  appKeyFile(t),
			Env:     func(string) string { return "" },
			BaseURL: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return base },
		}
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("a file key reference must resolve: %v", err)
		}
	})

	t.Run("PKCS#8 PEM", func(t *testing.T) {
		der, err := x509.MarshalPKCS8PrivateKey(appKey(t))
		if err != nil {
			t.Fatal(err)
		}
		pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		src := &AppInstallation{
			AppID: 1, InstallationID: 2,
			KeyRef: "K", Env: envWith("K", pkcs8),
			BaseURL: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return base },
		}
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("a PKCS#8 key must resolve: %v", err)
		}
	})
}

// TestInvalidateOnlyDiscardsTheNamedToken pins the token-scoped invalidation: a
// 401 on a token that has since been replaced must not throw the replacement
// away.
func TestInvalidateOnlyDiscardsTheNamedToken(t *testing.T) {
	src := &AppInstallation{AppID: 1, InstallationID: 2, KeyRef: "K"}
	src.token = "old"
	src.expires = time.Now().Add(time.Hour)

	src.Invalidate("stale")
	if src.token != "old" {
		t.Fatal("Invalidate discarded a token that was not the one named")
	}
	src.Invalidate("old")
	if src.token != "" {
		t.Fatal("Invalidate did not discard the named token")
	}
}

// TestAppInstallationStringRedactsTheCachedToken pins the dump hygiene directly:
// the source is reachable from the connector's Config.Token, so a %+v of the
// connector must not print the cached token.
func TestAppInstallationStringRedactsTheCachedToken(t *testing.T) {
	src := &AppInstallation{AppID: 7, InstallationID: 8, KeyRef: "ref"}
	src.token = "ghs-must-not-appear"

	dumped := fmt.Sprintf("%+v", src)
	if strings.Contains(dumped, "ghs-must-not-appear") {
		t.Fatalf("the cached token leaked into the dump: %s", dumped)
	}
	for _, want := range []string{"app:7", "installation:8", "ref", "token cached"} {
		if !strings.Contains(dumped, want) {
			t.Fatalf("dump %q does not report %q", dumped, want)
		}
	}
}
