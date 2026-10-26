// Package cli is the client half of a navi's command line: the roles that
// talk to a RUNNING navi rather than hosting one.
//
// A navi binary has one hosting role and several client roles. Hosting is the
// navi's own composition (its connectors, tools, model, and portals — see
// package navi); the client roles are the same for every navi, because they
// build no navi at all. A client needs no queue, datastore, provider, or
// model: everything it renders and every action it posts goes through the
// remote object surface and observation stream (ADR 0017).
//
// This package exists so a navi composed in its OWN repository can offer the
// client roles without copying them. `cmd/navi` is its first consumer, and a
// per-navi repo (e.g. navi-bastilli) is the reason it is public:
//
//	func main() {
//	    args := os.Args[1:]
//	    switch role(args) {
//	    case "connect":
//	        err = cli.Connect(args[1:])
//	    case "enqueue":
//	        err = cli.Enqueue(args[1:])
//	    case "health":
//	        err = cli.Health(args[1:])
//	    default:
//	        err = run() // host this navi
//	    }
//	}
//
// Every role takes the argument list AFTER its own name and returns an error;
// the caller decides how to report and exit. None of them calls os.Exit.
package cli

// This file holds the shared plumbing the roles agree on: where a navi is, and
// how a client proves who it is. Keeping it here rather than in each role is
// what makes `connect`, `enqueue`, and `health` resolve the same endpoint.

import (
	"fmt"
	"os"
	"strings"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/principal"
)

// PortalEnv names the environment variable a client role reads for the navi's
// endpoint when no positional argument is given. All the client roles read it,
// so they agree on where a navi is.
const PortalEnv = "NAVI_PORTAL"

// ResolveEndpoint resolves the portal endpoint: the positional argument wins,
// then $NAVI_PORTAL.
func ResolveEndpoint(arg string) (string, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(PortalEnv))
	}
	return NormalizeURL(raw)
}

// NormalizeURL accepts what an operator types ("127.0.0.1:8090") as well as a
// full URL, and refuses to dial nothing.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("a portal URL is required — <command> http://host:port, or set $%s", PortalEnv)
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/"), nil
}

// Credential builds the credential a client presents from the flags a role
// exposes. A token is a bearer credential (an OIDC session); a principal plus
// a key is a signed request.
//
// Neither is an error: a gateway with a real authenticator refuses an
// unauthenticated connection (fail closed), and a development gateway accepts
// it, so the client says what it has and lets the navi decide. The second
// result describes the credential for the operator's orienting line.
func Credential(token, ref, keyPath, keyID string) (portal.CredentialSource, string, error) {
	switch {
	case token != "" && keyPath != "":
		return nil, "", fmt.Errorf("--token and --key are mutually exclusive")
	case token != "":
		return gateway.BearerToken(token), "bearer token", nil
	case keyPath != "":
		if ref == "" {
			return nil, "", fmt.Errorf("--key requires --principal")
		}
		if keyID == "" {
			return nil, "", fmt.Errorf("--key requires --key-id")
		}
		who, err := principal.Parse(ref)
		if err != nil {
			return nil, "", fmt.Errorf("--principal: %w", err)
		}
		key, err := gateway.LoadEd25519Key(keyPath)
		if err != nil {
			return nil, "", err
		}
		signer, err := gateway.Ed25519Signer(who, keyID, key)
		if err != nil {
			return nil, "", err
		}
		return signer, "signed request as " + who.String(), nil
	case ref != "":
		return nil, "", fmt.Errorf("--principal requires --key")
	}
	return nil, "no credential", nil
}
