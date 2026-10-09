package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// newStreamServer serves the unified portal stream over a fake surface, with
// the queue and surface handed back so a test can push a change and watch it
// arrive.
func newStreamServer(t *testing.T, auth pubportal.Authorizer) (*httptest.Server, *thought.Queue, *fakeSurface) {
	t.Helper()
	fake := newFakeSurface()
	q := newQueue(t)
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })
	hub := pubportal.NewControl(auth)
	if err := hub.Start(context.Background(), pubportal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(hub, broker, fake))
	t.Cleanup(srv.Close)
	return srv, q, fake
}

// openSSE opens the stream and returns the response plus a channel of decoded
// frames (closed when the stream ends).
func openSSE(t *testing.T, url string) (*http.Response, <-chan pubportal.Frame) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan pubportal.Frame, 256)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var f pubportal.Frame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f) != nil {
				return
			}
			frames <- f
		}
	}()
	return resp, frames
}

// waitFrame returns the first frame matching pred, or fails after a deadline.
func waitFrame(t *testing.T, frames <-chan pubportal.Frame, pred func(pubportal.Frame) bool) pubportal.Frame {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before the expected frame arrived")
			}
			if pred(f) {
				return f
			}
		case <-deadline:
			t.Fatal("timed out waiting for a stream frame")
		}
	}
}

// TestGatewayStreamServesSnapshotEventsAndDeltas is the one-stream gate
// (ADR 0019 decisions 2-4): a single GET carries the bounded opening
// snapshot, live observation events, and live object deltas.
func TestGatewayStreamServesSnapshotEventsAndDeltas(t *testing.T) {
	srv, q, fake := newStreamServer(t, allowAll{})
	resp, frames := openSSE(t, srv.URL+"/portal/stream?identity=u")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get(streamVersionHeader), strconv.Itoa(pubportal.StreamVersion); got != want {
		t.Fatalf("stream version header = %q, want %q", got, want)
	}

	// The opening snapshot: descriptors, a page per declared type, then the
	// done marker that tells the client the live stream has begun.
	typesFrame := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Result != nil && len(f.Result.Types) > 0
	})
	if len(typesFrame.Result.Types) != 2 || !typesFrame.Result.Snapshot {
		t.Fatalf("snapshot types frame = %+v, want 2 descriptors marked as snapshot", typesFrame.Result)
	}
	waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Result != nil && f.Result.Type == "thought" && len(f.Result.Objects) == 3
	})
	waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Result != nil && f.Result.SnapshotDone
	})

	// A delta pushed after the snapshot is delivered live, carrying the
	// server cursor a reconnect resumes from.
	fake.pushDelta(object.Delta{
		Type: "thought", ID: "t1", Op: object.OpUpdate,
		Fields: map[string]any{"status": "complete"},
	})
	deltaFrame := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Kind == pubportal.FrameDelta
	})
	if deltaFrame.Delta == nil || deltaFrame.Delta.ID != "t1" || deltaFrame.Cursor == 0 {
		t.Fatalf("delta frame = %+v, want the pushed delta with a cursor", deltaFrame)
	}

	// An observation event published after the snapshot is delivered live on
	// the same stream.
	if err := q.Enqueue(&thought.Thought{
		ID: "s1", Prompt: "hi", Priority: 4, Status: thought.ThoughtPending,
	}); err != nil {
		t.Fatal(err)
	}
	evFrame := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Kind == pubportal.FrameEvent
	})
	if evFrame.Event == nil || evFrame.Event.Kind != "enqueued" || evFrame.Event.ThoughtID != "s1" {
		t.Fatalf("event frame = %+v, want the enqueued event", evFrame.Event)
	}
}

// TestGatewayStreamResumesFromObservationCursor pins reconnection (ADR 0019
// decision 6): resuming past a cursor delivers only what is new.
func TestGatewayStreamResumesFromObservationCursor(t *testing.T) {
	srv, q, _ := newStreamServer(t, allowAll{})
	if err := q.Enqueue(&thought.Thought{
		ID: "r1", Prompt: "old", Priority: 4, Status: thought.ThoughtPending,
	}); err != nil {
		t.Fatal(err)
	}
	resp, frames := openSSE(t, srv.URL+"/portal/stream?identity=u")
	first := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Kind == pubportal.FrameEvent && f.Event != nil && f.Event.ThoughtID == "r1"
	})
	resp.Body.Close()

	resp2, frames2 := openSSE(t, fmt.Sprintf("%s/portal/stream?identity=u&events=%d", srv.URL, first.Event.Seq+1))
	defer resp2.Body.Close()
	if err := q.Enqueue(&thought.Thought{
		ID: "r2", Prompt: "new", Priority: 4, Status: thought.ThoughtPending,
	}); err != nil {
		t.Fatal(err)
	}
	next := waitFrame(t, frames2, func(f pubportal.Frame) bool {
		return f.Kind == pubportal.FrameEvent
	})
	if next.Event == nil || next.Event.ThoughtID != "r2" {
		t.Fatalf("resumed stream delivered %+v, want only the new event", next.Event)
	}
}

// TestGatewayStreamReportsDeltaGap pins the slow-consumer path: when the
// bounded delta window drops changes, the stream says so (a Lost frame)
// rather than silently skipping them, so the client re-snapshots instead of
// rendering a stale view (ADR 0019 decisions 6 and 8).
func TestGatewayStreamReportsDeltaGap(t *testing.T) {
	srv, _, fake := newStreamServer(t, allowAll{})
	// Overrun the bounded window before the client attaches, so the client's
	// cursor (1) is behind the window's oldest entry.
	for i := 0; i < 600; i++ {
		fake.pushDelta(object.Delta{Type: "thought", ID: "t1", Op: object.OpUpdate})
	}
	resp, frames := openSSE(t, srv.URL+"/portal/stream?identity=u&from=1")
	defer resp.Body.Close()
	lost := waitFrame(t, frames, func(f pubportal.Frame) bool {
		return f.Result != nil && f.Result.Lost
	})
	if !lost.Result.Lost {
		t.Fatalf("frame = %+v, want a Lost result", lost.Result)
	}
}

// TestGatewayStreamResumeSkipsTheSnapshot pins the resume path: a connection
// with a cursor picks up live frames and does not re-send the opening
// snapshot (ADR 0019 decision 6).
func TestGatewayStreamResumeSkipsTheSnapshot(t *testing.T) {
	srv, _, _ := newStreamServer(t, allowAll{})
	resp, frames := openSSE(t, srv.URL+"/portal/stream?identity=u&from=1&events=1")
	defer resp.Body.Close()

	select {
	case f, ok := <-frames:
		if ok && f.Result != nil && f.Result.Snapshot {
			t.Fatalf("a resumed stream re-sent the snapshot: %+v", f.Result)
		}
	case <-time.After(250 * time.Millisecond):
		// Nothing new to send: a resumed, idle stream is silent. Correct.
	}
}

// TestGatewayStreamCallRoundTrip pins the upstream half: a POSTed call frame
// gets its result frame on the same endpoint.
func TestGatewayStreamCallRoundTrip(t *testing.T) {
	srv, _, _ := newStreamServer(t, allowAll{})
	body := `{"kind":"call","call":{"method":"types"}}`
	resp, err := http.Post(srv.URL+"/portal/stream?identity=u", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("call status = %d, want 200", resp.StatusCode)
	}
	var f pubportal.Frame
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		t.Fatal(err)
	}
	if f.Kind != pubportal.FrameResult || f.Result == nil || len(f.Result.Types) != 2 {
		t.Fatalf("call response = %+v, want a result frame with the descriptors", f)
	}

	// A refused call keeps its typed status: no authorizer, fail closed.
	closed, _, _ := newStreamServer(t, nil)
	resp2, err := http.Post(closed.URL+"/portal/stream?identity=u", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("call without authorizer = %d, want 403", resp2.StatusCode)
	}
}

// TestGatewayStreamFailsClosedWithoutAuthorizer pins the security property:
// the stream refuses (403) when no authorizer is wired, exactly like the
// control channel and the object routes.
func TestGatewayStreamFailsClosedWithoutAuthorizer(t *testing.T) {
	srv, _, _ := newStreamServer(t, nil)
	resp, err := http.Get(srv.URL + "/portal/stream?identity=u")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stream without authorizer = %d, want 403", resp.StatusCode)
	}
}
