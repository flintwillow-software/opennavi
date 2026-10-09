package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/flintwillow-software/opennavi/portal/tui"
)

// runConnect is the `navi connect` client role: a portal attached to a running
// navi's portal gateway. It builds no navi — no queue, datastore, provider, or
// model — because everything it renders and every action it posts goes through
// the remote object surface and observation stream (ADR 0017). That is what
// makes it a role rather than a flag on the host.
func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	identity := fs.String("identity", "cli", "identity presented to the navi")
	if err := fs.Parse(args); err != nil {
		return err
	}
	endpoint, err := connectTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("navi: connecting to %s as %s (client mode — no model or datastore)\n", endpoint, *identity)
	return tui.RunAttached(ctx, endpoint, *identity)
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
