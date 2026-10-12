package gateway

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the transport half of portal credential presentation (ADR 0021
// decision 8, ADR 0022 decision 8): it parses what a client sent into a
// portal.Credential and encodes what a client sends. Verification is the
// navi's — the gateway hands the credential over and acts on the answer.

// DefaultSignatureWindow bounds how far a signed request's timestamp may be
// from the verifier's clock. It is the replay window: a captured signature is
// useless after it, and a skewed client clock is tolerated within it.
//
// It also makes a signed request depend on CLOCK AGREEMENT, and that is a
// transport requirement rather than a credential one. A client whose clock is
// more than the window from the verifier's cannot authenticate at all: an
// off-grid deployment with no NTP source, a device that boots at the epoch
// with no RTC, or an RTC that has drifted past the window over a long
// deployment, each fail every request. Widening the window does not fix that —
// it trades replay protection for a horizon, and an epoch clock is decades
// out regardless. The fix is a time-agreement story on the link (an
// occasional internet window, or peer time sync), which is where the
// requirement belongs.
const DefaultSignatureWindow = 5 * time.Minute

// maxRequestBytes bounds a request body the gateway buffers so a signed
// request can bind it.
const maxRequestBytes = 1 << 20

// authSchemeEd25519 is the Authorization scheme for a signed request. The
// scheme token is deliberately not "Signature": it names the key type, so a
// reader can tell at a glance what the credential is.
const authSchemeEd25519 = "Navi-Ed25519"

// credentialFromRequest parses a request's Authorization header into a
// credential. A request with no Authorization header yields the zero
// credential — no credential at all — which a real authenticator refuses and
// a development authenticator deliberately does not.
func credentialFromRequest(r *http.Request, body []byte, now time.Time) (portal.Credential, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return portal.Credential{Now: now}, nil
	}
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok {
		return portal.Credential{}, fmt.Errorf("%w: malformed Authorization header", portal.ErrUnauthenticated)
	}
	switch strings.ToLower(scheme) {
	case "bearer":
		token := strings.TrimSpace(rest)
		if token == "" {
			return portal.Credential{}, fmt.Errorf("%w: empty bearer token", portal.ErrUnauthenticated)
		}
		return portal.Credential{Scheme: portal.SchemeBearer, Token: token, Now: now}, nil
	case strings.ToLower(authSchemeEd25519):
		fields := strings.Fields(rest)
		if len(fields) != 4 {
			return portal.Credential{}, fmt.Errorf("%w: %s needs <principal> <key-id> <unix-seconds> <signature>", portal.ErrUnauthenticated, authSchemeEd25519)
		}
		ref, err := principal.Parse(fields[0])
		if err != nil {
			return portal.Credential{}, fmt.Errorf("%w: %w", portal.ErrUnauthenticated, err)
		}
		secs, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return portal.Credential{}, fmt.Errorf("%w: signed request timestamp %q", portal.ErrUnauthenticated, fields[2])
		}
		signedAt := time.Unix(secs, 0)
		if d := now.Sub(signedAt); d > DefaultSignatureWindow || d < -DefaultSignatureWindow {
			return portal.Credential{}, fmt.Errorf("%w: signed request is outside the %s window", portal.ErrUnauthenticated, DefaultSignatureWindow)
		}
		proof, err := base64.RawURLEncoding.DecodeString(fields[3])
		if err != nil {
			return portal.Credential{}, fmt.Errorf("%w: signed request signature: %w", portal.ErrUnauthenticated, err)
		}
		return portal.Credential{
			Scheme:    portal.SchemeEd25519,
			Principal: ref,
			KeyID:     fields[1],
			Message:   canonicalRequest(r.Method, r.URL.Path, secs, body),
			Proof:     proof,
			SignedAt:  secs,
			Now:       now,
		}, nil
	default:
		return portal.Credential{}, fmt.Errorf("%w: unsupported Authorization scheme %q", portal.ErrUnauthenticated, scheme)
	}
}

// credentialHeader encodes a credential as an Authorization header value.
func credentialHeader(cred portal.Credential) (string, error) {
	switch strings.ToLower(strings.TrimSpace(cred.Scheme)) {
	case portal.SchemeBearer:
		if cred.Token == "" {
			return "", fmt.Errorf("gateway: bearer credential has no token")
		}
		return "Bearer " + cred.Token, nil
	case portal.SchemeEd25519:
		if cred.Principal.IsZero() || cred.KeyID == "" || len(cred.Proof) == 0 {
			return "", fmt.Errorf("gateway: ed25519 credential is incomplete")
		}
		return fmt.Sprintf("%s %s %s %d %s", authSchemeEd25519, cred.Principal,
			cred.KeyID, cred.SignedAt, base64.RawURLEncoding.EncodeToString(cred.Proof)), nil
	default:
		return "", fmt.Errorf("gateway: unknown credential scheme %q", cred.Scheme)
	}
}

// canonicalRequest is the exact message a signed request covers: the method,
// the path, the client's timestamp, and a digest of the body. Binding the body
// means a captured signature cannot be replayed with different content.
func canonicalRequest(method, path string, unixSeconds int64, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(fmt.Sprintf("%s\n%s\n%d\n%x", method, path, unixSeconds, digest))
}

// BearerToken presents a static bearer token — an OIDC session a human
// obtained from the identity provider (ADR 0021 decision 8).
func BearerToken(token string) portal.CredentialSource {
	return portal.CredentialSourceFunc(func(string, string, []byte, time.Time) (portal.Credential, error) {
		return portal.Credential{Scheme: portal.SchemeBearer, Token: token}, nil
	})
}

// Ed25519Signer presents a signed request: each request is signed with key, so
// the credential is bound to the method, the path, the body, and the time, and
// a revocation takes effect on the very next request.
func Ed25519Signer(ref principal.Ref, keyID string, key ed25519.PrivateKey) (portal.CredentialSource, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("gateway: signer principal: %w", err)
	}
	if strings.TrimSpace(keyID) == "" {
		return nil, fmt.Errorf("gateway: signer key id is required")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("gateway: signer key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	return portal.CredentialSourceFunc(func(method, path string, body []byte, now time.Time) (portal.Credential, error) {
		secs := now.Unix()
		message := canonicalRequest(method, path, secs, body)
		return portal.Credential{
			Scheme:    portal.SchemeEd25519,
			Principal: ref,
			KeyID:     keyID,
			SignedAt:  secs,
			Message:   message,
			Proof:     ed25519.Sign(key, message),
		}, nil
	}), nil
}

// LoadEd25519Key reads a PKCS#8 PEM Ed25519 private key — the operator's own
// key material, referenced by path (ADR 0008: the key is never a literal and
// never leaves the client).
func LoadEd25519Key(path string) (ed25519.PrivateKey, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gateway: read key %s: %w", path, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("gateway: key %s is not PEM", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("gateway: key %s: %w", path, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("gateway: key %s is %T, not an Ed25519 private key", path, parsed)
	}
	return key, nil
}
