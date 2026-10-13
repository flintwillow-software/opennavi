package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the gateway's binding of the unified portal stream (ADR 0019
// decision 4): GET /portal/stream is the SSE downstream (the bounded opening
// snapshot, observation events, object deltas), and POST /portal/stream is
// the upstream Surface call channel. The object routes and /portal/control
// remain for non-portal clients (curl, navi enqueue); the portal path is this
// stream.
//
// The stream pushes: the loop is woken by the surface's delta window and the
// observation mailbox, never by a timer, so an idle host still wakes a
// connected portal the moment a thought is enqueued externally (ADR 0019
// decision 5).

// streamVersionHeader carries the frame dialect out of band, so a client can
// refuse a stream it does not understand (ADR 0019 decision 9).
const streamVersionHeader = "X-Navi-Stream-Version"

// serveStream answers GET /portal/stream with an SSE stream. It authorizes
// the attachment first (fail closed), then writes the opening snapshot unless
// the client is resuming from a cursor, then pushes events and deltas.
func (g *Gateway) serveStream(w http.ResponseWriter, r *http.Request) {
	if !methodIs(w, r, http.MethodGet) {
		return
	}
	if g.objects == nil && g.sub == nil {
		http.Error(w, "gateway: not started", http.StatusServiceUnavailable)
		return
	}
	actor, aerr := g.authenticate(r)
	if aerr != nil {
		g.refusal(w, aerr)
		return
	}
	if err := g.Attach(actor); err != nil {
		g.refusal(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "gateway: streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set(streamVersionHeader, strconv.Itoa(portal.StreamVersion))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	from := queryInt(r, "from")
	obsCursor := queryInt(r, "events")
	deltaCursor := from

	// A fresh connection opens with the bounded snapshot; a resumed one
	// (from > 0) picks up where it left off (ADR 0019 decision 6).
	if from == 0 && g.objects != nil {
		if _, next, _, err := g.objects.Watch(0); err == nil {
			deltaCursor = next
		}
		if !g.writeSnapshot(w, flusher) {
			return
		}
	}

	ctx := r.Context()
	for {
		// Capture the wake channels BEFORE reading, so a change that lands
		// between the read and the select closes the captured channel and is
		// never missed.
		surfaceCh := g.surfaceNotify()
		obsCh := g.subNotify()
		if !g.flushStream(w, flusher, &deltaCursor, &obsCursor) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-surfaceCh:
		case <-obsCh:
		}
	}
}

// flushStream writes everything new since the cursors and advances them. It
// returns false when the client is gone (a write failed).
func (g *Gateway) flushStream(w http.ResponseWriter, flusher http.Flusher, deltaCursor, obsCursor *int) bool {
	if g.objects != nil {
		deltas, next, lost, err := g.objects.Watch(*deltaCursor)
		if err != nil {
			return writeSSE(w, flusher, portal.Frame{
				Kind:   portal.FrameResult,
				Result: &portal.Result{Error: err.Error()},
			})
		}
		if lost {
			// The bounded window dropped deltas: tell the client so it
			// re-snapshots instead of silently missing changes.
			if !writeSSE(w, flusher, portal.Frame{
				Kind:   portal.FrameResult,
				Result: &portal.Result{Lost: true},
			}) {
				return false
			}
		}
		for _, d := range deltas {
			if !writeSSE(w, flusher, portal.Frame{Kind: portal.FrameDelta, Delta: &d, Cursor: next}) {
				return false
			}
		}
		*deltaCursor = next
	}
	if g.sub != nil {
		events, next, lost := g.sub.ReadAfter(*obsCursor)
		if lost {
			if !writeSSE(w, flusher, portal.Frame{
				Kind:   portal.FrameResult,
				Result: &portal.Result{Lost: true},
			}) {
				return false
			}
		}
		for i := range events {
			if !writeSSE(w, flusher, portal.Frame{Kind: portal.FrameEvent, Event: &events[i]}) {
				return false
			}
		}
		*obsCursor = next
	}
	flusher.Flush()
	return true
}

// writeSnapshot writes the opening snapshot: the descriptor set, then one
// bounded first page per declared type. The delta cursor was captured before
// the pages were read, so a change during the snapshot is replayed as a delta
// afterwards rather than lost.
func (g *Gateway) writeSnapshot(w http.ResponseWriter, flusher http.Flusher) bool {
	types, err := g.objects.Types()
	if err != nil {
		return writeSSE(w, flusher, portal.Frame{
			Kind:   portal.FrameResult,
			Result: &portal.Result{Error: err.Error()},
		})
	}
	if !writeSSE(w, flusher, portal.Frame{
		Kind:   portal.FrameResult,
		Result: &portal.Result{Types: types, Snapshot: true},
	}) {
		return false
	}
	for _, t := range types {
		objs, next, err := g.objects.List(t.Name, object.Query{})
		if err != nil {
			continue
		}
		if !writeSSE(w, flusher, portal.Frame{
			Kind: portal.FrameResult,
			Result: &portal.Result{
				Type: t.Name, Objects: objs, Next: next, Snapshot: true,
			},
		}) {
			return false
		}
	}
	// The done marker tells the client the live stream has begun.
	if !writeSSE(w, flusher, portal.Frame{
		Kind:   portal.FrameResult,
		Result: &portal.Result{Snapshot: true, SnapshotDone: true},
	}) {
		return false
	}
	flusher.Flush()
	return true
}

// writeSSE writes one frame as an SSE data event.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, f portal.Frame) bool {
	blob, err := json.Marshal(f)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", blob); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// surfaceNotify returns the surface's push signal, or nil when it cannot wake
// a consumer (then only the observation side wakes the loop).
func (g *Gateway) surfaceNotify() <-chan struct{} {
	if n, ok := g.objects.(object.Notifier); ok {
		return n.Notify()
	}
	return nil
}

// subNotify returns the observation subscription's push signal.
func (g *Gateway) subNotify() <-chan struct{} {
	if wk, ok := g.sub.(portal.Waker); ok {
		return wk.Notify()
	}
	return nil
}

// ── upstream: POST /portal/stream ───────────────────────────────────────

// serveStreamCall answers POST /portal/stream with a call frame and returns
// the matching result frame. The HTTP status mirrors the result's error class
// (403 refusal, 404 missing, ...), so a client keeps its typed errors.
func (g *Gateway) serveStreamCall(w http.ResponseWriter, r *http.Request) {
	if !methodIs(w, r, http.MethodPost) {
		return
	}
	if g.objects == nil {
		http.Error(w, "gateway: no object surface bound", http.StatusServiceUnavailable)
		return
	}
	actor, aerr := g.authenticate(r)
	if aerr != nil {
		g.refusal(w, aerr)
		return
	}
	if err := g.Attach(actor); err != nil {
		g.refusal(w, err)
		return
	}
	blob, rerr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if rerr != nil {
		http.Error(w, "gateway: read body: "+rerr.Error(), http.StatusBadRequest)
		return
	}
	var f portal.Frame
	if err := json.Unmarshal(blob, &f); err != nil || f.Kind != portal.FrameCall || f.Call == nil {
		http.Error(w, "gateway: body is not a call frame", http.StatusBadRequest)
		return
	}
	res, status := g.dispatchCall(actor, f.Call)
	writeResultFrame(w, res, status)
}

// dispatchCall performs one Surface call (or creation) and maps the outcome
// onto a result plus the HTTP status that classifies a failure.
func (g *Gateway) dispatchCall(actor portal.Actor, c *portal.Call) (portal.Result, int) {
	switch c.Method {
	case portal.MethodTypes:
		types, err := g.objects.Types()
		if err != nil {
			return errResult(err)
		}
		return portal.Result{Types: types}, http.StatusOK

	case portal.MethodList:
		objs, next, err := g.objects.List(c.Type, c.Query)
		if err != nil {
			return errResult(err)
		}
		return portal.Result{Objects: objs, Next: next}, http.StatusOK

	case portal.MethodGet:
		obj, err := g.objects.Get(c.Type, c.ID)
		if err != nil {
			return errResult(err)
		}
		return portal.Result{Object: &obj}, http.StatusOK

	case portal.MethodDetail:
		objs, next, err := g.objects.Detail(c.Type, c.ID, c.Detail, c.Query)
		if err != nil {
			return errResult(err)
		}
		return portal.Result{Objects: objs, Next: next}, http.StatusOK

	case portal.MethodAct:
		if err := g.objects.Act(c.Type, c.ID, c.Action, c.Args); err != nil {
			return errResult(err)
		}
		return portal.Result{OK: true}, http.StatusOK

	case portal.MethodWatch:
		deltas, next, lost, err := g.objects.Watch(c.After)
		if err != nil {
			return errResult(err)
		}
		return portal.Result{Deltas: deltas, Cursor: next, Lost: lost}, http.StatusOK

	case portal.MethodEnqueue:
		if g.control == nil {
			return portal.Result{Error: "gateway: no control portal bound"}, http.StatusServiceUnavailable
		}
		now := time.Now().UTC()
		t := &thought.Thought{
			ID:        fmt.Sprintf("portal-%d", now.UnixNano()),
			Prompt:    c.Prompt,
			Priority:  4,
			Status:    thought.ThoughtPending,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if c.Priority >= 1 && c.Priority <= 10 {
			t.Priority = c.Priority
		}
		if c.DeferUntil != nil {
			t.DeferUntil = c.DeferUntil.UTC()
		}
		if err := g.control.Enqueue(actor, t); err != nil {
			return errResult(err)
		}
		return portal.Result{OK: true}, http.StatusOK

	default:
		return portal.Result{Error: "gateway: unknown method " + c.Method}, http.StatusBadRequest
	}
}

// errResult frames a failure with its HTTP class.
func errResult(err error) (portal.Result, int) {
	return portal.Result{Error: err.Error()}, statusForError(err)
}

// writeResultFrame writes a result frame with the given status.
func writeResultFrame(w http.ResponseWriter, res portal.Result, status int) {
	blob, err := json.Marshal(portal.Frame{Kind: portal.FrameResult, Result: &res})
	if err != nil {
		http.Error(w, "gateway: encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(blob)
}

// queryInt reads a non-negative integer query parameter (0 when absent or
// malformed).
func queryInt(r *http.Request, name string) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
