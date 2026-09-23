// Portal serve shim (S000407): the daemon serves the portal stream +
// control channel so a remote gateway attachment can observe the
// navi's thought process and — once IAM lands — control it. The
// contract (internal/interfaces.Portal) is transport-agnostic; this
// file provides the thin loopback-grade JSON mapping the daemon's
// gateway serves. The real wire protocol is deferred to 05-messaging
// (spec says so); until then this shim mirrors the --metrics-addr
// serve pattern (#280): same listener lifecycle, same graceful
// shutdown, errors logged never fatal.
//
// Authorization: OBSERVATION is an attachment (S000407) — the stream
// carries prompt content and tool-input snippets, so attaching to it
// is governed by the same portal authorization as control. Every
// request (stream or control) authorizes through Portal.Attach; with
// no authorizer wired (IAM pending) everything fails closed with 403
// (S040200) — the same fail-closed stub as the http tool's nil
// AuthResolver. In-process consumers (future TUI) are unaffected:
// they use Portal.Subscribe directly, not the shim.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/internal/interfaces"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// DefaultPortalMailboxCap bounds the served portal stream: a client
// that reads slower than the runtime emits events loses the oldest
// (bounded memory; the ReadAfter cursor reports the gap).
const DefaultPortalMailboxCap = 4096

// portalHandler returns the gateway handler: GET /portal/events
// drains the stream after a cursor; POST /portal/control applies a
// control action (authorizer-gated, fail closed).
func portalHandler(d *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/portal/events":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			serveEvents(w, r, d)
		case "/portal/control":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			serveControl(w, r, d)
		default:
			http.NotFound(w, r)
		}
	})
}

// formatEventAt renders a portal event timestamp for JSON.
func formatEventAt(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// attachIdentity extracts the client identity for a request: the
// `identity` query parameter, falling back to the X-Portal-Identity
// header. Empty means anonymous — the authorizer decides.
func attachIdentity(r *http.Request) string {
	if v := r.URL.Query().Get("identity"); v != "" {
		return v
	}
	return r.Header.Get("X-Portal-Identity")
}

// serveEvents answers GET /portal/events?from=N&identity=ID with the
// events after cursor N. Streaming requires a prior authorization
// (S000407 attach): the identity is authorized through Portal.Attach;
// without an authorizer every attachment is refused 403 fail-closed.
func serveEvents(w http.ResponseWriter, r *http.Request, d *Runtime) {
	if d.Portal == nil || d.PortalBuf == nil {
		http.Error(w, "portal stream unavailable", http.StatusServiceUnavailable)
		return
	}
	if aerr := d.Portal.Attach(attachIdentity(r)); aerr != nil {
		if errors.Is(aerr, interfaces.ErrPortalUnauthorized) {
			http.Error(w, "portal: unauthorized (fail closed)", http.StatusForbidden)
			return
		}
		http.Error(w, "portal: attachment refused: "+aerr.Error(), http.StatusForbidden)
		return
	}

	cursor := 0
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			cursor = int(n)
		}
	}
	events, next, lost := d.PortalBuf.ReadAfter(cursor)

	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		out = append(out, map[string]any{
			"seq":       ev.Seq,
			"category":  ev.Category,
			"kind":      ev.Kind,
			"thoughtID": ev.ThoughtID,
			"detail":    ev.Detail,
			"delivery":  ev.Delivery,
			"at":        formatEventAt(ev.At),
		})
	}
	blob, err := json.Marshal(map[string]any{
		"lost":   lost,
		"next":   next,
		"events": out,
	})
	if err != nil {
		http.Error(w, "portal: encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(string(blob)))
}

// serveControl answers POST /portal/control with a JSON body
// {"identity","action","thoughtID","text","id","prompt","priority"}.
// Every action is authorizer-gated; with the daemon's nil authorizer
// all actions are refused 403 (fail closed) until IAM lands.
func serveControl(w http.ResponseWriter, r *http.Request, d *Runtime) {
	if d.Portal == nil {
		http.Error(w, "portal control unavailable", http.StatusServiceUnavailable)
		return
	}
	// Streaming and control both require the attachment authorization;
	// the identity is taken from the request before any action runs.
	identity := attachIdentity(r)
	blob, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, "portal: read body: "+rerr.Error(), http.StatusBadRequest)
		return
	}
	var req map[string]any
	if err := json.Unmarshal(blob, &req); err != nil {
		http.Error(w, "portal: control body is not an object", http.StatusBadRequest)
		return
	}
	str := func(k string) string {
		if v, ok := req[k].(string); ok {
			return v
		}
		return ""
	}
	action := strings.ToLower(str("action"))
	thoughtID := str("thoughtID")

	var ctrlErr error
	switch action {
	case "attach":
		ctrlErr = d.Portal.Attach(identity)
	case "steer":
		ctrlErr = d.Portal.Steer(identity, thoughtID, str("text"))
	case "pause":
		ctrlErr = d.Portal.Pause(identity, thoughtID)
	case "resume":
		ctrlErr = d.Portal.Resume(identity, thoughtID)
	case "cancel":
		ctrlErr = d.Portal.Cancel(identity, thoughtID)
	case "enqueue":
		now := time.Now().UTC()
		t := &thought.Thought{
			ID:        str("id"),
			Prompt:    str("prompt"),
			Priority:  4,
			Status:    thought.ThoughtPending,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if p, ok := req["priority"].(float64); ok && p > 0 {
			t.Priority = int(p)
		}
		ctrlErr = d.Portal.Enqueue(identity, t)
	default:
		http.Error(w, fmt.Sprintf("portal: unknown action %q", action), http.StatusBadRequest)
		return
	}

	if ctrlErr != nil {
		if errors.Is(ctrlErr, interfaces.ErrPortalUnauthorized) {
			http.Error(w, "portal: unauthorized (fail closed)", http.StatusForbidden)
			return
		}
		http.Error(w, "portal: "+ctrlErr.Error(), http.StatusBadRequest)
		return
	}
	blob, merr := json.Marshal(map[string]any{"ok": true})
	if merr != nil {
		http.Error(w, "portal: encode: "+merr.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(string(blob)))
}
