package connector

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// This file is the webhook ingress seam (ADR 0029 decisions 1-3): the navi
// owns ONE shared webhook listener and a router; a connector owns its route and
// the handler that verifies and processes a delivery.
//
// It lives in the contract connector package and references only the standard
// library. That is deliberate and is ADR 0029 decision 2's requirement: the
// seam is a handle the navi supplies through Deps, so it stays contract-pure.
// The router NEVER parses, verifies, or interprets a payload — it maps a path
// to a handler — which is what keeps it plumbing rather than the generic
// provider dispatcher ADR 0014 decision 3 rejected.

// ErrNoWebhookRouter reports a connector declaring a webhook binding in a navi
// that composed no webhook listener. The binding is a build-time declaration
// (ADR 0023 decision 1), so a navi that has routes to serve but no listener to
// serve them on is a composition error rather than a runtime degradation — the
// routes could never be reached.
var ErrNoWebhookRouter = errors.New("connector: no webhook router; the navi composed no webhook listener (ADR 0029 decision 2)")

// WebhookRouter maps a webhook request to the connector-registered handler for
// its path. It is the whole of the navi-side contract a connector needs: the
// connector declares a route and registers a handler, and the navi's one
// webhook listener dispatches through it.
//
// The router is deliberately dumb. It holds no provider vocabulary, resolves no
// credential, and verifies no signature — a handler does all three, because
// provider-specific semantics belong to the connector (ADR 0029 decision 1).
// The router's only jobs are dispatch and the events-log choke point (decision
// 3), which is why it is the navi's and not the connector's.
//
// A handler is an http.Handler rather than a bespoke function type for one
// reason: the connector author writes ordinary HTTP, and the connector package
// gains no type that has to be explained. The connector is responsible for
// reading the body whole and bounded, verifying the signature BEFORE parsing
// (decision 4), and answering with the status table in decision 9.
type WebhookRouter interface {
	// Handle registers a handler for a route path (e.g. "/github"). It is
	// called at Start and the navi removes the route at Stop. Registering the
	// same path twice is an error rather than a silent replacement, so two
	// connectors cannot shadow each other's route.
	Handle(path string, h http.Handler) error
	// Remove unregisters a route, so a stopped connector serves 404 rather
	// than a handler over a cleared client.
	Remove(path string)
}

// Route is the shape a router validates: a non-empty, rooted path with no
// query or fragment. It is exported so a connector can fail a declaration at
// composition time with the same rule the router enforces at Start.
func Route(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return errors.New("connector: webhook route requires a path")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("connector: webhook route %q must be rooted (start with /)", path)
	}
	if strings.ContainsAny(p, "?#") {
		return fmt.Errorf("connector: webhook route %q must not carry a query or fragment", path)
	}
	return nil
}

// Router is the default WebhookRouter: a path-keyed handler table safe for
// concurrent registration and dispatch. The navi's webhook listener serves it;
// a test can serve it directly with httptest.
//
// A Router must not be copied: it holds a lock. Hold it by pointer.
type Router struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

// NewRouter builds an empty router.
func NewRouter() *Router {
	return &Router{handlers: map[string]http.Handler{}}
}

// Handle implements WebhookRouter. A duplicate path is an error, so a second
// connector declaring a taken route fails its Start loudly rather than
// silently stealing the first one's traffic.
func (r *Router) Handle(path string, h http.Handler) error {
	if err := Route(path); err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("connector: webhook route %q has a nil handler", path)
	}
	p := strings.TrimSpace(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.handlers[p]; taken {
		return fmt.Errorf("connector: webhook route %q is already registered", p)
	}
	r.handlers[p] = h
	return nil
}

// Remove implements WebhookRouter.
func (r *Router) Remove(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, strings.TrimSpace(path))
}

// ServeHTTP implements http.Handler: it dispatches to the registered handler,
// and answers 404 for an unknown route. It deliberately does nothing else — no
// logging of the body, no signature check, no event vocabulary (ADR 0029
// decision 1).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	h, ok := r.handlers[req.URL.Path]
	r.mu.RUnlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	h.ServeHTTP(w, req)
}

// Routes returns the registered paths, sorted for stable output. It is a
// diagnostic: what a navi can be reached by is answerable from its composition
// (ADR 0029, consequences).
func (r *Router) Routes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for p := range r.handlers {
		out = append(out, p)
	}
	sortStrings(out)
	return out
}

// sortStrings is a tiny insertion sort so this file needs no `sort` import for
// a diagnostic path. Route counts are single digits in practice.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
