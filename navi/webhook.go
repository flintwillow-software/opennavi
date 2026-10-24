package navi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
)

// This file is the navi's webhook ingress (ADR 0029 decisions 2 and 3): ONE
// shared webhook listener with a router, and the router handed to connectors
// through connector.Deps.
//
// The separation the record insists on is enforced here rather than documented:
// the webhook listener serves the connector router and NOTHING else. It does
// not mount the portal gateway, it holds no provider vocabulary, and a webhook
// request can never reach a portal or object route (ADR 0029 decision 1). The
// two authentication models — a provider signature and a verified principal —
// do not share a dispatcher because they do not share a socket.

// WithWebhooks declares the navi's webhook listener on addr (e.g. ":8081").
// Declaring it is what makes a connector's webhook binding reachable: a navi
// with no webhook listener hands connectors a nil router, and a connector that
// declared a webhook binding fails Start naming that (ADR 0029 decision 2).
//
// The address is the navi's, declared once — a connector declares its ROUTE,
// never a port — so exposing a navi to providers is one address and one
// certificate, and every delivery passes one router (ADR 0029, consequences).
func WithWebhooks(addr string) Option {
	return func(n *Navi) {
		n.webhookAddr = addr
		if n.webhooks == nil {
			n.webhooks = connector.NewRouter()
		}
	}
}

// WithWebhookRouter supplies the router the webhook listener serves and
// connectors register on. It exists so a test — or a navi that wants to inspect
// the registered routes — can own the router; WithWebhooks builds a default one.
// The router is shared, so it must be the same value a connector receives.
func WithWebhookRouter(r *connector.Router) Option {
	return func(n *Navi) { n.webhooks = r }
}

// webhookListener is the navi's one webhook listener: it serves the shared
// router on the declared address. It is a connector.Connector so it composes,
// starts, stops, and reports health exactly like every other door — but it
// serves INGRESS for other connectors rather than work of its own, which is why
// it is built by the navi and never added by an author.
type webhookListener struct {
	addr   string
	router *connector.Router

	ln  net.Listener
	srv *http.Server
}

// Name identifies the listener in diagnostics and the health view.
func (l *webhookListener) Name() string { return "webhooks" }

// Addr reports the configured listen address.
func (l *webhookListener) Addr() string { return l.addr }

// BoundAddr reports the address actually bound after Start, or "" when the
// listener is not serving (useful when the configured address used port 0).
func (l *webhookListener) BoundAddr() string {
	if l.ln == nil {
		return ""
	}
	return l.ln.Addr().String()
}

// Start binds the listener and serves the router. It binds the address HERE,
// which is the point of the navi owning the listener: a bind failure is a
// Start failure, reported through the same health path as every other door,
// rather than a surprise at the first delivery.
func (l *webhookListener) Start(ctx context.Context, deps connector.Deps) error {
	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		// The listener's own error already says "listen tcp ... bind: address
		// already in use"; the health view names the listener.
		return err
	}
	l.ln = ln
	srv := &http.Server{
		Handler:           l.router,
		ReadHeaderTimeout: 5 * time.Second,
		// A webhook body is bounded by the connector's own read limit (ADR 0029
		// decision 7), but the listener sets a ceiling too so a request that
		// never finishes cannot pin a connection. It is generous because the
		// bound belongs to the connector, not to the transport.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	l.srv = srv
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			if deps.Log != nil {
				deps.Log.Error("webhooks: serve error", "addr", l.addr, "err", serr)
			}
		}
	}()
	return nil
}

// Stop shuts the listener down with the run. The router is left intact: a
// connector removes its own route at its own Stop, and the navi stops
// connectors in reverse order, so a route never outlives the client behind it
// by more than the shutdown itself.
func (l *webhookListener) Stop() error {
	if l.srv == nil {
		return nil
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := l.srv.Shutdown(shutCtx)
	l.srv = nil
	l.ln = nil
	return err
}

// webhookListenerFor builds the listener a navi declared, or nil when it
// declared none.
func (n *Navi) webhookListenerFor() connector.Connector {
	if n.webhookAddr == "" {
		return nil
	}
	if n.webhooks == nil {
		n.webhooks = connector.NewRouter()
	}
	return &webhookListener{addr: n.webhookAddr, router: n.webhooks}
}

// WebhookRoutes reports the paths the navi's webhook router currently serves,
// sorted. It is a diagnostic: "can this navi be reached by a provider, and on
// what routes?" is answerable from the composition rather than from a log
// (ADR 0029, consequences).
func (n *Navi) WebhookRoutes() []string {
	if n.webhooks == nil {
		return nil
	}
	return n.webhooks.Routes()
}

// compile-time checks.
var (
	_ connector.Connector = (*webhookListener)(nil)
	_ fmt.Stringer        = (*webhookListener)(nil)
)

// String names the listener and its address for logs.
func (l *webhookListener) String() string {
	return fmt.Sprintf("webhooks(%s)", l.addr)
}
