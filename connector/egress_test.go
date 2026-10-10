package connector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/tool"
)

// fakeClient stands in for a connector's service client: the tool must reach
// THIS value, and only while the connector is serving.
type fakeClient struct{ name string }

func egressDef(t *testing.T, name string) tool.Def {
	t.Helper()
	schema, err := tool.ParseSchema(json.RawMessage(`{"type":"object","properties":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	return tool.Def{
		Name:        name,
		Description: "test egress tool",
		InputSchema: schema,
		Access:      tool.AccessWrite,
		Recovery:    "capture_response",
	}
}

// TestEgressToolFailsClosedBeforeStart is ADR 0014's consequence and NAVI-371's
// acceptance criterion: a tool bound at authoring time is called before its
// connector serves, and it must fail cleanly rather than dereference nil.
func TestEgressToolFailsClosedBeforeStart(t *testing.T) {
	live := NewLive[*fakeClient]()
	called := false
	tl, err := SendTool(egressDef(t, "test_send"), live, func(_ context.Context, c *fakeClient, _ map[string]any) (string, error) {
		called = true
		return c.name, nil
	})
	if err != nil {
		t.Fatalf("SendTool: %v", err)
	}

	if _, err := tl.Handler(context.Background(), nil); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("call before Start = %v, want ErrNotStarted", err)
	}
	if called {
		t.Fatal("the handler ran before the connector was serving")
	}
	if live.Serving() {
		t.Fatal("a fresh Live must not report serving")
	}
}

// TestEgressToolReachesTheLiveClient is the other half: once Start publishes a
// client, the same tool reaches it.
func TestEgressToolReachesTheLiveClient(t *testing.T) {
	live := NewLive[*fakeClient]()
	tl, err := SendTool(egressDef(t, "test_send"), live, func(_ context.Context, c *fakeClient, args map[string]any) (string, error) {
		if c == nil {
			t.Fatal("handler received a nil client")
		}
		return c.name + ":" + args["text"].(string), nil
	})
	if err != nil {
		t.Fatalf("SendTool: %v", err)
	}

	live.Set(&fakeClient{name: "svc"})
	got, err := tl.Handler(context.Background(), map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("call while serving: %v", err)
	}
	if got != "svc:hi" {
		t.Fatalf("call = %q, want svc:hi", got)
	}

	// After Stop the tool is gated again, so a call fails rather than reaching
	// a client the connector has released.
	live.Clear()
	if _, err := tl.Handler(context.Background(), map[string]any{"text": "hi"}); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("call after Stop = %v, want ErrNotStarted", err)
	}
}

// TestEgressToolRecordsProvenance pins ADR 0020 decision 5: an egress tool is
// attributable from the catalog, and the namespace IS the connector identity.
func TestEgressToolRecordsProvenance(t *testing.T) {
	tl, err := SendTool(egressDef(t, "github_issue_comment"), NewLive[*fakeClient](),
		func(context.Context, *fakeClient, map[string]any) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("SendTool: %v", err)
	}
	if tl.Def.SourceInfo.Origin != tool.OriginAuthor {
		t.Fatalf("origin = %v, want author", tl.Def.SourceInfo.Origin)
	}
	if tl.Def.SourceInfo.Path != "github" {
		t.Fatalf("provenance path = %q, want the connector namespace github", tl.Def.SourceInfo.Path)
	}
	if tl.Def.Namespace() != "github" {
		t.Fatalf("namespace = %q, want github", tl.Def.Namespace())
	}
}

// TestEgressToolRejectsAnUnattributableShape is the guard: a capability cannot
// be registered in a shape that hides what it does.
func TestEgressToolRejectsAnUnattributableShape(t *testing.T) {
	live := NewLive[*fakeClient]()
	ok := func(context.Context, *fakeClient, map[string]any) (string, error) { return "", nil }

	cases := []struct {
		name    string
		mutate  func(*tool.Def)
		wantSub string
	}{
		{"unnamed", func(d *tool.Def) { d.Name = "" }, "needs a name"},
		{"un-namespaced", func(d *tool.Def) { d.Name = "send" }, "must be namespaced"},
		{"no access level", func(d *tool.Def) { d.Access = tool.AccessAny }, "access level"},
		{"no schema", func(d *tool.Def) { d.InputSchema = tool.Schema{} }, "input schema"},
		{"no recovery strategy", func(d *tool.Def) { d.Recovery = "" }, "recovery strategy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := egressDef(t, "test_send")
			tc.mutate(&def)
			if _, err := SendTool(def, live, ok); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("SendTool = %v, want an error mentioning %q", err, tc.wantSub)
			}
		})
	}

	if _, err := SendTool(egressDef(t, "test_send"), nil, ok); err == nil {
		t.Fatal("a nil live handle must be refused")
	}
	if _, err := SendTool(egressDef(t, "test_send"), live, nil); err == nil {
		t.Fatal("a nil handler must be refused")
	}
}

// TestEgressToolConcurrentCallsAreSafe guards the handle: a connector starts
// while calls are in flight.
func TestEgressToolConcurrentCallsAreSafe(t *testing.T) {
	live := NewLive[*fakeClient]()
	tl, err := SendTool(egressDef(t, "test_send"), live, func(_ context.Context, c *fakeClient, _ map[string]any) (string, error) {
		return c.name, nil
	})
	if err != nil {
		t.Fatalf("SendTool: %v", err)
	}
	live.Set(&fakeClient{name: "svc"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			live.Set(&fakeClient{name: "svc"})
			live.Clear()
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := tl.Handler(context.Background(), nil); err != nil && !errors.Is(err, ErrNotStarted) {
			t.Fatalf("concurrent call: %v", err)
		}
	}
	<-done
}
