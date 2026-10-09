package navi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/thought"
)

// gatewayParity builds a navi whose object surface is served by the gateway,
// plus a remote binding of it. It is the slice's parity seam: the same navi
// state read through the in-process surface and through HTTP must agree.
func gatewayParity(t *testing.T) (*Navi, *objectSurface, object.Surface) {
	t.Helper()
	n := New("gateway-parity", WithPlanCloseCheck(false),
		WithObjectAuthorizer(portal.AllowAll(), "test"))
	n.Datastores.Handle().Bind(inmem.New())
	n.transcripts = n.Datastores.Handle().Thoughts()

	base := time.Now().UTC()
	enqueueThought(t, n, "t1", base.Add(-2*time.Minute))
	enqueueThought(t, n, "t2", base.Add(-time.Minute))
	enqueueThought(t, n, "t3", base)

	entries := []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "hi"),
		datastore.NewEntry(datastore.RoleAssistant, "hello"),
		datastore.NewEntry(datastore.RoleTool, "tool out"),
	}
	if err := n.transcripts.SaveTranscript("t1", entries); err != nil {
		t.Fatal(err)
	}

	control := portal.NewControl(portal.AllowAll())
	if err := control.Start(context.Background(), portal.Deps{
		ThoughtSystem: n.Thoughts, Observation: n.observation,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gateway.Handler(control, n.observation, n.objects))
	t.Cleanup(srv.Close)

	c := gateway.NewClient(srv.URL, "test")
	if err := c.OpenStream(context.Background()); err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return n, n.objects, c
}

// mustJSON is the comparison form for surface values: marshalling both sides
// normalizes Go values (time.Time) and wire values (RFC 3339 strings) onto the
// same bytes.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(blob)
}

// TestGatewayClientMatchesInProcessReads is the parity gate: descriptors,
// object pages, and a thought's transcript read through HTTP are identical to
// the in-process surface.
func TestGatewayClientMatchesInProcessReads(t *testing.T) {
	_, local, remote := gatewayParity(t)

	remoteTypes, err := remote.Types()
	if err != nil {
		t.Fatalf("remote Types: %v", err)
	}
	localTypes, err := local.Types()
	if err != nil {
		t.Fatalf("local Types: %v", err)
	}
	if got, want := mustJSON(t, remoteTypes), mustJSON(t, localTypes); got != want {
		t.Fatalf("descriptors differ over the wire:\n remote %s\n local  %s", got, want)
	}

	localObjs, localNext, err := local.List(TypeThought, object.Query{})
	if err != nil {
		t.Fatal(err)
	}
	remoteObjs, remoteNext, err := remote.List(TypeThought, object.Query{})
	if err != nil {
		t.Fatalf("remote List: %v", err)
	}
	if localNext != remoteNext {
		t.Fatalf("next cursor differs: local %q remote %q", localNext, remoteNext)
	}
	if got, want := mustJSON(t, remoteObjs), mustJSON(t, localObjs); got != want {
		t.Fatalf("object pages differ over the wire:\n remote %s\n local  %s", got, want)
	}

	localObj, err := local.Get(TypeThought, "t1")
	if err != nil {
		t.Fatal(err)
	}
	remoteObj, err := remote.Get(TypeThought, "t1")
	if err != nil {
		t.Fatalf("remote Get: %v", err)
	}
	if got, want := mustJSON(t, remoteObj), mustJSON(t, localObj); got != want {
		t.Fatalf("object differs over the wire:\n remote %s\n local  %s", got, want)
	}

	// The conversation is the point: a completed thought's transcript must
	// render the same remotely as in-process.
	localEntries, localNext, err := local.Detail(TypeThought, "t1", DetailTranscript, object.Query{})
	if err != nil {
		t.Fatal(err)
	}
	remoteEntries, remoteNext, err := remote.Detail(TypeThought, "t1", DetailTranscript, object.Query{})
	if err != nil {
		t.Fatalf("remote Detail: %v", err)
	}
	if localNext != remoteNext {
		t.Fatalf("detail cursor differs: local %q remote %q", localNext, remoteNext)
	}
	if got, want := mustJSON(t, remoteEntries), mustJSON(t, localEntries); got != want {
		t.Fatalf("transcripts differ over the wire:\n remote %s\n local  %s", got, want)
	}
	if len(remoteEntries) != 3 {
		t.Fatalf("transcript = %d entries, want 3", len(remoteEntries))
	}
}

// TestGatewayClientPagesTheSameObjects checks the bounded-read contract end
// to end: following the remote cursor enumerates exactly what one in-process
// page would, with no duplicates and no gap.
func TestGatewayClientPagesTheSameObjects(t *testing.T) {
	_, local, remote := gatewayParity(t)

	localObjs, _, err := local.List(TypeThought, object.Query{})
	if err != nil {
		t.Fatal(err)
	}

	var paged []object.Object
	cursor := ""
	for page := 0; page < 10; page++ {
		objs, next, err := remote.List(TypeThought, object.Query{Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		paged = append(paged, objs...)
		if next == "" {
			break
		}
		cursor = next
	}
	if got, want := mustJSON(t, paged), mustJSON(t, localObjs); got != want {
		t.Fatalf("paged read differs from one page:\n paged %s\n one   %s", got, want)
	}
}

// TestGatewayClientActReachesTheNavi checks the write path end to end: an
// action posted to the gateway lands on the navi through the surface's own
// authorizer and argument validation.
func TestGatewayClientActReachesTheNavi(t *testing.T) {
	n, _, remote := gatewayParity(t)

	if err := remote.Act(TypeThought, "t1", "bump", map[string]any{"priority": 7}); err != nil {
		t.Fatalf("remote Act: %v", err)
	}
	got, err := n.Thoughts.GetThought("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 7 {
		t.Fatalf("priority after a remote bump = %d, want 7", got.Priority)
	}

	// Argument validation is the surface's, not the gateway's: a remote
	// caller cannot bypass the declared schema.
	if err := remote.Act(TypeThought, "t1", "bump", map[string]any{"priority": 99}); err == nil {
		t.Fatal("remote Act with out-of-range priority must fail")
	}
	if got, _ := n.Thoughts.GetThought("t1"); got.Priority != 7 {
		t.Fatalf("a rejected remote action mutated the object: priority %d", got.Priority)
	}
}

// TestGatewayClientWatchSeesDeltas checks the live path over the wire: a
// queue transition on the navi appears as an object delta to the remote
// binding.
func TestGatewayClientWatchSeesDeltas(t *testing.T) {
	n, _, remote := gatewayParity(t)
	if err := n.Thoughts.Cancel("t2"); err != nil {
		t.Fatal(err)
	}

	// The cancellation is pushed down the stream; read the local window until
	// it lands (the stream is asynchronous).
	deadline := time.Now().Add(5 * time.Second)
	var deltas []object.Delta
	var next int
	for {
		var lost bool
		var err error
		deltas, next, lost, err = remote.Watch(0)
		if err != nil {
			t.Fatalf("remote Watch: %v", err)
		}
		if lost {
			t.Fatal("a first read from cursor 0 must not report a gap")
		}
		var sawCancel bool
		for _, d := range deltas {
			if d.Type == TypeThought && d.ID == "t2" && d.Op == object.OpUpdate &&
				d.Fields["status"] == string(thought.ThoughtCancelled) {
				sawCancel = true
			}
		}
		if sawCancel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote deltas did not carry the cancellation: %+v", deltas)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next == 0 {
		t.Fatal("watch returned no cursor")
	}

	// Resuming from the returned cursor yields nothing already seen.
	again, _, _, err := remote.Watch(next)
	if err != nil {
		t.Fatalf("remote Watch resume: %v", err)
	}
	for _, d := range again {
		if d.ID == "t2" && d.Op == object.OpUpdate {
			t.Fatalf("cursor resume re-delivered a seen delta: %+v", d)
		}
	}
}

// TestGatewayClientDetailIsBounded pins the bounded-read contract over the
// wire: a large transcript is never served in one response, and the surface's
// page cap (not the caller's limit) is what bounds it.
func TestGatewayClientDetailIsBounded(t *testing.T) {
	n, _, remote := gatewayParity(t)

	const total = 600
	entries := make([]datastore.Entry, 0, total)
	for i := 0; i < total; i++ {
		entries = append(entries, datastore.NewEntry(datastore.RoleAssistant, "turn"))
	}
	if err := n.transcripts.SaveTranscript("t1", entries); err != nil {
		t.Fatal(err)
	}

	first, next, err := remote.Detail(TypeThought, "t1", DetailTranscript, object.Query{Limit: 10000})
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(first) != maxPage {
		t.Fatalf("first page = %d entries, want the surface cap %d", len(first), maxPage)
	}
	if next == "" {
		t.Fatal("a capped page must carry a cursor")
	}

	rest, next, err := remote.Detail(TypeThought, "t1", DetailTranscript, object.Query{Cursor: next, Limit: 10000})
	if err != nil {
		t.Fatalf("Detail page 2: %v", err)
	}
	if len(rest) != total-maxPage || next != "" {
		t.Fatalf("second page = %d entries, next %q; want %d and no cursor", len(rest), next, total-maxPage)
	}
}
