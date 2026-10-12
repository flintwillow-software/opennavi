package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// testAuth authenticates the two presentation schemes the transport carries:
// a bearer token against a fixed value, and an Ed25519 signed request against
// a public key. It is the navi's job in production; the gateway only frames
// the credential.
type testAuth struct {
	pub   ed25519.PublicKey
	token string
	who   principal.Ref
}

func (a testAuth) Authenticate(_ context.Context, cred portal.Credential) (principal.Ref, error) {
	switch cred.Scheme {
	case portal.SchemeBearer:
		if cred.Token == a.token {
			return a.who, nil
		}
	case portal.SchemeEd25519:
		if cred.Principal == a.who && cred.KeyID == "k1" && ed25519.Verify(a.pub, cred.Message, cred.Proof) {
			return a.who, nil
		}
	}
	return principal.Ref{}, fmt.Errorf("%w: refused", portal.ErrUnauthenticated)
}

func newTestAuth(t *testing.T) (testAuth, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return testAuth{pub: pub, token: "session-token", who: principal.MustParse("service:cli")}, priv
}

// newAuthGateway wires a gateway with the given authorizer and authenticator.
func newAuthGateway(t *testing.T, authorizer portal.Authorizer, auth portal.Authenticator) *Gateway {
	t.Helper()
	q := newQueue(t)
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })
	hub := portal.NewControl(authorizer)
	if err := hub.Start(context.Background(), portal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatal(err)
	}
	gw := New("127.0.0.1:0", hub)
	if err := gw.Start(context.Background(), portal.Deps{
		Observation:   broker,
		Authenticator: auth,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Stop() })
	return gw
}

func TestCredentialFromRequestParsesSchemes(t *testing.T) {
	now := time.Now()
	who := principal.MustParse("service:cli")

	req := httptest.NewRequest(http.MethodPost, "/portal/control", nil)
	req.Header.Set("Authorization", "Bearer a-token")
	cred, err := credentialFromRequest(req, nil, now)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if cred.Scheme != portal.SchemeBearer || cred.Token != "a-token" {
		t.Fatalf("bearer credential = %+v", cred)
	}
	if cred.Now != now {
		t.Error("the credential must carry the VERIFIER's clock, not the wire's")
	}

	// A signed request binds the method, path, and body it was built for.
	body := []byte(`{"action":"enqueue"}`)
	message := canonicalRequest(http.MethodPost, "/portal/control", now.Unix(), body)
	req = httptest.NewRequest(http.MethodPost, "/portal/control", strings.NewReader(string(body)))
	req.Header.Set("Authorization", fmt.Sprintf("%s %s k1 %d %s", authSchemeEd25519, who, now.Unix(),
		base64.RawURLEncoding.EncodeToString([]byte("proof"))))
	cred, err = credentialFromRequest(req, body, now)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	if cred.Scheme != portal.SchemeEd25519 || cred.Principal != who || cred.KeyID != "k1" || cred.SignedAt != now.Unix() {
		t.Fatalf("ed25519 credential = %+v", cred)
	}
	if string(cred.Message) != string(message) {
		t.Errorf("signed message = %q, want %q", cred.Message, message)
	}

	// No header is the zero credential — no credential at all, which a real
	// authenticator refuses and a development one deliberately does not.
	req = httptest.NewRequest(http.MethodGet, "/portal/stream", nil)
	cred, err = credentialFromRequest(req, nil, now)
	if err != nil || cred.Scheme != "" {
		t.Fatalf("no header = %+v (%v), want the zero credential", cred, err)
	}
}

func TestCredentialFromRequestRefusesBadCredentials(t *testing.T) {
	now := time.Now()
	who := principal.MustParse("service:cli")

	cases := map[string]string{
		"no scheme":         "just-a-token",
		"unknown scheme":    "Basic dXNlcjpwYXNz",
		"empty bearer":      "Bearer ",
		"short ed25519":     authSchemeEd25519 + " service:cli k1",
		"bad principal":     fmt.Sprintf("%s not-qualified k1 %d sig", authSchemeEd25519, now.Unix()),
		"bad timestamp":     fmt.Sprintf("%s %s k1 not-a-number sig", authSchemeEd25519, who),
		"stale timestamp":   fmt.Sprintf("%s %s k1 %d sig", authSchemeEd25519, who, now.Add(-2*time.Hour).Unix()),
		"future timestamp":  fmt.Sprintf("%s %s k1 %d sig", authSchemeEd25519, who, now.Add(2*time.Hour).Unix()),
		"bad signature b64": fmt.Sprintf("%s %s k1 %d %s", authSchemeEd25519, who, now.Unix(), "!!!"),
	}
	for name, header := range cases {
		req := httptest.NewRequest(http.MethodPost, "/portal/control", nil)
		req.Header.Set("Authorization", header)
		if _, err := credentialFromRequest(req, nil, now); !errors.Is(err, portal.ErrUnauthenticated) {
			t.Errorf("%s = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestCredentialHeaderRoundTrips(t *testing.T) {
	who := principal.MustParse("service:cli")
	now := time.Now()
	for name, cred := range map[string]portal.Credential{
		"bearer": {Scheme: portal.SchemeBearer, Token: "tok"},
		"ed25519": {
			Scheme: portal.SchemeEd25519, Principal: who, KeyID: "k1",
			SignedAt: now.Unix(), Proof: []byte("proof"),
		},
	} {
		header, err := credentialHeader(cred)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		req := httptest.NewRequest(http.MethodPost, "/portal/control", nil)
		req.Header.Set("Authorization", header)
		parsed, err := credentialFromRequest(req, nil, now)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if parsed.Scheme != cred.Scheme {
			t.Errorf("%s: scheme = %q, want %q", name, parsed.Scheme, cred.Scheme)
		}
	}
	if _, err := credentialHeader(portal.Credential{Scheme: "nope"}); err == nil {
		t.Error("an unknown scheme must not encode")
	}
}

// TestGatewayFailsClosedWithoutAuthenticator pins the 402 property: a gateway
// with no authenticator authenticates NOBODY, so every attachment is refused
// before authorization is even consulted.
func TestGatewayFailsClosedWithoutAuthenticator(t *testing.T) {
	gw := newAuthGateway(t, portal.AllowAll(), nil)
	base := "http://" + gw.BoundAddr()

	resp, err := http.Post(base+"/portal/control", "application/json",
		strings.NewReader(`{"action":"enqueue","id":"x","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("control without an authenticator = %d, want 401", resp.StatusCode)
	}

	resp, err = http.Get(base + "/portal/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stream without an authenticator = %d, want 401", resp.StatusCode)
	}
}

// TestGatewayAuthenticatesSignedRequest drives the whole signed-request path:
// the client signs, the transport parses, the navi verifies, and the control
// action runs as the verified principal.
func TestGatewayAuthenticatesSignedRequest(t *testing.T) {
	auth, priv := newTestAuth(t)
	gw := newAuthGateway(t, portal.AllowAll(), auth)
	base := "http://" + gw.BoundAddr()

	if got := doControl(t, base, mustSigner(t, auth.who, "k1", priv), `{"action":"enqueue","id":"s1","prompt":"hi"}`); got != http.StatusOK {
		t.Fatalf("signed control = %d, want 200", got)
	}
	// No credential at all is refused.
	if got := doControl(t, base, nil, `{"action":"enqueue","id":"s2","prompt":"hi"}`); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated control = %d, want 401", got)
	}
}

// TestGatewayRejectsTamperedSignedBody pins body binding: a signature over one
// body does not authorize another.
func TestGatewayRejectsTamperedSignedBody(t *testing.T) {
	auth, priv := newTestAuth(t)
	gw := newAuthGateway(t, portal.AllowAll(), auth)
	base := "http://" + gw.BoundAddr()

	// Sign one body, send a different one.
	original := []byte(`{"action":"enqueue","id":"a","prompt":"hi"}`)
	tampered := `{"action":"enqueue","id":"b","prompt":"owned"}`
	cred, err := mustSigner(t, auth.who, "k1", priv).Credential(http.MethodPost, "/portal/control", original, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	header, err := credentialHeader(cred)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/portal/control", strings.NewReader(tampered))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered body = %d, want 401", resp.StatusCode)
	}
}

func TestGatewayAuthenticatesBearerToken(t *testing.T) {
	auth, _ := newTestAuth(t)
	gw := newAuthGateway(t, portal.AllowAll(), auth)
	base := "http://" + gw.BoundAddr()

	if got := doControl(t, base, BearerToken(auth.token), `{"action":"enqueue","id":"b1","prompt":"hi"}`); got != http.StatusOK {
		t.Fatalf("bearer control = %d, want 200", got)
	}
	// The wrong token is refused.
	if got := doControl(t, base, BearerToken("not-the-token"), `{"action":"enqueue","id":"b2","prompt":"hi"}`); got != http.StatusUnauthorized {
		t.Fatalf("wrong bearer token = %d, want 401", got)
	}
}

// doControl posts one control body presenting creds (nil = none) and returns
// the HTTP status.
func doControl(t *testing.T, base string, creds portal.CredentialSource, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/portal/control", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if creds != nil {
		cred, err := creds.Credential(http.MethodPost, "/portal/control", []byte(body), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		header, err := credentialHeader(cred)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestEd25519SignerValidatesItsInputs(t *testing.T) {
	_, priv := newTestAuth(t)
	if _, err := Ed25519Signer(principal.Ref{}, "k1", priv); err == nil {
		t.Error("a zero principal must be refused")
	}
	if _, err := Ed25519Signer(principal.MustParse("service:x"), "", priv); err == nil {
		t.Error("an empty key id must be refused")
	}
	if _, err := Ed25519Signer(principal.MustParse("service:x"), "k1", nil); err == nil {
		t.Error("a nil key must be refused")
	}
}

func mustSigner(t *testing.T, who principal.Ref, keyID string, priv ed25519.PrivateKey) portal.CredentialSource {
	t.Helper()
	signer, err := Ed25519Signer(who, keyID, priv)
	if err != nil {
		t.Fatalf("Ed25519Signer: %v", err)
	}
	return signer
}
