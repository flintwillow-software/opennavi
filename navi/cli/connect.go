package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flintwillow-software/opennavi/portal/tui"
)

// Connect is the `connect` client role: a portal attached to a running navi's
// portal gateway. It builds no navi — no queue, datastore, provider, or model
// — because everything it renders and every action it posts goes through the
// remote object surface and observation stream (ADR 0017). That is what makes
// it a role rather than a flag on the host.
//
// The connection is authenticated by the navi (ADR 0021 decisions 6-8): the
// client presents a credential — an OIDC bearer token for a human, or a signed
// request for a service — and the navi answers with a verified principal or a
// refusal. It never asserts an identity.
//
// NOTE ON FLAG ORDER: Go's flag parser stops at the first non-flag argument,
// so a credential passed AFTER the address is silently ignored and the client
// connects with none. The flags must precede the URL:
//
//	connect --principal service:cli --key k.pem --key-id k1 10.0.0.51:8090
func Connect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	token := fs.String("token", "", "bearer token (an OIDC session) presented to the navi")
	principalRef := fs.String("principal", "", "principal reference to sign requests as, e.g. service:cli (requires --key)")
	keyPath := fs.String("key", "", "path to a PKCS#8 PEM Ed25519 private key used to sign requests (requires --principal and --key-id)")
	keyID := fs.String("key-id", "", "key id within the principal (requires --key)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	endpoint, err := ResolveEndpoint(fs.Arg(0))
	if err != nil {
		return err
	}
	creds, kind, err := Credential(*token, *principalRef, *keyPath, *keyID)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("connecting to %s (%s — client mode, no model or datastore)\n", endpoint, kind)
	return tui.RunAttached(ctx, endpoint, creds)
}
