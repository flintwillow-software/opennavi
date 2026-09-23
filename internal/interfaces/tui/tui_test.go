package tui

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/daemon"
	"github.com/flintwillow-software/opennavi/internal/interfaces"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// allowAllAuth permits every portal action: the shim-attachment test
// seam (production wires nil and fails closed; TUI.LocalAuthorizer is
// the in-process local-trust authorizer).
type allowAllAuth struct{}

func (a allowAllAuth) Authorize(action interfaces.PortalAction, identity string) error {
	return nil
}

func newQueue(t *testing.T) *thought.ThoughtQueue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 4, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func th(id string, prio int) *thought.Thought {
	return &thought.Thought{ID: id, Prompt: "task " + id, Priority: prio, Status: thought.ThoughtPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

// drain applies the feed's pending events to the model, mirroring the
// poll cmd (ReadAfter cursor semantics).
func drain(m *tuiModel) {
	evs, next, lost := m.feed.ReadAfter(m.cursor)
	if len(evs) == 0 && !lost {
		return
	}
	m.cursor = next
	_, _ = m.Update(feedMsg{evs: evs, lost: lost})
}

func TestLocalAuthorizerPermitsLocal(t *testing.T) {
	if err := (LocalAuthorizer{}).Authorize(interfaces.PortalActSteer, LocalIdentity); err != nil {
		t.Errorf("local authorizer refused local control: %v", err)
	}
}

func TestModelSeedsAndTracksLifecycle(t *testing.T) {
	q := newQueue(t)
	tport, err := NewInProcess(q)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(tport)
	_ = m.Init() // seed from snapshot (returns the poll cmd, re-armed by tea)

	if err := q.Enqueue(th("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}

	drain(m)
	view := m.View()
	if !strings.Contains(view, "t1") || !strings.Contains(view, "running") {
		t.Errorf("row missing/status stale:\n%s", view)
	}

	// Complete it: the lifecycle transition lands within one drain.
	if err := q.Complete("t1"); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if !strings.Contains(m.View(), "complete") {
		t.Errorf("lifecycle update lost:\n%s", m.View())
	}
}

func TestModelAckDisplay(t *testing.T) {
	q := newQueue(t)
	tport, _ := NewInProcess(q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("t2", 4)); err != nil {
		t.Fatal(err)
	}
	path, err := q.RouteMessage("t2", "hello")
	if err != nil || path != thought.PathContextAppend {
		t.Fatalf("RouteMessage: %v", err)
	}
	drain(m)
	if !strings.Contains(m.status, "delivered") || !strings.Contains(m.status, "t2") {
		t.Errorf("steering ack not displayed: %q", m.status)
	}
}

func TestModelLiveView(t *testing.T) {
	q := newQueue(t)
	tport, _ := NewInProcess(q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("t3", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m)
	// Hop into the live view of t3 (Select then enter via key).
	m.sel = 0
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != ModeLive || m.liveID != "t3" {
		t.Fatal("did not enter live view")
	}
	// Agent event for the live thought appears in the buffer (driven
	// through the portal fan, the runner-hook path).
	tport.portal.ObserveAgent("t3", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "hello world"},
	})
	drain(m)
	if len(m.live) == 0 || !strings.Contains(m.live[0], "content_delta") {
		t.Errorf("live buffer empty: %v", m.live)
	}
	// Events for OTHER thoughts do not pollute the live view.
	tport.portal.ObserveAgent("other", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "noise"},
	})
	drain(m)
	if len(m.live) != 1 {
		t.Errorf("foreign agent event leaked into live view: %v", m.live)
	}
}

func TestActionsThroughTransport(t *testing.T) {
	q := newQueue(t)
	tport, _ := NewInProcess(q)
	if err := tport.Bump("x", 2); err == nil {
		t.Error("bump of unknown thought succeeded")
	}
	if err := q.Enqueue(th("t4", 4)); err != nil {
		t.Fatal(err)
	}
	if err := tport.Steer("t4", "queue me right"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	got, err := q.GetThought("t4")
	if err != nil || !strings.Contains(got.Context, "queue me right") {
		t.Fatalf("steer did not reach the thought: %v", err)
	}
	if err := tport.Bump("t4", 2); err != nil {
		t.Fatalf("bump: %v", err)
	}
	got, _ = q.GetThought("t4")
	if got.Priority != 2 {
		t.Errorf("priority not bumped: %d", got.Priority)
	}
	// Cancel a pending thought.
	if err := tport.Cancel("t4"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Pause/resume a running thought.
	if err := q.Enqueue(th("t5", 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for pause")
	}
	if err := tport.Pause("t5"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := tport.Resume("t5"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Park t5 again so the retry thought below is the only claimable
	// one (paused thoughts are not claimable).
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t5 no longer claimable after resume")
	}
	if err := tport.Pause("t5"); err != nil {
		t.Fatalf("re-pause: %v", err)
	}
	// Retry a failed thought.
	if err := q.Enqueue(th("t6", 3)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for fail")
	}
	if err := q.Fail("t6", "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := tport.Retry("t6"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, _ = q.GetThought("t6")
	if got.Status != thought.ThoughtPending {
		t.Errorf("retry did not re-queue: %q", got.Status)
	}
}

// ── remote transport (gateway attachment seam) ───────────────────────────

func portalFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestRemoteTransportControlAndEvents(t *testing.T) {
	addr := portalFreePort(t)
	rt, err := daemon.Assemble(daemon.RuntimeParams{
		SessionDB:          t.TempDir() + "/tui-remote.db",
		Model:              "test-model",
		Workers:            1,
		PortalAddr:         addr,
		OverrideAuthorizer: allowAllAuth{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	// Serve the portal WITHOUT starting the runtime's pool: the
	// thought stays pending, so remote control is deterministic (no
	// runner racing the queue).
	ln, lerr := net.Listen("tcp", addr)
	if lerr != nil {
		t.Fatal(lerr)
	}
	srv := &http.Server{Addr: addr, Handler: daemon.PortalHandler(rt), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	shutdown, cancelSrv := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSrv()
	defer func() { _ = srv.Shutdown(shutdown) }()

	base := "http://" + addr
	time.Sleep(250 * time.Millisecond)
	rport, terr := NewRemote(base, "tui-client")
	if terr != nil {
		t.Fatal(terr)
	}

	// Control against the (authorized) shim: steer a pending thought;
	// the delivery-ack comes back on the remote feed.
	if err := rt.Queue.Enqueue(th("r1", 4)); err != nil {
		t.Fatal(err)
	}
	if err := rport.Steer("r1", "from remote"); err != nil {
		t.Fatalf("remote steer: %v", err)
	}
	got, err := rt.Queue.GetThought("r1")
	if err != nil || !strings.Contains(got.Context, "from remote") {
		t.Fatalf("remote steer did not land: %v", err)
	}

	// The pump delivers stream events into the feed (lifecycle +
	// delivery ack) within a sane window.
	deadline := time.Now().Add(5 * time.Second)
	var sawAck, sawEnqueued bool
	for {
		evs, _, _ := rport.Feed().ReadAfter(0)
		for _, ev := range evs {
			if ev.Kind == "message_routed" && ev.ThoughtID == "r1" {
				sawAck = true
			}
			if ev.Kind == "enqueued" {
				sawEnqueued = true
			}
		}
		if sawAck && sawEnqueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote feed never delivered the ack/enqueued events")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Control against a NO-authorizer shim fails closed (403 →
	// error): same seamlnd, no Runtime.Run involved.
	addr2 := portalFreePort(t)
	rt2, err2 := daemon.Assemble(daemon.RuntimeParams{
		SessionDB:  t.TempDir() + "/tui-remote2.db",
		Model:      "test-model",
		Workers:    1,
		PortalAddr: addr2,
	})
	if err2 != nil {
		t.Fatal(err2)
	}
	defer rt2.Store.Close()
	ln2, lerr2 := net.Listen("tcp", addr2)
	if lerr2 != nil {
		t.Fatal(lerr2)
	}
	srv2 := &http.Server{Addr: addr2, Handler: daemon.PortalHandler(rt2), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv2.Serve(ln2) }()
	shutdown2, cancelSrv2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSrv2()
	defer func() { _ = srv2.Shutdown(shutdown2) }()
	time.Sleep(250 * time.Millisecond)
	failport, ferr := NewRemote("http://"+addr2, "intruder")
	if ferr != nil {
		t.Fatal(ferr)
	}
	if gerr := failport.Steer("x", "hi"); gerr == nil || !strings.Contains(gerr.Error(), "unauthorized") {
		t.Errorf("remote control without authorizer: %v, want fail-closed", gerr)
	}
}

// ── tea program smoke (no terminal; buffered io) ─────────────────────────

func TestTeaProgramRunsAndQuits(t *testing.T) {
	q := newQueue(t)
	tport, _ := NewInProcess(q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("smoke", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m)

	// The full program with buffered input/output: type 'q' to quit.
	var buf bytes.Buffer
	var in bytes.Buffer
	in.Write([]byte("q"))
	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()
	p := tea.NewProgram(m, tea.WithInput(&in), tea.WithOutput(&buf), tea.WithContext(ctx))
	model2, perr := p.Run()
	if perr != nil {
		t.Fatal(perr)
	}
	if model2 == nil {
		t.Fatal("program returned no model")
	}
	// The model quit cleanly; the queue row survived.
	if !strings.Contains(m.View(), "smoke") {
		t.Errorf("view lost rows: %q", m.View())
	}
}
