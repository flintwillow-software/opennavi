// Package gateway implements the HTTP portal gateway: a portal.Portal that
// serves the navi's portal surface over HTTP — the bounded observation
// stream, the control channel, and (ADR 0017 decision 10) the typed-object
// surface: the type descriptors, bounded object and detail reads, declared
// actions, and the object delta stream. A remote front-end therefore binds
// the same contract an in-process portal is handed.
//
// It is a *transport* portal: framing and HTTP live here, while
// authorization and the actual control actions delegate to another
// portal (the shared Hub). That keeps one authorization path — the
// gateway never decides who may control a thought. The object routes are
// authorized on that same attachment path and the surface authorizes the
// actions themselves, so a remote portal cannot perform what an in-process
// one could not.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// DefaultSubscriptionCap bounds one served observation attachment: a
// client that reads slower than the runtime emits loses the oldest (the
// cursor reports the gap), so a slow attachment never slows the queue.
const DefaultSubscriptionCap = 4096

// Gateway serves a navi's portal over HTTP. It satisfies portal.Portal
// (control methods delegate to the wrapped control portal) and
// portal.Addressed (it binds a listen address).
type Gateway struct {
	addr    string
	control portal.Portal

	obs     portal.Observer
	sub     portal.Subscription
	objects object.Surface
	// auth resolves a presented credential to a verified principal (ADR 0021
	// decisions 6-8). Nil refuses every attachment: a gateway with no
	// authenticator authenticates nobody (fail closed).
	auth portal.Authenticator
	ln   net.Listener
	srv  *http.Server
	log  *slog.Logger
}

// New builds a gateway that serves on addr and delegates attach/control to
// control (usually the navi's Hub). control may be nil: the gateway then
// serves observation only and refuses control, fail-closed.
func New(addr string, control portal.Portal) *Gateway {
	return &Gateway{addr: addr, control: control}
}

// Handler returns the gateway HTTP surface without owning a listener, so a
// host that already runs an HTTP server (the reference navi's serve path)
// can mount it. It subscribes one observation mailbox from obs; obs may be
// nil (observation endpoints then answer 503), as may objects (the object
// routes then answer 503). auth resolves a presented credential to a verified
// principal; nil refuses every attachment (fail closed).
func Handler(control portal.Portal, obs portal.Observer, objects object.Surface, auth portal.Authenticator) http.Handler {
	g := &Gateway{control: control, objects: objects, auth: auth}
	if obs != nil {
		g.obs = obs
		g.sub = obs.Subscribe(DefaultSubscriptionCap)
	}
	return g
}

// Addr reports the configured listen address (portal.Addressed): the
// composition root uses it to detect port conflicts before serving.
func (g *Gateway) Addr() string { return g.addr }

// BoundAddr reports the actual bound address after Start (useful when the
// configured address used port 0).
func (g *Gateway) BoundAddr() string {
	if g.ln == nil {
		return ""
	}
	return g.ln.Addr().String()
}

// Start binds the gateway to the navi's observation source and serves.
// ctx cancellation is handled by the composition root's Stop (the server
// lifecycle mirrors the metrics/portal listener pattern).
func (g *Gateway) Start(ctx context.Context, deps portal.Deps) error {
	if deps.Observation == nil {
		return fmt.Errorf("gateway: observation source is required")
	}
	if deps.Log != nil {
		g.log = deps.Log
	}
	g.obs = deps.Observation
	g.sub = deps.Observation.Subscribe(DefaultSubscriptionCap)
	// The object surface is handed in the same Deps (ADR 0017): the gateway
	// serves whatever the navi exposes, exactly as the in-process portal does.
	g.objects = deps.Objects
	// The navi resolves trust for its portals (ADR 0022 decision 8): the
	// gateway presents the credential and acts on the answer.
	g.auth = deps.Authenticator

	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", g.addr, err)
	}
	g.ln = ln
	// The goroutine keeps its OWN reference: Stop nils g.srv, and reading the
	// field from the serve goroutine raced that write — a fast shutdown
	// (a daemon whose stdin is already at EOF, an aborted run) dereferenced
	// the nil server and panicked.
	srv := &http.Server{
		Handler:           g,
		ReadHeaderTimeout: 5 * time.Second,
	}
	g.srv = srv
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			g.logf("gateway: serve error", "err", serr.Error())
		}
	}()
	return nil
}

// Stop shuts the server down and closes the observation subscription.
func (g *Gateway) Stop() error {
	if g.srv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = g.srv.Shutdown(shutCtx)
		g.srv = nil
	}
	if g.sub != nil {
		g.sub.Close()
		g.sub = nil
	}
	return nil
}

func (g *Gateway) logf(msg string, kv ...any) {
	if g.log != nil {
		g.log.Error(msg, kv...)
	}
}

// ── control delegation (portal.Portal) ──────────────────────────────────

func (g *Gateway) delegate(fn func(portal.Portal) error) error {
	if g.control == nil {
		return fmt.Errorf("gateway: no control portal bound")
	}
	return fn(g.control)
}

// Attach authorizes a verified principal for the portal attachment.
func (g *Gateway) Attach(who principal.Ref) error {
	return g.delegate(func(p portal.Portal) error { return p.Attach(who) })
}

// Steer routes a steering message to a thought.
func (g *Gateway) Steer(who principal.Ref, thoughtID, message string) error {
	return g.delegate(func(p portal.Portal) error { return p.Steer(who, thoughtID, message) })
}

// Pause parks a running thought.
func (g *Gateway) Pause(who principal.Ref, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Pause(who, thoughtID) })
}

// Resume re-queues a paused thought.
func (g *Gateway) Resume(who principal.Ref, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Resume(who, thoughtID) })
}

// Cancel removes queued/paused work.
func (g *Gateway) Cancel(who principal.Ref, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Cancel(who, thoughtID) })
}

// BumpPriority changes a queued thought's priority.
func (g *Gateway) BumpPriority(who principal.Ref, thoughtID string, prio int) error {
	return g.delegate(func(p portal.Portal) error { return p.BumpPriority(who, thoughtID, prio) })
}

// Retry re-queues a failed thought.
func (g *Gateway) Retry(who principal.Ref, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Retry(who, thoughtID) })
}

// Enqueue schedules a thought.
func (g *Gateway) Enqueue(who principal.Ref, t *thought.Thought) error {
	return g.delegate(func(p portal.Portal) error { return p.Enqueue(who, t) })
}

// ── HTTP surface ────────────────────────────────────────────────────────

// ServeHTTP routes the gateway endpoints. The portal path is the unified
// stream (`GET /portal/stream` for the SSE downstream, `POST /portal/stream`
// for upstream calls); the control channel keeps `/portal/control` for
// non-portal clients (navi enqueue), and the typed-object surface owns the
// type root (`/{type}`, `/{type}/{id}`, `/{type}/{id}/{detail}`,
// `POST /{type}/{id}/{action}`) with its own descriptor route namespaced
// under `_` (`/_types`), so `portal` and `_`-prefixed roots are reserved
// type names.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Buffer a body once, so a signed request can bind it and every handler
	// reads the same bytes. GET and HEAD carry none.
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
		if err != nil {
			http.Error(w, "gateway: read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, body))
	}
	path := strings.Trim(r.URL.Path, "/")
	switch path {
	case "portal/stream":
		switch r.Method {
		case http.MethodGet:
			g.serveStream(w, r)
		case http.MethodPost:
			g.serveStreamCall(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "portal/control":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g.serveControl(w, r)
	case "_types":
		g.serveTypes(w, r)
	case "":
		http.NotFound(w, r)
	default:
		g.serveObject(w, r, strings.Split(path, "/"))
	}
}

// bodyKey carries the buffered request body so authenticate can bind it to a
// signed request after the body has been read once.
type bodyKey struct{}

func requestBody(r *http.Request) []byte {
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	return body
}

// authenticate resolves the request's credential to a verified principal. It
// is the ONLY place a connection's identity is decided (ADR 0022 decision 8):
// the gateway presents what the client sent and acts on the navi's answer. A
// gateway with no authenticator authenticates nobody — fail closed.
func (g *Gateway) authenticate(r *http.Request) (principal.Ref, error) {
	if g.auth == nil {
		return principal.Ref{}, fmt.Errorf("%w: no authenticator wired (fail closed)", portal.ErrUnauthenticated)
	}
	cred, err := credentialFromRequest(r, requestBody(r), time.Now())
	if err != nil {
		return principal.Ref{}, err
	}
	return g.auth.Authenticate(r.Context(), cred)
}

// controlRequest is the POST /portal/control body. It carries NO identity:
// the acting principal comes from the verified credential (ADR 0022 decision
// 8), never from the body.
type controlRequest struct {
	Action    string  `json:"action"`
	ThoughtID string  `json:"thoughtID"`
	Text      string  `json:"text"`
	ID        string  `json:"id"`
	Prompt    string  `json:"prompt"`
	Priority  float64 `json:"priority"`
}

// serveControl answers POST /portal/control. The connection is authenticated
// first and the attachment authorized; every action is then authorizer-gated
// by the control portal with the VERIFIED principal, and with a nil authorizer
// all actions are refused 403 (fail closed).
func (g *Gateway) serveControl(w http.ResponseWriter, r *http.Request) {
	if g.control == nil {
		http.Error(w, "gateway: no control portal bound", http.StatusServiceUnavailable)
		return
	}
	who, err := g.authenticate(r)
	if err != nil {
		g.refusal(w, err)
		return
	}
	if err := g.control.Attach(who); err != nil {
		g.refusal(w, err)
		return
	}
	blob, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, "gateway: read body: "+rerr.Error(), http.StatusBadRequest)
		return
	}
	var req controlRequest
	if err := json.Unmarshal(blob, &req); err != nil {
		http.Error(w, "gateway: control body is not an object", http.StatusBadRequest)
		return
	}

	var ctrlErr error
	switch strings.ToLower(req.Action) {
	case "attach":
		ctrlErr = g.control.Attach(who)
	case "steer":
		ctrlErr = g.control.Steer(who, req.ThoughtID, req.Text)
	case "pause":
		ctrlErr = g.control.Pause(who, req.ThoughtID)
	case "resume":
		ctrlErr = g.control.Resume(who, req.ThoughtID)
	case "cancel":
		ctrlErr = g.control.Cancel(who, req.ThoughtID)
	case "bump":
		prio := 4
		if req.Priority >= 1 && req.Priority <= 10 {
			prio = int(req.Priority)
		}
		ctrlErr = g.control.BumpPriority(who, req.ThoughtID, prio)
	case "retry":
		ctrlErr = g.control.Retry(who, req.ThoughtID)
	case "enqueue":
		now := time.Now().UTC()
		t := &thought.Thought{
			ID:        req.ID,
			Prompt:    req.Prompt,
			Priority:  4,
			Status:    thought.ThoughtPending,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if req.Priority >= 1 && req.Priority <= 10 {
			t.Priority = int(req.Priority)
		}
		ctrlErr = g.control.Enqueue(who, t)
	default:
		http.Error(w, fmt.Sprintf("gateway: unknown action %q", req.Action), http.StatusBadRequest)
		return
	}

	if ctrlErr != nil {
		g.refusal(w, ctrlErr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// refusal maps a control or authentication error onto an HTTP status: a
// credential that does not authenticate is 401, an authorization refusal is
// 403, everything else is 400. The reason is logged, never returned to the
// client (a refusal must not become an oracle).
func (g *Gateway) refusal(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, portal.ErrUnauthenticated):
		g.logf("gateway: authentication refused", "err", err.Error())
		http.Error(w, "portal: unauthenticated (fail closed)", http.StatusUnauthorized)
	case errors.Is(err, portal.ErrUnauthorized):
		http.Error(w, "portal: unauthorized (fail closed)", http.StatusForbidden)
	default:
		http.Error(w, "portal: "+err.Error(), http.StatusBadRequest)
	}
}

// Compile-time checks: the gateway is a portal component that binds an
// address.
var (
	_ portal.Portal    = (*Gateway)(nil)
	_ portal.Addressed = (*Gateway)(nil)
)
