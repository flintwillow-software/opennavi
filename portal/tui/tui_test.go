package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/thought"
)

// allowAllAuth permits every portal action: the shim-attachment test
// seam (production wires nil and fails closed).
type allowAllAuth struct{}

func (a allowAllAuth) Authorize(action portal.Action, identity string) error {
	return nil
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
func newInProcess(t *testing.T, q *thought.Queue) (*inProcessTransport, *portal.Broker) {
	t.Helper()
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })
	tport, err := NewInProcess(portal.Deps{ThoughtSystem: q, Observation: broker, Objects: queueSurface{q: q}})
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	return tport, broker
}

// portalHarness is the minimal public-surface stand-in for the retired
// internal/daemon assembly: a queue, a control portal, and an observation
// broker wired the way the navi composition root wires them. The pool is
// never started, so thoughts stay pending and remote control is
// deterministic (no runner racing the queue).
type portalHarness struct {
	Queue       *thought.Queue
	Control     *portal.Control
	Observation *portal.Broker
	Surface     object.Surface
}

// queueSurface is the minimal object.Surface over a queue: enough for the
// remote transport's snapshot and object reads in tests (the real surface is
// the navi's; a portal test must not import the composition root).
type queueSurface struct{ q *thought.Queue }

func (s queueSurface) Types() []object.Type {
	return []object.Type{{
		Name: "thought", Label: "Thought", ID: "id",
		Fields: []object.Field{
			{Name: "id", Kind: object.KindString, Section: "identity"},
			{Name: "prompt", Label: "Prompt", Kind: object.KindText, Section: "core"},
			{Name: "status", Kind: object.KindEnum, Section: "state"},
			{Name: "priority", Kind: object.KindInt, Section: "state"},
			{Name: "model", Kind: object.KindString, Section: "run"},
			{Name: "foreground", Kind: object.KindBool, Section: "run"},
			{Name: "depends_on", Kind: object.KindList, ItemKind: object.KindRef, Section: "relations"},
			{Name: "verification", Kind: object.KindBool, Section: "run"},
			{Name: "updated_at", Kind: object.KindTime, Section: "timestamps"},
		},
		Details: []object.Detail{{Name: "transcript", Label: "Transcript", Type: "entry", Key: "thought_id"}},
		Actions: []object.Action{
			{Name: "pause"}, {Name: "resume"}, {Name: "cancel"}, {Name: "retry"},
			{Name: "steer", Args: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`)},
			{Name: "bump", Args: json.RawMessage(`{"type":"object","properties":{"priority":{"type":"integer"}}}`)},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Thoughts", Columns: []string{"id", "prompt", "status", "priority", "model", "updated_at"}},
			{Kind: object.ViewDetail, Label: "Thought"},
			{Kind: object.ViewChat, Label: "Conversation", Detail: "transcript"},
		},
	}}
}

func (s queueSurface) List(typeName string, _ object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	out := make([]object.Object, 0, len(s.q.GetThoughts()))
	for _, t := range s.q.GetThoughts() {
		out = append(out, object.Object{Type: "thought", ID: t.ID, Fields: map[string]any{
			"id": t.ID, "prompt": t.Prompt, "context": t.Context, "status": string(t.Status),
			"priority": t.Priority, "model": t.Model, "foreground": t.Foreground,
			"verification": t.IsVerification(),
			"depends_on":   t.DependsOn, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
		}})
	}
	return out, "", nil
}

func (s queueSurface) Get(typeName, id string) (object.Object, error) {
	objs, _, err := s.List(typeName, object.Query{})
	if err != nil {
		return object.Object{}, err
	}
	for _, o := range objs {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (s queueSurface) Detail(string, string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

func (s queueSurface) Act(typeName, id, action string, args map[string]any) error {
	if typeName != "thought" {
		return object.ErrUnknownType
	}
	switch action {
	case "steer":
		msg, _ := args["message"].(string)
		return s.q.RouteMessage(id, msg)
	case "pause":
		return s.q.Pause(id)
	case "resume":
		return s.q.Resume(id)
	case "cancel":
		return s.q.Cancel(id)
	case "retry":
		return s.q.Retry(id)
	case "bump":
		return s.q.BumpPriority(id, intField(args["priority"]))
	default:
		return object.ErrUnknownAction
	}
}

func (s queueSurface) Watch(int) ([]object.Delta, int, bool) { return nil, 0, false }

func newPortalHarness(t *testing.T, auth portal.Authorizer) *portalHarness {
	t.Helper()
	q := newQueue(t)
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })
	control := portal.NewControl(auth)
	if err := control.Start(context.Background(), portal.Deps{ThoughtSystem: q, Observation: broker}); err != nil {
		t.Fatalf("control.Start: %v", err)
	}
	return &portalHarness{Queue: q, Control: control, Observation: broker, Surface: queueSurface{q: q}}
}

// observe publishes an agent event through the broker the way the real
// runner's producer does.
func observe(broker *portal.Broker, thoughtID string, ev agent.AgentEvent) {
	detail := ""
	switch {
	case ev.Tool != nil:
		detail = strings.TrimSpace(ev.Tool.Name + " " + string(ev.Tool.Input))
	case ev.Message != nil:
		detail = strings.TrimSpace(ev.Message.Content)
	case ev.Error != nil:
		detail = ev.Error.Error()
	}
	broker.Publish(portal.Event{
		Category: portal.CategoryAgent, Kind: string(ev.Type),
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
	// The cursor advances inside Update, with the batch (NAVI-380).
	_, _ = m.Update(feedMsg{evs: evs, next: next, lost: lost})
}

// TestDepsTransport is the TUI-as-portal gate: the portal's transport is
// built from Deps alone (thought system + bounded observation), with no
// concrete queue at construction.
func TestDepsTransport(t *testing.T) {
	q := newQueue(t)
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })

	tr, err := NewDepsTransport(portal.Deps{ThoughtSystem: q, Observation: broker, Objects: queueSurface{q: q}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Feed().Close()

	if err := tr.Enqueue("hi", 5, time.Time{}); err != nil {
		t.Fatal(err)
	}
	objs, _, err := tr.Objects().List("thought", object.Query{})
	if err != nil || len(objs) != 1 {
		t.Fatalf("surface list = %v (%v)", objs, err)
	}
	id := objs[0].ID

	// The lifecycle event arrived on the Deps-provided subscription.
	evs, _, _ := tr.Feed().ReadAfter(0)
	sawEnqueued := false
	for _, ev := range evs {
		if ev.Kind == "enqueued" && ev.ThoughtID == id {
			sawEnqueued = true
		}
	}
	if !sawEnqueued {
		t.Fatal("enqueued event not observed through Deps")
	}

	// Control goes through the thought-system handle.
	if err := tr.Steer(id, "steered"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Bump(id, 2); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetThought(id)
	if err != nil || !strings.Contains(got.Context, "steered") {
		t.Fatalf("steer did not land: %+v (%v)", got, err)
	}
	if got.Priority != 2 {
		t.Fatalf("bump did not land: priority %d", got.Priority)
	}
}

func TestModelSeedsAndTracksLifecycle(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
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

// TestFeedMsgAdvancesCursorInUpdate pins NAVI-380: the feed cursor is
// advanced on the Update goroutine, with the batch it describes, so an
// applied batch is never re-read.
func TestFeedMsgAdvancesCursorInUpdate(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)

	updated, _ := m.Update(feedMsg{
		evs:  []portal.Event{{Category: portal.CategoryQueue, Kind: "enqueued", ThoughtID: "t1"}},
		next: 7,
	})
	mm, ok := updated.(*tuiModel)
	if !ok {
		t.Fatalf("Update returned %T, want *tuiModel", updated)
	}
	if mm.cursor != 7 {
		t.Fatalf("cursor = %d, want 7", mm.cursor)
	}
}

func TestModelAckDisplay(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
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
	tport, broker := newInProcess(t, q)
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
	tport, _ := newInProcess(t, q)
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
	tport, _ := newInProcess(t, q)

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
	// One KeyMsg per rune — the shape a real terminal produces. Batching a
	// whole string into one KeyMsg made String() return the whole string,
	// which hid command-key interception in input mode.
	for _, r := range s {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
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
	tport, _ := newInProcess(t, q)
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
	tport, _ := newInProcess(t, q)
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
	rt := newPortalHarness(t, allowAllAuth{})

	// Serve the portal WITHOUT starting the runtime's pool: the
	// thought stays pending, so remote control is deterministic (no
	// runner racing the queue).
	ln, lerr := net.Listen("tcp", addr)
	if lerr != nil {
		t.Fatal(lerr)
	}
	srv := &http.Server{Addr: addr, Handler: gateway.Handler(rt.Control, rt.Observation, rt.Surface), ReadHeaderTimeout: 5 * time.Second}
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

	// The remote transport reads pre-existing thoughts through the object
	// surface (ADR 0017): the queue is visible to a remote front-end instead
	// of only converging from events, which is what replaced the old
	// empty-snapshot path.
	deadline = time.Now().Add(5 * time.Second)
	for {
		objs, _, serr := rport.Objects().List("thought", object.Query{})
		if serr != nil {
			t.Fatalf("remote surface list: %v", serr)
		}
		if len(objs) >= 2 {
			var sawSteered bool
			for _, o := range objs {
				if o.ID != "r1" {
					continue
				}
				if ctx, _ := o.Fields["context"].(string); strings.Contains(ctx, "from remote") {
					sawSteered = true
				}
			}
			if sawSteered {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote surface never listed the queue: %+v", objs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Control against a NO-authorizer shim fails closed (403 →
	// error): same seamlnd, no Runtime.Run involved.
	addr2 := portalFreePort(t)
	rt2 := newPortalHarness(t, nil)
	ln2, lerr2 := net.Listen("tcp", addr2)
	if lerr2 != nil {
		t.Fatal(lerr2)
	}
	srv2 := &http.Server{Addr: addr2, Handler: gateway.Handler(rt2.Control, rt2.Observation, rt2.Surface), ReadHeaderTimeout: 5 * time.Second}
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
	tport, _ := newInProcess(t, q)
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
	tport, _ := newInProcess(t, q)
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
	tport, _ := newInProcess(t, q)
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

func TestLiveViewHeaderMarksLive(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
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
	tport, broker := newInProcess(t, q)
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

// TestPollIsSingleFlightAndIdleFree pins the NAVI-380 follow-up: every
// Update re-arms the drain, so the guard must make re-arming idempotent
// (two in-flight timers read the same non-destructive mailbox range and
// each apply it), while an idle read still stops the drain instead of
// busy-looping — and a running thought keeps it alive so ages tick.
func TestPollIsSingleFlightAndIdleFree(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init() // arms the drain

	// Armed: re-arming from a key press must be a no-op.
	if cmd := m.poll(); cmd != nil {
		t.Fatal("poll re-armed while a drain is already in flight")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}); cmd != nil {
		t.Fatal("key press stacked a second drain")
	}

	// Idle with no events: the drain stops until the next user input.
	if _, cmd := m.Update(feedMsg{}); cmd != nil {
		t.Error("idle drain re-armed — busy loop")
	}
	if m.polling {
		t.Error("idle drain left the poll armed")
	}

	// Running: an eventless read keeps the drain alive (ticking clocks).
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
	if _, cmd := m.Update(feedMsg{}); cmd == nil {
		t.Fatal("drain stopped while a thought runs")
	}
}

// TestDuplicateBatchDoesNotReplayTurns is the observed NAVI-380 follow-up
// bug: the mailbox read is non-destructive (a bounded replay ring), so
// two drains that captured the same cursor applied the same batch twice —
// the chat showed ALPHA, then the run's system lines, then ALPHA again,
// once per in-flight timer. A duplicate delivery must not re-append.
func TestDuplicateBatchDoesNotReplayTurns(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	if err := q.Enqueue(th("t1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	m.enterLive("t1")

	// One realistic batch: the assistant's content delta followed by the
	// run's end marker (the shape the screenshot showed repeated).
	broker.Publish(portal.Event{Category: portal.CategoryAgent, Kind: "content_delta", ThoughtID: "t1", Detail: "ALPHA"})
	broker.Publish(portal.Event{Category: portal.CategoryAgent, Kind: "message_end", ThoughtID: "t1"})

	evs, next, lost := m.feed.ReadAfter(m.cursor)
	if len(evs) != 2 {
		t.Fatalf("feed batch = %d events, want 2", len(evs))
	}
	// First delivery applies the delta and the end marker.
	_, _ = m.Update(feedMsg{evs: evs, next: next, lost: lost})
	// A second drain that captured the same cursor (the stacked-timer bug)
	// delivers the identical range; it must be ignored, not re-applied.
	_, _ = m.Update(feedMsg{evs: evs, next: next, lost: lost})

	assistant, ends := 0, 0
	for _, turn := range m.transcripts["t1"] {
		switch {
		case turn.role == TurnAssistant && turn.text == "ALPHA":
			assistant++
		case turn.role == TurnSystem && turn.text == "message_end":
			ends++
		}
	}
	if assistant != 1 || ends != 1 {
		t.Fatalf("assistant turns = %d, end markers = %d, want 1 and 1 (batch replayed)", assistant, ends)
	}
	if got := strings.Count(m.View(), "ALPHA"); got != 1 {
		t.Fatalf("ALPHA rendered %d times, want 1", got)
	}
	if m.cursor != next {
		t.Fatalf("cursor = %d, want %d (a duplicate must not move it)", m.cursor, next)
	}
}

// TestEnqueueInputAcceptsCommandLetters is the regression for command keys
// being swallowed while typing: a prompt containing q/n/a/p/s/r/c/l/j/k
// must survive to the enqueued thought, and q must not quit mid-message.
func TestEnqueueInputAcceptsCommandLetters(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	keyRunes(t, m, "n") // open the enqueue input
	if m.mode != ModeInput {
		t.Fatalf("mode = %q, want input", m.mode)
	}

	const prompt = "please cancel my paused quick run"
	keyRunes(t, m, prompt)
	if m.input != prompt {
		t.Fatalf("input = %q, want %q (keys dropped)", m.input, prompt)
	}
	if m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}) {
		t.Fatal("q while typing must not quit")
	}
	if m.input != prompt+"q" {
		t.Fatalf("q was not inserted: %q", m.input)
	}
	m.input = prompt

	keyEnter(m) // prompt
	keyEnter(m) // default priority
	keyEnter(m) // no deferral
	found := false
	for _, th := range q.GetThoughts() {
		if th.Prompt == prompt {
			found = true
		}
	}
	if !found {
		t.Fatalf("prompt %q was not enqueued intact", prompt)
	}
}

// TestCompletedThoughtResponseVisible is the regression for a response
// that "isn't in the TUI": a thought enqueued from the list view
// accumulates its agent events, so opening it after completion shows the
// answer (in-session; durable replay is NAVI-353).
func TestCompletedThoughtResponseVisible(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	if err := q.Enqueue(th("late", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m) // the row lands in the list

	// The agent answers while the user is still in the list view.
	observe(broker, "late", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "the answer is 42"},
	})
	drain(m)
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	if err := q.Complete("late", "the answer is 42"); err != nil {
		t.Fatal(err)
	}
	drain(m)

	// Open the completed thought; prompt + response must both be there.
	m.sel = 0
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != ModeLive || m.liveID != "late" {
		t.Fatalf("did not enter live (mode=%q id=%q)", m.mode, m.liveID)
	}
	view := m.View()
	if !strings.Contains(view, "the answer is 42") {
		t.Fatalf("response missing from live view:\n%s", view)
	}
	if !strings.Contains(view, "task late") {
		t.Fatalf("prompt missing from live view:\n%s", view)
	}
}

// TestModelShowsBlockedDependency is NAVI-378's portal gate: a pending
// thought whose dependency has not completed reads "blocked", and the row
// clears the moment the dependency completes.
func TestModelShowsBlockedDependency(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	if err := q.Enqueue(th("t1", 4)); err != nil {
		t.Fatal(err)
	}
	second := th("t2", 4)
	second.DependsOn = []string{"t1"}
	if err := q.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if !strings.Contains(m.View(), "blocked") {
		t.Errorf("dependent not shown as blocked:\n%s", m.View())
	}

	// Running the dependency does not release the dependent.
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	if !strings.Contains(m.View(), "blocked") {
		t.Errorf("dependent released while its dependency only runs:\n%s", m.View())
	}

	// Completing it does.
	if err := q.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if strings.Contains(m.View(), "blocked") {
		t.Errorf("dependent still blocked after its dependency completed:\n%s", m.View())
	}
}

// TestModelShowsVerificationFlag is ADR 0016's portal gate: a plan's
// closing check is visible as a check in the queue view, not just in its
// record metadata.
func TestModelShowsVerificationFlag(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	check := th("verify-abcd1234", 5)
	check.Metadata = map[string]string{thought.MetadataRole: thought.RoleVerification}
	if err := q.Enqueue(check); err != nil {
		t.Fatal(err)
	}
	drain(m)

	obj, ok := m.items["verify-abcd1234"]
	if !ok {
		t.Fatalf("verification row missing:\n%s", m.View())
	}
	if v, _ := obj.Fields["verification"].(bool); !v {
		t.Fatal("verification flag not derived from the record metadata")
	}
	if !strings.Contains(m.View(), "V ") {
		t.Errorf("verification flag not rendered:\n%s", m.View())
	}
}

// ── descriptor-driven views (NAVI-384) ───────────────────────────────────

// multiSurface exposes a thought type (with a chat view) and a todo type
// (detail only), so the type switcher, the detail screen, and generic action
// dispatch are exercised without a navi.
type multiSurface struct {
	q          *thought.Queue
	todos      []object.Object
	transcript []object.Object
	mu         sync.Mutex
	acted      []string
}

func (s *multiSurface) Types() []object.Type {
	return []object.Type{
		{
			Name: "thought", Label: "Thought", ID: "id",
			Fields: []object.Field{
				{Name: "id", Kind: object.KindString, Section: "identity"},
				{Name: "prompt", Label: "Prompt", Kind: object.KindText, Section: "core"},
				{Name: "status", Kind: object.KindEnum, Section: "state"},
			},
			Details: []object.Detail{{Name: "transcript", Type: "entry", Key: "thought_id"}},
			Actions: []object.Action{{Name: "pause", Label: "Pause"}, {Name: "bump", Label: "Bump"}},
			Views: []object.View{
				{Kind: object.ViewList, Columns: []string{"id", "prompt", "status"}},
				{Kind: object.ViewChat, Label: "Conversation", Detail: "transcript"},
			},
		},
		{
			Name: "todo", Label: "Todo", ID: "id",
			Fields: []object.Field{
				{Name: "id", Kind: object.KindString, Section: "identity"},
				{Name: "text", Label: "Text", Kind: object.KindText, Section: "core"},
				{Name: "status", Kind: object.KindEnum, Section: "state"},
			},
			Actions: []object.Action{{Name: "complete", Label: "Complete", Confirm: true}},
			Views: []object.View{
				{Kind: object.ViewList, Columns: []string{"text", "status"}},
				{Kind: object.ViewDetail, Sections: []object.Section{{Name: "core", Label: "Task"}}},
			},
		},
	}
}

func (s *multiSurface) List(typeName string, _ object.Query) ([]object.Object, string, error) {
	switch typeName {
	case "thought":
		objs := []object.Object{}
		for _, t := range s.q.GetThoughts() {
			objs = append(objs, object.Object{Type: "thought", ID: t.ID, Fields: map[string]any{
				"id": t.ID, "prompt": t.Prompt, "status": string(t.Status),
			}})
		}
		return objs, "", nil
	case "todo":
		return s.todos, "", nil
	}
	return nil, "", object.ErrUnknownType
}

func (s *multiSurface) Get(typeName, id string) (object.Object, error) {
	objs, _, err := s.List(typeName, object.Query{})
	if err != nil {
		return object.Object{}, err
	}
	for _, o := range objs {
		if o.ID == id {
			return o, nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (s *multiSurface) Detail(typeName, id, detail string, _ object.Query) ([]object.Object, string, error) {
	if typeName != "thought" {
		return nil, "", object.ErrUnknownType
	}
	if detail != "transcript" {
		return nil, "", object.ErrUnknownDetail
	}
	return s.transcript, "", nil
}

func (s *multiSurface) Act(typeName, id, action string, _ map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acted = append(s.acted, typeName+"/"+id+"/"+action)
	return nil
}

func (s *multiSurface) Watch(int) ([]object.Delta, int, bool) { return nil, 0, false }

// newMultiModel builds a model whose transport serves s.
func newMultiModel(t *testing.T, q *thought.Queue, s object.Surface) *tuiModel {
	t.Helper()
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })
	tr, err := NewInProcess(portal.Deps{ThoughtSystem: q, Observation: broker, Objects: s})
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(tr)
	_ = m.Init()
	return m
}

// TestModelSwitchesTypesAndOpensDetail is the toolkit's list/detail gate: the
// list renders whichever type is selected from that type's declared columns,
// and a type without a chat view opens its sectioned detail.
func TestModelSwitchesTypesAndOpensDetail(t *testing.T) {
	q := newQueue(t)
	s := &multiSurface{q: q, todos: []object.Object{{Type: "todo", ID: "td1", Fields: map[string]any{
		"id": "td1", "text": "ship the toolkit", "status": "pending"}}}}
	m := newMultiModel(t, q, s)

	if err := q.Enqueue(th("t1", 4)); err != nil {
		t.Fatal(err)
	}
	m.refreshList()
	if !strings.Contains(m.View(), "task t1") {
		t.Fatalf("thought list missing:\n%s", m.View())
	}

	// `t` selects the next exposed type and renders ITS declared columns.
	keyRunes(t, m, "t")
	if m.typeIdx != 1 {
		t.Fatalf("typeIdx = %d, want 1", m.typeIdx)
	}
	if !strings.Contains(m.View(), "ship the toolkit") {
		t.Fatalf("todo list missing:\n%s", m.View())
	}
	if strings.Contains(m.View(), "task t1") {
		t.Fatalf("the todo list still renders thoughts:\n%s", m.View())
	}

	// enter opens the sectioned detail for a type with no chat view, with the
	// actions its descriptor grants.
	keyEnter(m)
	if m.mode != ModeDetail || m.detail == nil {
		t.Fatalf("mode = %q, want detail", m.mode)
	}
	detail := m.View()
	for _, want := range []string{"ship the toolkit", "Task", "complete!"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail view missing %q:\n%s", want, detail)
		}
	}

	// The back key returns to the list.
	keyRunes(t, m, "l")
	if m.mode != ModeList {
		t.Fatalf("mode = %q, want list", m.mode)
	}
}

// TestModelOpensChatFromTheTranscript is the acceptance gate: opening a
// thought renders its persisted conversation through the surface, so a
// completed thought opened after the fact shows what happened.
func TestModelOpensChatFromTheTranscript(t *testing.T) {
	q := newQueue(t)
	s := &multiSurface{q: q, transcript: []object.Object{
		{Type: "entry", ID: "e1", Fields: map[string]any{"role": "user", "message": "what is the answer"}},
		{Type: "entry", ID: "e2", Fields: map[string]any{"role": "assistant", "message": "the answer is 42"}},
		{Type: "entry", ID: "e3", Fields: map[string]any{"role": "assistant", "tool_name": "calc", "tool_input": `{"expr":"6*7"}`}},
		{Type: "entry", ID: "e4", Fields: map[string]any{"role": "tool", "tool_name": "calc", "message": "42"}},
	}}
	m := newMultiModel(t, q, s)
	if err := q.Enqueue(th("late", 4)); err != nil {
		t.Fatal(err)
	}
	m.refreshList()
	m.sel = 0

	keyEnter(m)
	if m.mode != ModeLive || m.liveID != "late" {
		t.Fatalf("mode = %q id = %q, want the chat view", m.mode, m.liveID)
	}
	view := m.View()
	for _, want := range []string{"what is the answer", "the answer is 42", "calc", `{"expr":"6*7"}`, "42"} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q:\n%s", want, view)
		}
	}
}

// TestModelDispatchesDeclaredActions is the toolkit's action gate: an action
// key reaches the surface as a declared action, not a hardcoded verb.
func TestModelDispatchesDeclaredActions(t *testing.T) {
	q := newQueue(t)
	s := &multiSurface{q: q}
	m := newMultiModel(t, q, s)
	if err := q.Enqueue(th("t1", 4)); err != nil {
		t.Fatal(err)
	}
	m.refreshList()
	m.sel = 0

	keyRunes(t, m, "s") // pause
	keyRunes(t, m, "p") // bump (carries its declared priority argument)

	s.mu.Lock()
	acted := append([]string(nil), s.acted...)
	s.mu.Unlock()
	if len(acted) != 2 || acted[0] != "thought/t1/pause" || acted[1] != "thought/t1/bump" {
		t.Fatalf("surface saw %v, want thought/t1/pause then thought/t1/bump", acted)
	}
}
