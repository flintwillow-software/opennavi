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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
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
	ln      net.Listener
	srv     *http.Server
	log     *slog.Logger
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
// routes then answer 503).
func Handler(control portal.Portal, obs portal.Observer, objects object.Surface) http.Handler {
	g := &Gateway{control: control, objects: objects}
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

	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", g.addr, err)
	}
	g.ln = ln
	g.srv = &http.Server{
		Handler:           g,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serr := g.srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
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

// Attach authorizes an identity for the portal attachment.
func (g *Gateway) Attach(identity string) error {
	return g.delegate(func(p portal.Portal) error { return p.Attach(identity) })
}

// Steer routes a steering message to a thought.
func (g *Gateway) Steer(identity, thoughtID, message string) error {
	return g.delegate(func(p portal.Portal) error { return p.Steer(identity, thoughtID, message) })
}

// Pause parks a running thought.
func (g *Gateway) Pause(identity, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Pause(identity, thoughtID) })
}

// Resume re-queues a paused thought.
func (g *Gateway) Resume(identity, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Resume(identity, thoughtID) })
}

// Cancel removes queued/paused work.
func (g *Gateway) Cancel(identity, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Cancel(identity, thoughtID) })
}

// BumpPriority changes a queued thought's priority.
func (g *Gateway) BumpPriority(identity, thoughtID string, prio int) error {
	return g.delegate(func(p portal.Portal) error { return p.BumpPriority(identity, thoughtID, prio) })
}

// Retry re-queues a failed thought.
func (g *Gateway) Retry(identity, thoughtID string) error {
	return g.delegate(func(p portal.Portal) error { return p.Retry(identity, thoughtID) })
}

// Enqueue schedules a thought.
func (g *Gateway) Enqueue(identity string, t *thought.Thought) error {
	return g.delegate(func(p portal.Portal) error { return p.Enqueue(identity, t) })
}

// ── HTTP surface ────────────────────────────────────────────────────────

// ServeHTTP routes the gateway endpoints. The observation stream and
// control channel keep their `/portal` namespace; the typed-object surface
// owns the type root (`/{type}`, `/{type}/{id}`, `/{type}/{id}/{detail}`,
// `POST /{type}/{id}/{action}`) with its own routes namespaced under `_`
// (`/_types`, `/_deltas`), so `portal` and `_`-prefixed roots are reserved
// type names.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	switch path {
	case "portal/events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g.serveEvents(w, r)
	case "portal/control":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g.serveControl(w, r)
	case "_types":
		g.serveTypes(w, r)
	case "_deltas":
		g.serveDeltas(w, r)
	case "":
		http.NotFound(w, r)
	default:
		g.serveObject(w, r, strings.Split(path, "/"))
	}
}

// attachIdentity extracts the client identity: the `identity` query
// parameter, falling back to the X-Portal-Identity header.
func attachIdentity(r *http.Request) string {
	if v := r.URL.Query().Get("identity"); v != "" {
		return v
	}
	return r.Header.Get("X-Portal-Identity")
}

// serveEvents answers GET /portal/events?from=N&identity=ID with the
// events after cursor N. Streaming requires an authorized attachment: the
// identity is authorized through the control portal's Attach; with no
// authorizer every attachment is refused 403 fail-closed.
func (g *Gateway) serveEvents(w http.ResponseWriter, r *http.Request) {
	if g.sub == nil {
		http.Error(w, "gateway: not started", http.StatusServiceUnavailable)
		return
	}
	if aerr := g.Attach(attachIdentity(r)); aerr != nil {
		g.refusal(w, aerr)
		return
	}

	cursor := 0
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			cursor = int(n)
		}
	}
	events, next, lost := g.sub.ReadAfter(cursor)

	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		out = append(out, map[string]any{
			"seq":       ev.Seq,
			"category":  ev.Category,
			"kind":      ev.Kind,
			"thoughtID": ev.ThoughtID,
			"detail":    ev.Detail,
			"delivery":  ev.Delivery,
			"at":        ev.At.UTC().Format(time.RFC3339Nano),
		})
	}
	blob, err := json.Marshal(map[string]any{"lost": lost, "next": next, "events": out})
	if err != nil {
		http.Error(w, "gateway: encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(blob)
}

// controlRequest is the POST /portal/control body.
type controlRequest struct {
	Identity  string  `json:"identity"`
	Action    string  `json:"action"`
	ThoughtID string  `json:"thoughtID"`
	Text      string  `json:"text"`
	ID        string  `json:"id"`
	Prompt    string  `json:"prompt"`
	Priority  float64 `json:"priority"`
}

// serveControl answers POST /portal/control. Every action is
// authorizer-gated by the control portal; with a nil authorizer all
// actions are refused 403 (fail closed) until IAM lands.
func (g *Gateway) serveControl(w http.ResponseWriter, r *http.Request) {
	if g.control == nil {
		http.Error(w, "gateway: no control portal bound", http.StatusServiceUnavailable)
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
	identity := req.Identity
	if identity == "" {
		identity = attachIdentity(r)
	}

	var ctrlErr error
	switch strings.ToLower(req.Action) {
	case "attach":
		ctrlErr = g.control.Attach(identity)
	case "steer":
		ctrlErr = g.control.Steer(identity, req.ThoughtID, req.Text)
	case "pause":
		ctrlErr = g.control.Pause(identity, req.ThoughtID)
	case "resume":
		ctrlErr = g.control.Resume(identity, req.ThoughtID)
	case "cancel":
		ctrlErr = g.control.Cancel(identity, req.ThoughtID)
	case "bump":
		prio := 4
		if req.Priority >= 1 && req.Priority <= 10 {
			prio = int(req.Priority)
		}
		ctrlErr = g.control.BumpPriority(identity, req.ThoughtID, prio)
	case "retry":
		ctrlErr = g.control.Retry(identity, req.ThoughtID)
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
		ctrlErr = g.control.Enqueue(identity, t)
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

// refusal maps a control error onto an HTTP status: authorization
// refusals are 403, everything else is 400.
func (g *Gateway) refusal(w http.ResponseWriter, err error) {
	if errors.Is(err, portal.ErrUnauthorized) {
		http.Error(w, "portal: unauthorized (fail closed)", http.StatusForbidden)
		return
	}
	http.Error(w, "portal: "+err.Error(), http.StatusBadRequest)
}

// Compile-time checks: the gateway is a portal component that binds an
// address.
var (
	_ portal.Portal    = (*Gateway)(nil)
	_ portal.Addressed = (*Gateway)(nil)
)
