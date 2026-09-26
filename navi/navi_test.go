package navi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

// --- fakes ------------------------------------------------------------

type fakeConnector struct {
	started   chan struct{}
	stopped   chan struct{}
	startedOK bool
	gotDeps   bool
}

func (c *fakeConnector) Start(ctx context.Context, deps connector.Deps) error {
	c.startedOK = true
	if deps.ThoughtSystem == nil || deps.Model == nil || deps.Log == nil {
		return errors.New("fakeConnector: incomplete Deps")
	}
	c.gotDeps = true
	close(c.started)
	return nil
}

func (c *fakeConnector) Stop() error { close(c.stopped); return nil }

type fakePortal struct {
	addr    string
	started chan struct{}
	stopped chan struct{}
	seen    []thought.Event
}

func (p *fakePortal) Start(ctx context.Context, deps portal.Deps) error {
	deps.ThoughtSystem.OnEvent(func(ev thought.Event) {
		p.seen = append(p.seen, ev)
	})
	close(p.started)
	return nil
}

func (p *fakePortal) Stop() error                            { close(p.stopped); return nil }
func (p *fakePortal) Addr() string                           { return p.addr }
func (p *fakePortal) Attach(string) error                    { return nil }
func (p *fakePortal) Steer(a, b, c string) error             { return nil }
func (p *fakePortal) Pause(a, b string) error                { return nil }
func (p *fakePortal) Resume(a, b string) error               { return nil }
func (p *fakePortal) Cancel(a, b string) error               { return nil }
func (p *fakePortal) Enqueue(string, *thought.Thought) error { return nil }

func newFakeConnector() *fakeConnector {
	return &fakeConnector{started: make(chan struct{}), stopped: make(chan struct{})}
}

func newFakePortal(addr string) *fakePortal {
	return &fakePortal{addr: addr, started: make(chan struct{}), stopped: make(chan struct{})}
}

// mustSchema parses a JSON schema, panicking on a bad literal — this is
// a test fixture, not author-facing code.
func mustSchema(raw string) tool.Schema {
	s, err := tool.ParseSchema(json.RawMessage(raw))
	if err != nil {
		panic(err)
	}
	return s
}

// validTool builds a tool that passes shape validation.
func validTool(name string) tool.Tool {
	return tool.Tool{
		Def: tool.Def{
			Name:        name,
			Description: "test tool " + name,
			InputSchema: mustSchema(`{"type":"object","properties":{}}`),
			Access:      tool.AccessReadOnly,
			Mode:        tool.ToolModeParallel,
			SourceInfo:  tool.SourceInfo{Origin: tool.OriginAuthor},
		},
		Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
	}
}

// --- Add validation ---------------------------------------------------

func TestAddShapeValidation(t *testing.T) {
	n := New("t")

	if err := n.Skills.Add(skill.Skill{Name: "a"}); err != nil {
		t.Fatalf("valid skill rejected: %v", err)
	}
	if err := n.Skills.Add(skill.Skill{Name: "a"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate skill: want ErrDuplicate, got %v", err)
	}
	if err := n.Skills.Add(skill.Skill{Name: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank skill name: want ErrInvalid, got %v", err)
	}
	if got := n.Skills.Len(); got != 1 {
		t.Fatalf("rejected adds must not be stored: len=%d", got)
	}

	// Tool shape validation is the load-bearing one: a tool that cannot
	// be described to the model or dispatched is rejected at Add.
	if err := n.Tools.Add(validTool("file_read")); err != nil {
		t.Fatalf("valid tool rejected: %v", err)
	}
	noHandler := validTool("file_write")
	noHandler.Handler = nil
	if err := n.Tools.Add(noHandler); !errors.Is(err, ErrInvalid) {
		t.Fatalf("handler-less tool: want ErrInvalid, got %v", err)
	}
}

func TestAddRejectsAfterFreeze(t *testing.T) {
	n := New("t")
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
	// Run freezes; Validate alone does not. Drive the freeze directly so
	// the test does not need a full serve cycle.
	n.freeze()
	if err := n.Skills.Add(skill.Skill{Name: "late"}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("add after freeze: want ErrFrozen, got %v", err)
	}
}

// --- cross-cutting validation ----------------------------------------

func TestRunCrossCuttingValidation(t *testing.T) {
	cases := []struct {
		name  string
		opts  []Option
		build func(*Navi)
		want  error
	}{
		{
			name: "nameless navi",
			want: ErrNoName,
		},
		{
			name: "mode activates unknown tool",
			build: func(n *Navi) {
				_ = n.Modes.Add(Mode{Name: "plan", Tools: []string{"nope_write"}})
			},
			want: ErrInvalid,
		},
		{
			name: "port conflict across portals",
			build: func(n *Navi) {
				_ = n.Portals.Add(newFakePortal(":8090"))
				_ = n.Portals.Add(newFakePortal(":8090"))
			},
			want: ErrInvalid,
		},
		{
			name: "mode escalates access above the ceiling",
			build: func(n *Navi) {
				n.Permissions = PermissionPolicy{MaxAccess: tool.AccessReadOnly}
				_ = n.Modes.Add(Mode{
					Name:        "exec",
					Permissions: PermissionPolicy{MaxAccess: tool.AccessExecute},
				})
			},
			want: ErrInvalid,
		},
		{
			name: "mode permits a tool the ceiling denies",
			build: func(n *Navi) {
				n.Permissions = PermissionPolicy{DenyTools: []string{"shell_run"}}
				_ = n.Modes.Add(Mode{
					Name:        "exec",
					Permissions: PermissionPolicy{AllowTools: []string{"shell_run"}},
				})
			},
			want: ErrInvalid,
		},
		{
			name: "models declared without a default",
			build: func(n *Navi) {
				_ = n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"})
			},
			want: ErrInvalid,
		},
		{
			name: "default model names an undeclared provider",
			opts: []Option{WithDefaultModel("ghost")},
			build: func(n *Navi) {
				_ = n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"})
			},
			want: ErrInvalid,
		},
		{
			name: "duplicate tool names survive an ignored Add error",
			build: func(n *Navi) {
				// An author who drops Add's error would have ONE tool,
				// not two; this drives the collection directly to prove
				// Run re-checks independently of Add.
				n.Tools.items = append(n.Tools.items,
					validTool("file_read"), validTool("file_read"))
			},
			want: ErrDuplicate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label := "t"
			if tc.name == "nameless navi" {
				label = ""
			}
			n := New(label, tc.opts...)
			if tc.build != nil {
				tc.build(n)
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Run must fail before serving, not block.
			err := n.Run(ctx)
			if err == nil {
				t.Fatalf("want an error, got nil")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if n.Serving() {
				t.Fatal("a navi that failed validation must not be serving")
			}
		})
	}
}

func TestValidationFailureStartsNothing(t *testing.T) {
	c := newFakeConnector()
	n := New("t")
	n.Connectors.Add(c)
	_ = n.Modes.Add(Mode{Name: "plan", Tools: []string{"ghost_read"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Run(ctx); err == nil {
		t.Fatal("want validation error")
	}
	select {
	case <-c.started:
		t.Fatal("connector started despite failed validation")
	default:
	}
}

func TestPermissionCeilingNarrowingIsAllowed(t *testing.T) {
	n := New("t",
		WithPermissions(PermissionPolicy{
			MaxAccess:  tool.AccessExecute,
			AllowTools: []string{"file_read", "file_write"},
		}),
		WithDefaultModel("ollama"),
	)
	_ = n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"})
	// A mode may only name tools the navi actually declares.
	if err := n.Tools.Add(validTool("file_read")); err != nil {
		t.Fatal(err)
	}
	if err := n.Tools.Add(validTool("file_write")); err != nil {
		t.Fatal(err)
	}
	// Narrower in both dimensions: lower access, subset of the allow-list.
	_ = n.Modes.Add(Mode{
		Name:        "read-only",
		Tools:       []string{"file_read"},
		Permissions: PermissionPolicy{MaxAccess: tool.AccessReadOnly},
	})
	if err := n.Validate(); err != nil {
		t.Fatalf("a narrowing mode must be accepted: %v", err)
	}
}

// --- the M2 gate: a navi runs end to end, events flow -----------------

func TestRunEndToEndEventsFlow(t *testing.T) {
	c := newFakeConnector()
	p := newFakePortal(":9123")

	n := New("gate-test", WithVersion("0.0.1"), WithDefaultModel("ollama"))
	_ = n.Models.Add(model.Provider{Name: "ollama", Model: "qwen3.8:27b"})
	n.Connectors.Add(c)
	n.Portals.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()

	// Both components must be live before we exercise the stream: the
	// portal subscribes in Start.
	waitFor(t, c.started, "connector start")
	waitFor(t, p.started, "portal start")
	if !c.gotDeps {
		t.Fatal("connector did not receive complete Deps")
	}
	if !n.Serving() {
		t.Fatal("navi should report serving once Run has started components")
	}

	// Drive the thought system through the same handle components hold,
	// and observe the event on the portal's subscription.
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t-1", Prompt: "hello", Priority: 5,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitForEvent(t, p, thought.EventEnqueued)

	// Run returns when the context is cancelled, having stopped
	// everything it started.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	waitFor(t, c.stopped, "connector stop")
	waitFor(t, p.stopped, "portal stop")
}

func TestRunAbortsAndStopsAlreadyStartedComponents(t *testing.T) {
	first := newFakeConnector()
	bad := &failingConnector{}
	n := New("t")
	n.Connectors.Add(first)
	n.Connectors.Add(bad)

	if err := n.Run(context.Background()); err == nil {
		t.Fatal("want a start error")
	}
	// The component that was already serving must be shut down.
	waitFor(t, first.stopped, "already-started connector stop")
	// The one whose Start failed cleans up after itself; Stop is not
	// called on it (the documented convention on connector.Connector).
	if bad.stopCalled {
		t.Fatal("Stop called on a connector whose Start failed")
	}
}

type failingConnector struct{ stopCalled bool }

func (c *failingConnector) Start(context.Context, connector.Deps) error {
	return errors.New("boom")
}
func (c *failingConnector) Stop() error { c.stopCalled = true; return nil }

// --- helpers ----------------------------------------------------------

func waitFor(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitForEvent(t *testing.T, p *fakePortal, kind thought.EventKind) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range p.seen {
			if ev.Kind == kind {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q", kind)
}
