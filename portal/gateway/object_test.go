package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/flintwillow-software/opennavi/object"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// fakeSurface is a stand-in object.Surface: three thoughts, a three-entry
// transcript, two deltas, and an injectable action error — enough to exercise
// the routes, bounded pagination, actions, and error mapping without the navi
// composition root (which a portal package must not import).
type fakeSurface struct {
	mu       sync.Mutex
	thoughts []object.Object
	entries  []object.Object
	deltas   []object.Delta
	actErr   error
	acted    []string
}

func newFakeSurface() *fakeSurface {
	f := &fakeSurface{}
	for _, id := range []string{"t1", "t2", "t3"} {
		f.thoughts = append(f.thoughts, object.Object{
			Type: "thought", ID: id, Version: 1,
			Fields: map[string]any{"id": id, "prompt": "prompt " + id, "status": "pending"},
		})
	}
	for i, msg := range []string{"hi", "hello", "tool out"} {
		f.entries = append(f.entries, object.Object{
			Type: "entry", ID: fmt.Sprintf("e%d", i), Version: 1,
			Fields: map[string]any{"id": fmt.Sprintf("e%d", i), "thought_id": "t1", "message": msg, "role": "user"},
		})
	}
	f.deltas = []object.Delta{
		{Type: "thought", ID: "t1", Op: object.OpUpdate, Fields: map[string]any{"status": "running"}},
		{Type: "thought", ID: "t1", Op: object.OpAppend, Detail: "transcript", Fields: map[string]any{"message": "hi"}},
	}
	return f
}

func (f *fakeSurface) Types() []object.Type {
	return []object.Type{
		{
			Name: "thought", Label: "Thought", ID: "id",
			Fields: []object.Field{
				{Name: "id", Kind: object.KindString},
				{Name: "prompt", Kind: object.KindText},
				{Name: "status", Kind: object.KindEnum},
			},
			Details: []object.Detail{{Name: "transcript", Label: "Transcript", Type: "entry", Key: "thought_id"}},
			Actions: []object.Action{
				{Name: "pause", Label: "Pause"},
				{Name: "steer", Label: "Steer", Args: json.RawMessage(
					`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`)},
			},
		},
		{Name: "entry", Label: "Transcript entry", ID: "id"},
	}
}

// page is the bounded-read slice both List and Detail use: an opaque cursor
// (the next index) plus a limit.
func page(all []object.Object, q object.Query) ([]object.Object, string) {
	start := 0
	if n, err := strconv.Atoi(q.Cursor); err == nil && n > 0 {
		start = n
	}
	if start > len(all) {
		start = len(all)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return all[start:end], next
}

func (f *fakeSurface) List(typeName string, q object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	objs, next := page(f.thoughts, q)
	return objs, next, nil
}

func (f *fakeSurface) Get(typeName, id string) (object.Object, error) {
	if typeName != "thought" {
		return object.Object{}, object.ErrUnknownType
	}
	for _, o := range f.thoughts {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (f *fakeSurface) Detail(typeName, id, detail string, q object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	if detail != "transcript" {
		return nil, "", object.ErrUnknownDetail
	}
	if _, err := f.Get(typeName, id); err != nil {
		return nil, "", err
	}
	objs, next := page(f.entries, q)
	return objs, next, nil
}

func (f *fakeSurface) Act(typeName, id, action string, args map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actErr != nil {
		return f.actErr
	}
	if _, err := f.Get(typeName, id); err != nil {
		return err
	}
	if action != "pause" && action != "steer" {
		return object.ErrUnknownAction
	}
	f.acted = append(f.acted, fmt.Sprintf("%s/%s/%v", id, action, args))
	return nil
}

func (f *fakeSurface) Watch(after int) ([]object.Delta, int, bool) {
	start := 0
	if after > 1 {
		start = after - 1
	}
	if start > len(f.deltas) {
		start = len(f.deltas)
	}
	return append([]object.Delta(nil), f.deltas[start:]...), len(f.deltas) + 1, false
}

// newObjectServer serves the gateway over a fake surface with the given
// authorizer.
func newObjectServer(t *testing.T, auth pubportal.Authorizer, surface object.Surface) *httptest.Server {
	t.Helper()
	q := newQueue(t)
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })
	hub := pubportal.NewControl(auth)
	if err := hub.Start(context.Background(), pubportal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(hub, broker, surface))
	t.Cleanup(srv.Close)
	return srv
}

// withIdentity adds the test identity to a URL, preserving any query the
// caller already set.
func withIdentity(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("identity", "u")
	u.RawQuery = q.Encode()
	return u.String()
}

// get performs a GET and returns the status and body.
func get(t *testing.T, raw string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(withIdentity(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// post performs a POST with a JSON body and returns the status and body.
func post(t *testing.T, raw, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(withIdentity(t, raw), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, blob
}

func TestGatewayServesTypesObjectsAndTranscript(t *testing.T) {
	srv := newObjectServer(t, allowAll{}, newFakeSurface())

	status, body := get(t, srv.URL+"/_types")
	if status != http.StatusOK {
		t.Fatalf("/_types = %d: %s", status, body)
	}
	var types struct {
		Types []object.Type `json:"types"`
	}
	if err := json.Unmarshal(body, &types); err != nil {
		t.Fatal(err)
	}
	if len(types.Types) != 2 || types.Types[0].Name != "thought" {
		t.Fatalf("/_types = %+v, want the thought descriptor first", types.Types)
	}
	// The descriptor crosses the wire with stable names and its action schema.
	if got := types.Types[0].Details[0].Name; got != "transcript" {
		t.Fatalf("detail name = %q, want transcript", got)
	}
	if _, ok := types.Types[0].Action("steer"); !ok {
		t.Fatal("steer action missing from the served descriptor")
	}

	status, body = get(t, srv.URL+"/thought")
	if status != http.StatusOK {
		t.Fatalf("/thought = %d: %s", status, body)
	}
	var list struct {
		Objects []object.Object `json:"objects"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Objects) != 3 || list.Objects[0].ID != "t1" {
		t.Fatalf("/thought = %+v, want three objects", list.Objects)
	}

	status, body = get(t, srv.URL+"/thought/t2")
	if status != http.StatusOK {
		t.Fatalf("/thought/t2 = %d: %s", status, body)
	}
	var obj object.Object
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.ID != "t2" || obj.Fields["prompt"] != "prompt t2" {
		t.Fatalf("/thought/t2 = %+v", obj)
	}

	status, body = get(t, srv.URL+"/thought/t1/transcript")
	if status != http.StatusOK {
		t.Fatalf("/thought/t1/transcript = %d: %s", status, body)
	}
	var detail struct {
		Objects []object.Object `json:"objects"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Objects) != 3 || detail.Objects[0].Fields["message"] != "hi" {
		t.Fatalf("transcript = %+v, want three entries", detail.Objects)
	}
}

func TestGatewayObjectRoutesPaginate(t *testing.T) {
	srv := newObjectServer(t, allowAll{}, newFakeSurface())

	status, body := get(t, srv.URL+"/thought?limit=2")
	if status != http.StatusOK {
		t.Fatalf("page 1 = %d: %s", status, body)
	}
	var first struct {
		Objects []object.Object `json:"objects"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 2 || first.Next == "" {
		t.Fatalf("page 1 = %d objects, next %q; want 2 and a cursor", len(first.Objects), first.Next)
	}

	status, body = get(t, srv.URL+"/thought?limit=2&cursor="+first.Next)
	if status != http.StatusOK {
		t.Fatalf("page 2 = %d: %s", status, body)
	}
	var second struct {
		Objects []object.Object `json:"objects"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 1 || second.Next != "" {
		t.Fatalf("page 2 = %d objects, next %q; want the last object and no cursor", len(second.Objects), second.Next)
	}
	if second.Objects[0].ID != "t3" {
		t.Fatalf("page 2 = %+v, want t3", second.Objects)
	}

	// A detail read is bounded the same way.
	status, body = get(t, srv.URL+"/thought/t1/transcript?limit=1")
	if status != http.StatusOK {
		t.Fatalf("transcript page = %d: %s", status, body)
	}
	var detail struct {
		Objects []object.Object `json:"objects"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Objects) != 1 || detail.Next != "1" {
		t.Fatalf("transcript page = %d objects, next %q; want 1 and cursor 1", len(detail.Objects), detail.Next)
	}
}

func TestGatewayObjectActions(t *testing.T) {
	fake := newFakeSurface()
	srv := newObjectServer(t, allowAll{}, fake)

	status, body := post(t, srv.URL+"/thought/t1/pause", "")
	if status != http.StatusOK {
		t.Fatalf("pause = %d: %s", status, body)
	}
	status, body = post(t, srv.URL+"/thought/t1/steer", `{"message":"change of plan"}`)
	if status != http.StatusOK {
		t.Fatalf("steer = %d: %s", status, body)
	}
	fake.mu.Lock()
	acted := append([]string(nil), fake.acted...)
	fake.mu.Unlock()
	if len(acted) != 2 || !strings.Contains(acted[1], "change of plan") {
		t.Fatalf("surface saw %+v, want pause then steer with args", acted)
	}

	// A GET on an action path is a method error, not a detail read of a
	// collection named "pause".
	status, _ = get(t, srv.URL+"/thought/t1/pause")
	if status != http.StatusNotFound {
		t.Fatalf("GET on an action path = %d, want 404 (unknown detail)", status)
	}
}

func TestGatewayObjectErrorsMapToStatus(t *testing.T) {
	fake := newFakeSurface()
	srv := newObjectServer(t, allowAll{}, fake)

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"unknown type", "/ghost", http.StatusNotFound},
		{"unknown object", "/thought/nope", http.StatusNotFound},
		{"unknown detail", "/thought/t1/ghost", http.StatusNotFound},
		{"unknown action", "/thought/t1/explode", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == http.StatusBadRequest {
				status, _ := post(t, srv.URL+tc.path, "")
				if status != tc.want {
					t.Fatalf("%s = %d, want %d", tc.path, status, tc.want)
				}
				return
			}
			status, _ := get(t, srv.URL+tc.path)
			if status != tc.want {
				t.Fatalf("%s = %d, want %d", tc.path, status, tc.want)
			}
		})
	}

	// Invalid arguments and an unserved capability are distinct refusals.
	setActErr(fake, object.ErrInvalidArgs)
	if status, _ := post(t, srv.URL+"/thought/t1/steer", ""); status != http.StatusBadRequest {
		t.Fatalf("invalid args = %d, want 400", status)
	}
	setActErr(fake, object.ErrUnsupported)
	if status, _ := post(t, srv.URL+"/thought/t1/steer", ""); status != http.StatusNotImplemented {
		t.Fatalf("unsupported = %d, want 501", status)
	}
	setActErr(fake, fmt.Errorf("datastore exploded"))
	if status, _ := post(t, srv.URL+"/thought/t1/steer", ""); status != http.StatusInternalServerError {
		t.Fatalf("provider failure = %d, want 500", status)
	}

	// A body that is not a JSON object is a bad request.
	setActErr(fake, nil)
	if status, _ := post(t, srv.URL+"/thought/t1/pause", `[1,2,3]`); status != http.StatusBadRequest {
		t.Fatalf("non-object body = %d, want 400", status)
	}
}

// setActErr injects an action failure under the fake's lock.
func setActErr(f *fakeSurface, err error) {
	f.mu.Lock()
	f.actErr = err
	f.mu.Unlock()
}

func TestGatewayServesDeltas(t *testing.T) {
	srv := newObjectServer(t, allowAll{}, newFakeSurface())

	status, body := get(t, srv.URL+"/_deltas")
	if status != http.StatusOK {
		t.Fatalf("/_deltas = %d: %s", status, body)
	}
	var doc struct {
		Deltas []object.Delta `json:"deltas"`
		Next   int            `json:"next"`
		Lost   bool           `json:"lost"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Deltas) != 2 || doc.Next != 3 || doc.Lost {
		t.Fatalf("/_deltas = %+v next=%d lost=%v, want two deltas and cursor 3", doc.Deltas, doc.Next, doc.Lost)
	}
	if doc.Deltas[1].Op != object.OpAppend || doc.Deltas[1].Detail != "transcript" {
		t.Fatalf("second delta = %+v, want a transcript append", doc.Deltas[1])
	}

	// Resume from the reported cursor: nothing re-delivered.
	status, body = get(t, srv.URL+"/_deltas?from=3")
	if status != http.StatusOK {
		t.Fatalf("/_deltas?from=3 = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Deltas) != 0 {
		t.Fatalf("resume re-delivered %d deltas", len(doc.Deltas))
	}
}

// TestGatewayObjectRoutesFailClosed pins the security property: every object
// route refuses (403) when no authorizer is wired, exactly like the
// observation stream and the control channel.
func TestGatewayObjectRoutesFailClosed(t *testing.T) {
	srv := newObjectServer(t, nil, newFakeSurface())

	for _, path := range []string{"/_types", "/_deltas", "/thought", "/thought/t1", "/thought/t1/transcript"} {
		status, _ := get(t, srv.URL+path)
		if status != http.StatusForbidden {
			t.Fatalf("%s without authorizer = %d, want 403", path, status)
		}
	}
	status, _ := post(t, srv.URL+"/thought/t1/pause", "")
	if status != http.StatusForbidden {
		t.Fatalf("action without authorizer = %d, want 403", status)
	}
}

// TestGatewayObjectRoutesWithoutSurface pins the observation-only
// deployment: the object routes are unavailable, not empty.
func TestGatewayObjectRoutesWithoutSurface(t *testing.T) {
	srv := newObjectServer(t, allowAll{}, nil)
	status, _ := get(t, srv.URL+"/_types")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("/_types without a surface = %d, want 503", status)
	}
}

// TestClientRoundTripsTheSurface is the remote-binding gate: the same fake
// surface is read through gateway.Client, so the in-process and remote paths
// share one contract.
func TestClientRoundTripsTheSurface(t *testing.T) {
	fake := newFakeSurface()
	srv := newObjectServer(t, allowAll{}, fake)
	c := NewClient(srv.URL, "u")

	if err := c.RefreshTypes(); err != nil {
		t.Fatalf("RefreshTypes: %v", err)
	}
	types := c.Types()
	if len(types) != 2 || types[0].Name != "thought" {
		t.Fatalf("Types = %+v, want the served descriptors", types)
	}
	if _, ok := types[0].Action("steer"); !ok {
		t.Fatal("descriptor lost its action across the wire")
	}

	objs, next, err := c.List("thought", object.Query{Limit: 2})
	if err != nil || len(objs) != 2 || next != "2" {
		t.Fatalf("List = %d objects, next %q, err %v", len(objs), next, err)
	}
	objs, next, err = c.List("thought", object.Query{Limit: 2, Cursor: next})
	if err != nil || len(objs) != 1 || next != "" {
		t.Fatalf("List page 2 = %d objects, next %q, err %v", len(objs), next, err)
	}

	obj, err := c.Get("thought", "t1")
	if err != nil || obj.Fields["prompt"] != "prompt t1" {
		t.Fatalf("Get = %+v (%v)", obj, err)
	}

	entries, next, err := c.Detail("thought", "t1", "transcript", object.Query{Limit: 1})
	if err != nil || len(entries) != 1 || next != "1" {
		t.Fatalf("Detail = %d entries, next %q, err %v", len(entries), next, err)
	}

	if err := c.Act("thought", "t1", "steer", map[string]any{"message": "hi"}); err != nil {
		t.Fatalf("Act: %v", err)
	}
	deltas, dnext, lost := c.Watch(0)
	if len(deltas) != 2 || dnext != 3 || lost {
		t.Fatalf("Watch = %d deltas, next %d, lost %v", len(deltas), dnext, lost)
	}
}

// TestClientReportsRefusalsAndGaps pins the remote binding's failure
// behaviour: typed HTTP statuses for the error-returning methods, and a
// reported gap (not a silent "nothing changed") where the Surface contract
// has no error.
func TestClientReportsRefusalsAndGaps(t *testing.T) {
	srv := newObjectServer(t, nil, newFakeSurface())
	c := NewClient(srv.URL, "u")

	_, _, err := c.List("thought", object.Query{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("List without authorizer = %v, want a 403 StatusError", err)
	}

	// Watch cannot return an error, so a refusal must surface as a gap with
	// the cursor unchanged rather than as "no deltas".
	deltas, next, lost := c.Watch(7)
	if len(deltas) != 0 || next != 7 || !lost {
		t.Fatalf("Watch under refusal = %d deltas, next %d, lost %v; want 0/7/true", len(deltas), next, lost)
	}

	// A transport failure behaves the same way.
	dead := NewClient("http://127.0.0.1:1", "u")
	if _, _, lost := dead.Watch(3); !lost {
		t.Fatal("Watch against a dead endpoint must report a gap")
	}
	if err := dead.RefreshTypes(); err == nil {
		t.Fatal("RefreshTypes against a dead endpoint must fail")
	}
}
