package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/principal"
)

// runConnect is the `navi connect` client role: a portal attached to a running
// navi's portal gateway. It builds no navi — no queue, datastore, provider, or
// model — because everything it renders and every action it posts goes through
// the remote object surface and observation stream (ADR 0017). That is what
// makes it a role rather than a flag on the host.
//
// The connection is authenticated by the navi (ADR 0021 decisions 6-8): the
// client presents a credential — an OIDC bearer token for a human, or a signed
// request for a service — and the navi answers with a verified principal or a
// refusal. It never asserts an identity.
func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	token := fs.String("token", "", "bearer token (an OIDC session) presented to the navi")
	principalRef := fs.String("principal", "", "principal reference to sign requests as, e.g. service:cli (requires --key)")
	keyPath := fs.String("key", "", "path to a PKCS#8 PEM Ed25519 private key used to sign requests (requires --principal and --key-id)")
	keyID := fs.String("key-id", "", "key id within the principal (requires --key)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	endpoint, err := connectTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	creds, kind, err := connectCredentials(*token, *principalRef, *keyPath, *keyID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("navi: connecting to %s (%s — client mode, no model or datastore)\n", endpoint, kind)
	return tui.RunAttached(ctx, endpoint, creds)
}

// connectCredentials builds the credential the client presents. A token is a
// bearer credential (an OIDC session); a principal plus a key is a signed
// request. Neither is an error: a gateway with a real authenticator refuses an
// unauthenticated connection (fail closed), and a development gateway accepts
// it, so the client says what it has and lets the navi decide. The second
// result describes the credential for the operator's orienting line.
func connectCredentials(token, ref, keyPath, keyID string) (portal.CredentialSource, string, error) {
	switch {
	case token != "" && keyPath != "":
		return nil, "", fmt.Errorf("connect: --token and --key are mutually exclusive")
	case token != "":
		return gateway.BearerToken(token), "bearer token", nil
	case keyPath != "":
		if ref == "" {
			return nil, "", fmt.Errorf("connect: --key requires --principal")
		}
		if keyID == "" {
			return nil, "", fmt.Errorf("connect: --key requires --key-id")
		}
		who, err := principal.Parse(ref)
		if err != nil {
			return nil, "", fmt.Errorf("connect: --principal: %w", err)
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
		return nil, "", fmt.Errorf("connect: --principal requires --key")
	}
	return nil, "no credential", nil
}

// connectTarget resolves the portal endpoint: the positional argument wins,
// then $NAVI_PORTAL — the same endpoint `navi enqueue` uses, so the client
// roles agree on where a navi is.
func connectTarget(arg string) (string, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("NAVI_PORTAL"))
	}
	return normalizePortalURL(raw)
}

// normalizePortalURL accepts what an operator types ("127.0.0.1:8090") as well
// as a full URL, and refuses to dial nothing.
func normalizePortalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("connect: a portal URL is required — navi connect http://host:port, or set $NAVI_PORTAL")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/"), nil
}
