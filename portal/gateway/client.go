package gateway

import (
	"bytes"
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
)

// Client is the remote binding of object.Surface over the gateway routes: the
// same contract an in-process portal is handed, framed over HTTP. A remote
// portal (the TUI's gateway attachment, a WebUI) uses it instead of a
// hand-rolled fetch per concern, so the in-process and remote paths share one
// contract (ADR 0017 decision 10).
//
// Descriptors are a frozen declaration, so Types returns the snapshot cached
// by RefreshTypes rather than fetching per call. List/Get/Detail/Act report
// transport failures as errors. Watch has no error in the Surface contract
// (deltas are a bounded, cursor-ed read), so a transport failure there is
// reported as a gap — the caller's view may be stale and the next poll
// resumes from the same cursor.
type Client struct {
	base     string
	identity string
	http     *http.Client

	mu    sync.Mutex
	types []object.Type
}

// NewClient builds a remote surface at base (the gateway's http://host:port)
// presenting identity. It performs no I/O: the connection is established by
// the first call, and descriptors are fetched with RefreshTypes.
func NewClient(base, identity string) *Client {
	return &Client{
		base:     strings.TrimRight(base, "/"),
		identity: identity,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

var _ object.Surface = (*Client)(nil)

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

// RefreshTypes fetches /_types and caches the descriptors.
func (c *Client) RefreshTypes() error {
	var doc struct {
		Types []object.Type `json:"types"`
	}
	if err := c.do(http.MethodGet, "/_types", nil, nil, &doc); err != nil {
		return err
	}
	c.mu.Lock()
	c.types = doc.Types
	c.mu.Unlock()
	return nil
}

// Types returns the descriptors cached by RefreshTypes (nil before the first
// successful refresh).
func (c *Client) Types() []object.Type {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]object.Type(nil), c.types...)
}

// List returns a bounded page of one type's objects.
func (c *Client) List(typeName string, q object.Query) ([]object.Object, string, error) {
	var doc objectPage
	if err := c.do(http.MethodGet, objectPath(typeName), pageValues(q), nil, &doc); err != nil {
		return nil, "", err
	}
	return doc.Objects, doc.Next, nil
}

// Get returns one object of one type.
func (c *Client) Get(typeName, id string) (object.Object, error) {
	var obj object.Object
	if err := c.do(http.MethodGet, objectPath(typeName, id), nil, nil, &obj); err != nil {
		return object.Object{}, err
	}
	return obj, nil
}

// Detail returns a bounded page of a named detail collection.
func (c *Client) Detail(typeName, id, detail string, q object.Query) ([]object.Object, string, error) {
	var doc objectPage
	if err := c.do(http.MethodGet, objectPath(typeName, id, detail), pageValues(q), nil, &doc); err != nil {
		return nil, "", err
	}
	return doc.Objects, doc.Next, nil
}

// Act performs a declared action, sending args as the request body.
func (c *Client) Act(typeName, id, action string, args map[string]any) error {
	var body io.Reader
	if len(args) > 0 {
		blob, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("gateway: encode action args: %w", err)
		}
		body = bytes.NewReader(blob)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	return c.do(http.MethodPost, objectPath(typeName, id, action), nil, body, &out)
}

// Watch returns the object deltas after a cursor. A transport failure reports
// a gap (lost=true) with the cursor unchanged: the Surface contract has no
// error here, and a stale view the caller is told about is better than a
// silent "nothing changed".
func (c *Client) Watch(after int) ([]object.Delta, int, bool) {
	q := url.Values{}
	if after > 0 {
		q.Set("from", strconv.Itoa(after))
	}
	var doc struct {
		Deltas []object.Delta `json:"deltas"`
		Next   int            `json:"next"`
		Lost   bool           `json:"lost"`
	}
	if err := c.do(http.MethodGet, "/_deltas", q, nil, &doc); err != nil {
		return nil, after, true
	}
	return doc.Deltas, doc.Next, doc.Lost
}

// objectPage is the shared list/detail response shape.
type objectPage struct {
	Objects []object.Object `json:"objects"`
	Next    string          `json:"next"`
}

// do performs one request, always presenting the client's identity, and
// decodes a 200 response into out (nil out discards the body).
func (c *Client) do(method, path string, q url.Values, body io.Reader, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if c.identity != "" {
		q.Set("identity", c.identity)
	}
	target := c.base + path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return fmt.Errorf("gateway: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gateway: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<24))
	if resp.StatusCode != http.StatusOK {
		return &StatusError{
			Status: resp.StatusCode,
			Method: method,
			Path:   path,
			Body:   strings.TrimSpace(string(blob)),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(blob, out); err != nil {
		return fmt.Errorf("gateway: %s %s: decode: %w", method, path, err)
	}
	return nil
}

// objectPath builds a type-root path with each segment escaped, so an id or
// action containing a slash or space cannot forge another route.
func objectPath(segs ...string) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString("/")
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

// pageValues encodes a bounded-read query.
func pageValues(q object.Query) url.Values {
	v := url.Values{}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return v
}
