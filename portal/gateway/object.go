package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
)

// This file binds the navi's typed-object surface to HTTP (ADR 0017
// decision 10): the type root is the namespace — `GET /{type}`,
// `GET /{type}/{id}`, `GET /{type}/{id}/{detail}`,
// `POST /{type}/{id}/{action}` — and the surface's descriptor route is
// namespaced away from it (`GET /_types`) so a type may be named anything
// except the reserved `portal` and `_`-prefixed roots.
//
// These routes are a convenience API for non-portal clients (curl, a
// script); the portal path is the unified stream (ADR 0019). Every route is
// authorized through the same fail-closed Attach path as the stream, so
// there is one attachment story; the action route is additionally authorized
// by the surface itself (the navi wires its authorizer), so a remote portal
// cannot perform what an in-process one could not. Reads are bounded: a page
// carries a cursor and a limit, and a large transcript is never returned in
// one response.

// ── surface's own routes ────────────────────────────────────────────────

// serveTypes answers GET /_types with every exposed descriptor. Descriptors
// are a frozen declaration, so one response carries them all.
func (g *Gateway) serveTypes(w http.ResponseWriter, r *http.Request) {
	if !methodIs(w, r, http.MethodGet) {
		return
	}
	surface, ok := g.objectsOrRefuse(w)
	if !ok || !g.authorizeAttach(w, r) {
		return
	}
	types, err := surface.Types()
	if err != nil {
		g.objectRefusal(w, err)
		return
	}
	if types == nil {
		types = []object.Type{}
	}
	writeJSON(w, map[string]any{"types": types})
}

// ── type root ───────────────────────────────────────────────────────────

// serveObject routes one type-root path. segs is the trimmed path split on
// "/": one segment lists a type, two read an object, and three either read a
// detail collection (GET) or perform an action (POST) — the two share a path
// shape and are told apart by method.
func (g *Gateway) serveObject(w http.ResponseWriter, r *http.Request, segs []string) {
	switch len(segs) {
	case 1:
		g.serveList(w, r, segs[0])
	case 2:
		g.serveGet(w, r, segs[0], segs[1])
	case 3:
		if r.Method == http.MethodPost {
			g.serveAction(w, r, segs[0], segs[1], segs[2])
			return
		}
		g.serveDetail(w, r, segs[0], segs[1], segs[2])
	default:
		http.NotFound(w, r)
	}
}

// serveList answers GET /{type} with a bounded page of objects.
func (g *Gateway) serveList(w http.ResponseWriter, r *http.Request, typeName string) {
	if !methodIs(w, r, http.MethodGet) {
		return
	}
	surface, ok := g.objectsOrRefuse(w)
	if !ok || !g.authorizeAttach(w, r) {
		return
	}
	objs, next, err := surface.List(typeName, queryFrom(r))
	if err != nil {
		g.objectRefusal(w, err)
		return
	}
	writeObjectPage(w, objs, next)
}

// serveGet answers GET /{type}/{id} with one object.
func (g *Gateway) serveGet(w http.ResponseWriter, r *http.Request, typeName, id string) {
	if !methodIs(w, r, http.MethodGet) {
		return
	}
	surface, ok := g.objectsOrRefuse(w)
	if !ok || !g.authorizeAttach(w, r) {
		return
	}
	obj, err := surface.Get(typeName, id)
	if err != nil {
		g.objectRefusal(w, err)
		return
	}
	writeJSON(w, obj)
}

// serveDetail answers GET /{type}/{id}/{detail} with a bounded page of a
// detail collection (a thought's transcript, for example).
func (g *Gateway) serveDetail(w http.ResponseWriter, r *http.Request, typeName, id, detail string) {
	if !methodIs(w, r, http.MethodGet) {
		return
	}
	surface, ok := g.objectsOrRefuse(w)
	if !ok || !g.authorizeAttach(w, r) {
		return
	}
	objs, next, err := surface.Detail(typeName, id, detail, queryFrom(r))
	if err != nil {
		g.objectRefusal(w, err)
		return
	}
	writeObjectPage(w, objs, next)
}

// serveAction answers POST /{type}/{id}/{action} with the request body as
// the action's arguments (an empty body means no arguments). Authorization
// and argument validation happen inside the surface; the gateway only
// frames them.
func (g *Gateway) serveAction(w http.ResponseWriter, r *http.Request, typeName, id, action string) {
	if !methodIs(w, r, http.MethodPost) {
		return
	}
	surface, ok := g.objectsOrRefuse(w)
	if !ok || !g.authorizeAttach(w, r) {
		return
	}
	args, ok := readArgs(w, r)
	if !ok {
		return
	}
	if err := surface.Act(typeName, id, action, args); err != nil {
		g.objectRefusal(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ── helpers ─────────────────────────────────────────────────────────────

// objectsOrRefuse returns the bound surface, or answers 503 when the
// gateway has none (observation-only deployment).
func (g *Gateway) objectsOrRefuse(w http.ResponseWriter) (object.Surface, bool) {
	if g.objects == nil {
		http.Error(w, "gateway: no object surface bound", http.StatusServiceUnavailable)
		return nil, false
	}
	return g.objects, true
}

// authorizeAttach gates a route on the same fail-closed attachment path the
// observation stream uses.
func (g *Gateway) authorizeAttach(w http.ResponseWriter, r *http.Request) bool {
	if err := g.Attach(attachIdentity(r)); err != nil {
		g.refusal(w, err)
		return false
	}
	return true
}

// methodIs enforces one HTTP method, answering 405 otherwise.
func methodIs(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// queryFrom reads the bounded-read parameters (cursor, limit).
func queryFrom(r *http.Request) object.Query {
	q := object.Query{Cursor: r.URL.Query().Get("cursor")}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Limit = n
		}
	}
	return q
}

// readArgs decodes an action body as the argument object. An empty body is
// no arguments; a body that is not a JSON object is a 400.
func readArgs(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	blob, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "gateway: read body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil, true
	}
	var args map[string]any
	if err := json.Unmarshal(blob, &args); err != nil {
		http.Error(w, "gateway: action body is not an object", http.StatusBadRequest)
		return nil, false
	}
	return args, true
}

func writeJSON(w http.ResponseWriter, v any) {
	blob, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "gateway: encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(blob)
}

func writeObjectPage(w http.ResponseWriter, objs []object.Object, next string) {
	if objs == nil {
		objs = []object.Object{}
	}
	writeJSON(w, map[string]any{"objects": objs, "next": next})
}

// objectRefusal maps a surface error onto an HTTP status. The mapping is
// explicit rather than defaulted: a missing object is 404, a bad request is
// 400, an unserved capability is 501, a refusal is 403, and anything else is
// a server-side failure.
func (g *Gateway) objectRefusal(w http.ResponseWriter, err error) {
	http.Error(w, "object: "+err.Error(), statusForError(err))
}

// statusForError classifies a surface error, so the stream's POST channel and
// the object routes answer with the same status.
func statusForError(err error) int {
	switch {
	case errors.Is(err, portal.ErrUnauthorized):
		return http.StatusForbidden
	case errors.Is(err, object.ErrUnknownType),
		errors.Is(err, object.ErrNotFound),
		errors.Is(err, object.ErrUnknownDetail):
		return http.StatusNotFound
	case errors.Is(err, object.ErrUnknownAction),
		errors.Is(err, object.ErrInvalidArgs):
		return http.StatusBadRequest
	case errors.Is(err, object.ErrUnsupported):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
