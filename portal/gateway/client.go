package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
)

// Client is the remote binding of the unified portal stream (ADR 0019): it
// opens one SSE stream (the opening snapshot, observation events, object
// deltas) and issues Surface calls on the same endpoint's POST channel. It
// implements object.Surface and portal.Stream, so a remote portal is handed
// the same contract an in-process one is — the model above them is identical.
//
// Descriptors come from the opening snapshot (and can be refreshed with
// RefreshTypes); deltas are buffered in a bounded object.DeltaWindow, so
// Watch is a local cursor read rather than a poll of the server. A stream
// failure is reported, never hidden: Types and Watch return it, so a caller
// never mistakes a broken stream for "nothing changed" (ADR 0019 decision 8).
type Client struct {
	base     string
	identity string
	http     *http.Client // per-call requests (bounded by Timeout)
	stream   *http.Client // the SSE stream (no global timeout; ctx-bounded)

	mu         sync.Mutex
	types      []object.Type
	typesErr   error
	deltas     *object.DeltaWindow
	feed       *portal.Mailbox
	streamErr  error
	streamOpen bool
	closed     bool
	cancel     context.CancelFunc
	// deltaCursor and obsCursor are the resume cursors for a reconnect.
	deltaCursor int
	obsCursor   int
}

// NewClient builds a remote stream client at base (the gateway's
// http://host:port) presenting identity. It performs no I/O: the stream is
// opened with OpenStream, and Surface calls connect lazily.
func NewClient(base, identity string) *Client {
	return &Client{
		base:     strings.TrimRight(base, "/"),
		identity: identity,
		http:     &http.Client{Timeout: 10 * time.Second},
		stream:   newStreamClient(),
		deltas:   object.NewDeltaWindow(DefaultSubscriptionCap),
		feed:     portal.NewMailbox(DefaultSubscriptionCap),
	}
}

var (
	_ object.Surface = (*Client)(nil)
	_ portal.Stream  = (*Client)(nil)
)

// newStreamClient builds the SSE client: no overall timeout (the stream is
// long-lived, bounded by its context), but a bounded connect so a silent
// server cannot hang the caller. The opening snapshot is separately bounded
// in OpenStream.
func newStreamClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: tr}
}

// StatusError is a non-200 gateway response, so a caller can tell a refusal
// (403) or a missing object (404) from a transport failure.
type StatusError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gateway: %s %s: http %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// ── stream lifecycle ────────────────────────────────────────────────────

// OpenStream opens the SSE stream and reads the opening snapshot, then keeps
// the live frames flowing in the background. It returns once the snapshot is
// complete, so a caller knows the descriptors and the first pages have
// arrived; a connect or protocol failure is returned rather than retried
// silently (the caller decides whether to retry).
func (c *Client) OpenStream(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("gateway: stream is closed")
	}
	if c.cancel != nil {
		c.mu.Unlock()
		return nil // already open
	}
	// The stream lives until Close (or the caller's context), not until
	// OpenStream returns: the request context must outlive this call, or the
	// body read would be cancelled the moment it returned.
	streamCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()

	sc, body, err := c.connect(streamCtx, 0, 0)
	if err != nil {
		cancel()
		c.clearCancel()
		c.setStreamErr(err)
		return err
	}
	// Bound only the opening snapshot: a server that accepts but never sends
	// the done marker must not hang the caller. The stream itself stays open.
	openTimer := time.AfterFunc(10*time.Second, cancel)
	err = c.readFrames(sc, true)
	openTimer.Stop()
	if err != nil {
		body.Close()
		cancel()
		c.clearCancel()
		c.setStreamErr(err)
		return err
	}
	c.mu.Lock()
	c.streamOpen = true
	c.mu.Unlock()
	go c.run(streamCtx, sc, body)
	return nil
}

// run reads the live stream and reconnects on a drop, resuming from the last
// cursors. A reported gap (the server dropped deltas) forces a fresh snapshot.
func (c *Client) run(ctx context.Context, sc *bufio.Scanner, body io.ReadCloser) {
	defer body.Close()
	backoff := 100 * time.Millisecond
	for {
		err := c.readFrames(sc, false)
		if ctx.Err() != nil {
			return
		}
		c.setStreamErr(err)
		if err == errNeedSnapshot {
			backoff = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
		c.mu.Lock()
		from, events := c.deltaCursor, c.obsCursor
		if err == errNeedSnapshot {
			from, events = 0, 0
		}
		c.mu.Unlock()
		nsc, nbody, cerr := c.connect(ctx, from, events)
		if cerr != nil {
			c.setStreamErr(cerr)
			continue
		}
		sc, body = nsc, nbody
		if from == 0 {
			if err := c.readFrames(sc, true); err != nil {
				body.Close()
				c.setStreamErr(err)
				continue
			}
		}
		c.setStreamErr(nil)
		backoff = 100 * time.Millisecond
	}
}

// errNeedSnapshot tells run to reconnect with a fresh opening snapshot.
var errNeedSnapshot = fmt.Errorf("gateway: stream gap — re-snapshot")

// connect opens one SSE connection and returns its scanner and body.
func (c *Client) connect(ctx context.Context, from, events int) (*bufio.Scanner, io.ReadCloser, error) {
	q := url.Values{}
	if c.identity != "" {
		q.Set("identity", c.identity)
	}
	if from > 0 {
		q.Set("from", strconv.Itoa(from))
	}
	if events > 0 {
		q.Set("events", strconv.Itoa(events))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/portal/stream?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: build stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: open stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		return nil, nil, &StatusError{
			Status: resp.StatusCode, Method: http.MethodGet,
			Path: "/portal/stream", Body: strings.TrimSpace(string(blob)),
		}
	}
	if v := resp.Header.Get(streamVersionHeader); v != "" && v != strconv.Itoa(portal.StreamVersion) {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("gateway: stream version %s, want %d", v, portal.StreamVersion)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	return sc, resp.Body, nil
}

// readFrames decodes SSE frames and applies them. With stopAtSnapshot it
// returns once the opening snapshot is complete; otherwise it returns when
// the stream ends or a gap requires a re-snapshot.
func (c *Client) readFrames(sc *bufio.Scanner, stopAtSnapshot bool) error {
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f portal.Frame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err != nil {
			return fmt.Errorf("gateway: decode stream frame: %w", err)
		}
		if err := c.applyFrame(f); err != nil {
			return err
		}
		if stopAtSnapshot && f.Result != nil && f.Result.SnapshotDone {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// applyFrame folds one frame into the client's caches. A Lost result notifies
// the consumer and asks for a fresh snapshot; a frame error is surfaced.
func (c *Client) applyFrame(f portal.Frame) error {
	switch f.Kind {
	case portal.FrameResult:
		if f.Result == nil {
			return nil
		}
		if f.Result.Error != "" {
			return fmt.Errorf("gateway: stream error: %s", f.Result.Error)
		}
		if f.Result.Lost {
			// A bounded window dropped changes. Tell the consumer with the same
			// "stream_lost" vocabulary the polled binding used — silence here
			// would make a gap look like "nothing changed" — and force a fresh
			// snapshot so the stream resynchronizes.
			c.feed.Push(portal.Event{
				Category: portal.CategoryQueue,
				Kind:     "stream_lost",
				Detail:   "portal stream dropped changes (resync)",
				At:       time.Now().UTC(),
			})
			return errNeedSnapshot
		}
		if f.Result.Types != nil {
			c.setTypes(f.Result.Types)
		}
	case portal.FrameDelta:
		if f.Delta != nil {
			c.deltas.Push(*f.Delta)
			c.mu.Lock()
			if f.Cursor > c.deltaCursor {
				c.deltaCursor = f.Cursor
			}
			c.mu.Unlock()
		}
	case portal.FrameEvent:
		if f.Event != nil {
			c.feed.Push(*f.Event)
			c.mu.Lock()
			if f.Event.Seq >= c.obsCursor {
				c.obsCursor = f.Event.Seq + 1
			}
			c.mu.Unlock()
		}
	}
	return nil
}

// Close ends the stream and releases the feed. Safe more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.feed.Close()
	return nil
}

// Feed implements portal.Stream: the observation events the stream delivers.
func (c *Client) Feed() portal.Subscription { return c.feed }

// Objects implements portal.Stream: the client itself is the Surface.
func (c *Client) Objects() object.Surface { return c }

// Enqueue implements portal.Stream: creation is a call on the stream.
func (c *Client) Enqueue(prompt string, prio int, deferUntil time.Time) error {
	call := portal.Call{Method: portal.MethodEnqueue, Prompt: prompt, Priority: prio}
	if !deferUntil.IsZero() {
		u := deferUntil.UTC()
		call.DeferUntil = &u
	}
	_, err := c.call(call)
	return err
}

// ── object.Surface ──────────────────────────────────────────────────────

// RefreshTypes fetches the descriptors over the stream's call channel and
// caches them. The opening snapshot also fills the cache.
func (c *Client) RefreshTypes() error {
	res, err := c.call(portal.Call{Method: portal.MethodTypes})
	if err != nil {
		c.setTypesErr(err)
		return err
	}
	c.setTypes(res.Types)
	return nil
}

// Types returns the cached descriptors. A stream that failed before any
// descriptor arrived reports the failure rather than an empty type set.
func (c *Client) Types() ([]object.Type, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]object.Type(nil), c.types...)
	if c.typesErr != nil {
		return out, c.typesErr
	}
	if c.types == nil {
		return nil, fmt.Errorf("gateway: descriptors not loaded (stream not open)")
	}
	return out, nil
}

// List returns a bounded page of one type's objects.
func (c *Client) List(typeName string, q object.Query) ([]object.Object, string, error) {
	res, err := c.call(portal.Call{Method: portal.MethodList, Type: typeName, Query: q})
	if err != nil {
		return nil, "", err
	}
	return res.Objects, res.Next, nil
}

// Get returns one object of one type.
func (c *Client) Get(typeName, id string) (object.Object, error) {
	res, err := c.call(portal.Call{Method: portal.MethodGet, Type: typeName, ID: id})
	if err != nil {
		return object.Object{}, err
	}
	if res.Object == nil {
		return object.Object{}, fmt.Errorf("gateway: get returned no object")
	}
	return *res.Object, nil
}

// Detail returns a bounded page of a named detail collection.
func (c *Client) Detail(typeName, id, detail string, q object.Query) ([]object.Object, string, error) {
	res, err := c.call(portal.Call{Method: portal.MethodDetail, Type: typeName, ID: id, Detail: detail, Query: q})
	if err != nil {
		return nil, "", err
	}
	return res.Objects, res.Next, nil
}

// Act performs a declared action on one object.
func (c *Client) Act(typeName, id, action string, args map[string]any) error {
	_, err := c.call(portal.Call{Method: portal.MethodAct, Type: typeName, ID: id, Action: action, Args: args})
	return err
}

// Watch returns the deltas after a cursor from the local bounded window fed
// by the stream. It fails when the stream has failed or was never opened, so
// a caller is never told "no changes" by a broken stream.
func (c *Client) Watch(after int) ([]object.Delta, int, bool, error) {
	deltas, next, lost := c.deltas.ReadAfter(after)
	c.mu.Lock()
	err := c.streamErr
	closed := c.closed
	open := c.streamOpen
	c.mu.Unlock()
	if err != nil {
		return deltas, next, lost, err
	}
	if closed {
		return deltas, next, lost, fmt.Errorf("gateway: stream is closed")
	}
	if !open {
		return deltas, next, lost, fmt.Errorf("gateway: stream is not open")
	}
	return deltas, next, lost, nil
}

// ── call channel ────────────────────────────────────────────────────────

// call posts one call frame and returns the result frame.
func (c *Client) call(call portal.Call) (portal.Result, error) {
	blob, err := json.Marshal(portal.Frame{Kind: portal.FrameCall, Call: &call})
	if err != nil {
		return portal.Result{}, fmt.Errorf("gateway: encode call: %w", err)
	}
	q := url.Values{}
	if c.identity != "" {
		q.Set("identity", c.identity)
	}
	path := "/portal/stream"
	target := c.base + path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(blob))
	if err != nil {
		return portal.Result{}, fmt.Errorf("gateway: build call: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return portal.Result{}, fmt.Errorf("gateway: %s: %w", call.Method, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<24))
	if resp.StatusCode != http.StatusOK {
		return portal.Result{}, &StatusError{
			Status: resp.StatusCode, Method: http.MethodPost, Path: path,
			Body: strings.TrimSpace(string(body)),
		}
	}
	var f portal.Frame
	if err := json.Unmarshal(body, &f); err != nil {
		return portal.Result{}, fmt.Errorf("gateway: %s: decode: %w", call.Method, err)
	}
	if f.Result == nil {
		return portal.Result{}, fmt.Errorf("gateway: %s: response is not a result frame", call.Method)
	}
	if f.Result.Error != "" {
		return *f.Result, fmt.Errorf("gateway: %s: %s", call.Method, f.Result.Error)
	}
	return *f.Result, nil
}

// ── cache helpers ───────────────────────────────────────────────────────

func (c *Client) setTypes(types []object.Type) {
	c.mu.Lock()
	c.types = append([]object.Type(nil), types...)
	c.typesErr = nil
	c.mu.Unlock()
}

func (c *Client) setTypesErr(err error) {
	c.mu.Lock()
	c.typesErr = err
	c.mu.Unlock()
}

func (c *Client) setStreamErr(err error) {
	c.mu.Lock()
	c.streamErr = err
	c.mu.Unlock()
}

// clearCancel drops the stored cancel func after a failed open, so a later
// OpenStream does not mistake a dead stream for an open one. OpenStream is
// the only writer, so an unconditional clear is safe.
func (c *Client) clearCancel() {
	c.mu.Lock()
	c.cancel = nil
	c.mu.Unlock()
}
