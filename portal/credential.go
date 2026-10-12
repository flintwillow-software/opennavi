package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/principal"
)

// ErrUnauthenticated is returned when a presented credential does not
// authenticate. It is the fail-closed answer: a portal maps it to its
// transport's refusal (HTTP 401), and an action is never defaulted open.
var ErrUnauthenticated = errors.New("portal: unauthenticated")

// This file is the portal side of ADR 0021 decisions 6-8 and ADR 0022 decision
// 8: a portal presents a credential, the navi resolves it to a verified
// principal, and the portal acts on the answer. A portal NEVER decides whether
// a credential is valid and never evaluates policy — it hands the credential to
// the navi and receives a principal or a refusal.

// Credential presentation schemes.
const (
	// SchemeBearer is a bearer token: an OIDC session for a human principal
	// (ADR 0021 decision 8). Token carries it.
	SchemeBearer = "bearer"
	// SchemeEd25519 is a signed request: a service or peer navi proves
	// possession of an Ed25519 key. Principal, KeyID, Message, and Proof
	// carry it.
	SchemeEd25519 = "ed25519"
)

// Credential is a credential a portal presents at an attachment (ADR 0021
// decision 8). The transport fills it from its wire form; the navi verifies
// it. The zero Credential — no credential at all — is valid input: an
// authenticator refuses it (fail closed), except a development authenticator
// that deliberately does not.
type Credential struct {
	// Scheme selects the presentation: SchemeBearer or SchemeEd25519. Empty
	// means no credential was presented.
	Scheme string
	// Token is the bearer token (SchemeBearer).
	Token string
	// Principal is the claimed principal (SchemeEd25519). It is a CLAIM until
	// the proof verifies.
	Principal principal.Ref
	// KeyID names the authenticator within the principal (SchemeEd25519).
	KeyID string
	// Message is the exact bytes the proof covers (SchemeEd25519).
	Message []byte
	// Proof is the signature over Message (SchemeEd25519).
	Proof []byte
	// SignedAt is the client's timestamp for a signed request (unix seconds),
	// carried on the wire so the verifier can enforce a replay window. It is a
	// claim: the transport checks it against its own clock, and the signature
	// covers it. Zero when the scheme does not use one.
	SignedAt int64
	// Now is the verifier's clock, set by the transport. It is NEVER taken
	// from the wire: a client does not get to choose when its credential is
	// valid. Zero means "use the verifier's current time".
	Now time.Time
}

// Authenticator resolves a presented credential to a verified principal (ADR
// 0021 decisions 6-8). The navi implements it; a portal only calls it (ADR
// 0022 decision 8). It fails closed: an unknown, expired, revoked, or
// malformed credential is an error, never a default principal.
type Authenticator interface {
	// Authenticate returns the principal a credential authenticates, or an
	// error. An empty credential is refused unless the implementation is a
	// deliberate development default.
	Authenticate(ctx context.Context, cred Credential) (principal.Ref, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, cred Credential) (principal.Ref, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, cred Credential) (principal.Ref, error) {
	return f(ctx, cred)
}

// AllowAllAuthenticator accepts every credential — including none — and
// resolves it to who. It is a DEVELOPMENT DEFAULT: the reference navi and a
// trusted local console use it so a local run needs no key material, and it
// MUST NOT be wired on an untrusted network surface. It authenticates nobody
// and distinguishes nobody; pair it with AllowAll only where every connection
// is already trusted (ADR 0021 decision 9 — the in-process console is the
// binary, with no transport).
func AllowAllAuthenticator(who principal.Ref) Authenticator {
	return AuthenticatorFunc(func(context.Context, Credential) (principal.Ref, error) {
		if err := who.Validate(); err != nil {
			return principal.Ref{}, fmt.Errorf("%w: console principal: %w", ErrUnauthenticated, err)
		}
		return who, nil
	})
}

// CredentialSource presents a credential for one request. A remote client
// calls it per request; a static bearer source ignores the message, while a
// signed-request source signs it (so the proof covers the method, the path,
// and the body digest). It lives here because it is the client half of the
// same seam the Authenticator is the server half of.
type CredentialSource interface {
	// Credential returns the credential for a request. method and path are
	// the request's; body is its exact bytes (nil for a bodyless request);
	// now is the client's clock.
	Credential(method, path string, body []byte, now time.Time) (Credential, error)
}

// CredentialSourceFunc adapts a function to CredentialSource.
type CredentialSourceFunc func(method, path string, body []byte, now time.Time) (Credential, error)

// Credential implements CredentialSource.
func (f CredentialSourceFunc) Credential(method, path string, body []byte, now time.Time) (Credential, error) {
	return f(method, path, body, now)
}
