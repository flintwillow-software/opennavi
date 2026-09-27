package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/daemon"
	"github.com/flintwillow-software/opennavi/internal/portal"
	"github.com/flintwillow-software/opennavi/internal/session"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// allowAllAuth permits every portal action: the shim-attachment test
// seam (production wires nil and fails closed; TUI.LocalAuthorizer is
// the in-process local-trust authorizer).
type allowAllAuth struct{}

func (a allowAllAuth) Authorize(action portal.PortalAction, identity string) error {
	return nil
}

// sessionSessions builds a session manager over an in-memory store
// (the tui test seam for transcript replay, NAVI-334).
func sessionSessions(t *testing.T) *session.SessionManager {
	t.Helper()
	return session.NewSessionManager(session.NewMemoryStore())
}

func newQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 4, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

// newInProcess builds the in-process transport over q with a fresh
// observation broker (NAVI-352): queue lifecycle events are wired in and
// the returned broker lets tests publish agent events.
func newInProcess(t *testing.T, q *thought.Queue, sessions *session.SessionManager) (*inProcessTransport, *pubportal.Broker) {
	t.Helper()
	broker := pubportal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(pubportal.QueueEvent(ev)) })
	tport, err := NewInProcess(q, broker, sessions)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	return tport, broker
}

// observe publishes an agent event through the broker the way the real
// runner's producer does.
func observe(broker *pubportal.Broker, thoughtID string, ev agent.AgentEvent) {
	detail := ""
	switch {
	case ev.Tool != nil:
		detail = strings.TrimSpace(ev.Tool.Name + " " + string(ev.Tool.Input))
	case ev.Message != nil:
		detail = strings.TrimSpace(ev.Message.Content)
	case ev.Error != nil:
		detail = ev.Error.Error()
	}
	broker.Publish(pubportal.Event{
		Category: pubportal.CategoryAgent, Kind: string(ev.Type),
		ThoughtID: thoughtID, Detail: detail, At: time.Now().UTC(),
	})
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
	if err := (LocalAuthorizer{}).Authorize(portal.PortalActSteer, LocalIdentity); err != nil {
		t.Errorf("local authorizer refused local control: %v", err)
	}
}

func TestModelSeedsAndTracksLifecycle(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
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
	if err := q.Complete("t1", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if !strings.Contains(m.View(), "complete") {
		t.Errorf("lifecycle update lost:\n%s", m.View())
	}
}

func TestModelAckDisplay(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("t2", 4)); err != nil {
		t.Fatal(err)
	}
	path, err := q.RouteMessageWithPath("t2", "hello")
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
	tport, broker := newInProcess(t, q, sessionSessions(t))
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
	// The thought's Prompt is the opening user turn (chat-window
	// rebuild-on-attach, NAVI-330).
	if len(m.turns) == 0 || m.turns[0].role != TurnUser || !strings.Contains(m.turns[0].text, "task t3") {
		t.Errorf("opening user turn missing: %+v", m.turns)
	}
	// Assistant deltas accumulate into ONE assistant turn.
	observe(broker, "t3", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "hello world"},
	})
	drain(m)
	observe(broker, "t3", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "!"},
	})
	drain(m)
	if !strings.Contains(m.View(), "hello world!") || !strings.Contains(m.View(), "◂") {
		t.Errorf("deltas did not accumulate into one assistant turn:\n%s", m.View())
	}
	// Tool calls render as indented blocks.
	observe(broker, "t3", agent.AgentEvent{
		Type: agent.AgentEventToolCall,
		Tool: &agent.ToolUpdate{ToolCallID: "c1", Name: "note_write", Input: json.RawMessage(`{"title":"x"}`)},
	})
	drain(m)
	if !strings.Contains(m.View(), "⚙") || !strings.Contains(m.View(), "note_write") {
		t.Errorf("tool block missing:\n%s", m.View())
	}
	// Lifecycle transitions land as system lines (claim it first so
	// started/completed both fan).
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("t3 not claimable")
	}
	if err := q.Complete("t3", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if !strings.Contains(m.View(), "complete") {
		t.Errorf("lifecycle system line missing:\n%s", m.View())
	}
	// Events for OTHER thoughts do not pollute the live window.
	observe(broker, "other", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "noise"},
	})
	drain(m)
	if strings.Contains(m.View(), "noise") {
		t.Errorf("foreign agent event leaked into live view")
	}
}

func TestActionsThroughTransport(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
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

// ── enqueue action (NAVI-326) ───────────────────────────────────────────

func TestEnqueueThroughTransport(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))

	// Default priority thought, due now, foreground-tagged.
	if err := tport.Enqueue("a brand new task", 0, time.Time{}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	all := q.GetThoughts()
	var found string
	for _, th := range all {
		if th.Prompt == "a brand new task" {
			found = th.ID
			if th.Priority != 4 {
				t.Errorf("default priority = %d, want 4", th.Priority)
			}
			if !th.Foreground {
				t.Error("session thought not foreground-tagged")
			}
		}
	}
	if found == "" {
		t.Fatal("session thought not enqueued")
	}

	// Explicit priority + deferral.
	due := time.Now().UTC().Add(time.Hour)
	if err := tport.Enqueue("later task", 2, due); err != nil {
		t.Fatalf("enqueue deferred: %v", err)
	}
	did := false
	for _, th := range q.GetThoughts() {
		if th.Prompt == "later task" {
			did = true
			if th.Priority != 2 {
				t.Errorf("priority = %d, want 2", th.Priority)
			}
			if th.DeferUntil.Compare(due) != 0 {
				t.Errorf("defer_until not applied")
			}
		}
	}
	if !did {
		t.Fatal("deferred thought not enqueued")
	}
}

func keyRunes(t *testing.T, m *tuiModel, s string) {
	t.Helper()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: stringToRunes(s)})
}

func stringToRunes(s string) []rune {
	out := []rune{}
	for i := 0; i < len(s); i++ {
		out = append(out, rune(s[i]))
	}
	return out
}

func keyEnter(m *tuiModel) { _, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) }

func TestModelEnqueueFlow(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()

	// n -> prompt "say hi" -> priority (default) -> defer (none).
	keyRunes(t, m, "n")
	if m.mode != ModeInput || !m.enqueue || m.stage != 0 {
		t.Fatal("n did not open the enqueue input")
	}
	keyRunes(t, m, "say hi")
	keyEnter(m) // prompt done
	if m.stage != 1 {
		t.Errorf("stage = %d, want 1 (priority)", m.stage)
	}
	keyEnter(m) // default priority
	if m.stage != 2 {
		t.Errorf("stage = %d, want 2 (defer)", m.stage)
	}
	keyEnter(m) // no deferral -> enqueue
	if m.mode != ModeList {
		t.Errorf("mode = %q, want list after enqueue", m.mode)
	}
	if !strings.Contains(m.status, "enqueued at p4") {
		t.Errorf("status = %q", m.status)
	}

	// The thought landed and shows live in the queue view (the
	// enqueued event rides the same feed as any other transition).
	drain(m)
	if !strings.Contains(m.View(), "say hi") || !strings.Contains(m.View(), "pending") {
		t.Errorf("enqueued thought missing from queue view:\n%s", m.View())
	}
	all := q.GetThoughts()
	var ok bool
	for _, th := range all {
		if th.Prompt == "say hi" && th.Priority == 4 && th.Foreground {
			ok = true
		}
	}
	if !ok {
		t.Error("enqueued thought not in the queue with default priority")
	}

	// Priority + deferral from the keyboard, no pool interference: the
	// previous thought stays pending (no pool started here).
	keyRunes(t, m, "n")
	keyRunes(t, m, "priority task")
	keyEnter(m)
	keyRunes(t, m, "2")
	keyEnter(m)
	keyRunes(t, m, "60")
	keyEnter(m)
	if !strings.Contains(m.status, "enqueued at p2") || !strings.Contains(m.status, "deferred") {
		t.Errorf("status = %q, want enqueued at p2 (deferred)", m.status)
	}
	found := false
	for _, th := range q.GetThoughts() {
		if th.Prompt == "priority task" && th.Priority == 2 && !th.DeferUntil.IsZero() {
			found = true
		}
	}
	if !found {
		t.Error("priority/deferred thought mis-enqueued")
	}
}

func TestModelEnqueueRejectsEmptyPrompt(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()

	keyRunes(t, m, "n")
	keyEnter(m) // empty prompt
	if m.mode != ModeList || !strings.Contains(m.status, "prompt is required") {
		t.Errorf("empty prompt not rejected: %q", m.status)
	}
	if len(q.GetThoughts()) != 0 {
		t.Error("empty-prompt enqueue created a thought")
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

	// Remote enqueue (NAVI-326): a session thought lands in the queue
	// and the stream shows it.
	if err := rport.Enqueue("remote new task", 2, time.Time{}); err != nil {
		t.Fatalf("remote enqueue: %v", err)
	}
	foundRemote := false
	for _, th := range rt.Queue.GetThoughts() {
		if th.Prompt == "remote new task" && th.Priority == 2 {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Error("remote-enqueued thought not in the queue")
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
	if gerr := failport.Enqueue("nope", 0, time.Time{}); gerr == nil || !strings.Contains(gerr.Error(), "unauthorized") {
		t.Errorf("remote enqueue without authorizer: %v, want fail-closed", gerr)
	}
}

// ── tea program smoke (no terminal; buffered io) ─────────────────────────

func TestTeaProgramRunsAndQuits(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
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

func TestModelShowsModelColumn(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()
	tagged := th("mm1", 4)
	tagged.Model = "very-long-model-name"
	if err := q.Enqueue(tagged); err != nil {
		t.Fatal(err)
	}
	drain(m)
	view := m.View()
	// The model shows abbreviated (clipped at 10 chars).
	if !strings.Contains(view, "very-long") {
		t.Errorf("model column missing:\n%s", view)
	}
	abbrev := "very-long-"
	if strings.Contains(view, "very-long-model-name") {
		t.Error("model not abbreviated in the row")
	}
	_ = abbrev
}

func TestSanitizeRowNewlines(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()
	weird := th("n1", 4)
	weird.Prompt = "hello\nworld check\tthis\rnice"
	if err := q.Enqueue(weird); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(th("n2", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m)
	view := m.View()

	// The two thoughts render as two SEPARATE row lines (the embedded
	// newline must not fuse them, NAVI-333).
	lines := strings.Split(view, "\n")
	nrows := 0
	for _, l := range lines {
		if strings.Contains(l, "n1") || strings.Contains(l, "n2") {
			nrows++
		}
	}
	if nrows != 2 {
		t.Errorf("rows fused/merged: %d row lines for 2 thoughts:\n%s", nrows, view)
	}
	// The newline became a space; no literal \n inside the row.
	if !strings.Contains(view, "hello world check this nice") {
		t.Errorf("control chars not neutralized to spaces:\n%s", view)
	}
	if strings.Contains(view, "hello\nworld") {
		t.Error("literal newline leaked into a row")
	}
	// Data untouched: the persisted prompt keeps its newline.
	got, gerr := q.GetThought("n1")
	if gerr != nil || !strings.Contains(got.Prompt, "\n") {
		t.Error("render sanitization leaked into the data")
	}
}

// ── transcript replay (NAVI-334) ────────────────────────────────────────

// persistConversationFor mirrors the daemon runner: create a session
// named after the thought and fill it with user/assistant/tool/system
// entries.
func persistConversationFor(t *testing.T, sessM *session.SessionManager, thoughtID string) {
	t.Helper()
	sess, err := sessM.CreateSession("daemon", thoughtID)
	if err != nil {
		t.Fatal(err)
	}
	entry := func(role session.Role, msg string) session.SessionEntry {
		e := session.NewEntry(role, msg)
		return e
	}
	sess.AppendEntry(entry(session.RoleUser, "what is the capital?"))

	// Assistant content entry.
	assistant := entry(session.RoleAssistant, "The capital is Paris.")
	sess.AppendEntry(assistant)
	// Assistant tool-call entry.
	tool := session.NewEntry(session.RoleAssistant, "")
	tool.ToolName = "note_write"
	tool.ToolInput = json.RawMessage(`{"title":"x"}`)
	sess.AppendEntry(tool)
	// Tool result entry.
	sess.AppendEntry(entry(session.RoleTool, "saved note x"))
	// System entry.
	sess.AppendEntry(entry(session.RoleSystem, "compaction ran"))
	if err := sessM.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
}

func TestReplayCompletedThought(t *testing.T) {
	q := newQueue(t)
	sessM := sessionSessions(t)
	tport, _ := newInProcess(t, q, sessM)
	m := NewModel(tport)
	_ = m.Init()

	if err := q.Enqueue(th("done1", 4)); err != nil {
		t.Fatal(err)
	}
	persistConversationFor(t, sessM, "done1")
	drain(m)

	// Hop into the completed thought's live view.
	m.sel = 0
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != ModeLive || m.liveID != "done1" {
		t.Fatal("did not enter live view")
	}
	view := m.View()
	if !strings.Contains(view, "replay") {
		t.Errorf("header should mark the chat as (replay):\n%s", view)
	}
	if !strings.Contains(view, "what is the capital?") ||
		!strings.Contains(view, "The capital is Paris.") ||
		!strings.Contains(view, "note_write") ||
		!strings.Contains(view, "saved note x") {
		t.Errorf("replayed conversation incomplete:\n%s", view)
	}
	// Tool result merged into the tool block (one ⚙ block).
	if strings.Contains(view, "⚙") == false {
		t.Error("tool block missing")
	}
}

func TestLiveViewHeaderMarksLive(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("run1", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m)
	m.sel = 0
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "live") || strings.Contains(m.View(), "replay") {
		t.Errorf("running view should be chat (live):\n%s", m.View())
	}
}

// ── real-time overview (NAVI-335) ───────────────────────────────────────

func TestHomeViewAgentPreview(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("run1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)

	// Deltas stream a preview snippet onto the row in LIST mode.
	observe(broker, "run1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "writing a hello world example"},
	})
	drain(m)
	if m.mode != ModeList {
		t.Fatal("test expects home view")
	}
	if !strings.Contains(m.View(), "writing a hello world") {
		t.Errorf("delta preview missing from the row:\n%s", m.View())
	}
	// Tool activity shows on the row and in the status activity line.
	observe(broker, "run1", agent.AgentEvent{
		Type: agent.AgentEventToolCall,
		Tool: &agent.ToolUpdate{ToolCallID: "c1", Name: "note_write", Input: json.RawMessage(`{"title":"x"}`)},
	})
	drain(m)
	if !strings.Contains(m.View(), "⚙ note_write") || !strings.Contains(m.View(), "run1 ·") {
		t.Errorf("tool preview / activity line missing:\n%s", m.View())
	}
	// Completion clears the activity line.
	if err := q.Complete("run1", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if strings.Contains(m.View(), "run1 ·") && m.active != "" {
		t.Errorf("activity line not cleared after completion:\n%s", m.View())
	}
}

func TestTickingClockWhileRunning(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q, sessionSessions(t))
	m := NewModel(tport)
	_ = m.Init()

	// Idle: the poll cmd yields no message (no busy-loop).
	if msg := m.poll()(); msg != nil {
		t.Error("idle poll produced a message")
	}

	if err := q.Enqueue(th("run2", 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	if !m.anyRunning() {
		t.Fatal("no running row after claim")
	}
	// Running: the poll cmd yields a tick even with zero events.
	msg := m.poll()()
	if msg == nil {
		t.Fatal("poll produced nothing while running")
	}
	if _, ok := msg.(tickMsg); !ok {
		t.Error("expected a tick message while running")
	}
}
