package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/thought"
)

// allowAuth is an authorizer that permits every action.
type allowAuth struct{}

func (a allowAuth) Authorize(action PortalAction, identity string) error {
	return nil
}

// denyAuth refuses every action with a named error.
type denyAuth struct{}

func (a denyAuth) Authorize(action PortalAction, identity string) error {
	return errors.New("denied by test authorizer")
}

// newQueue builds a queue over an in-memory store for contract tests.
func newQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 4, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func thoughtT(id string) *thought.Thought {
	return &thought.Thought{ID: id, Prompt: "task " + id, Priority: 4, Status: thought.ThoughtPending}
}

func readAll(m *PortalMailbox) []PortalEvent {
	evs, _, _ := m.ReadAfter(0)
	return evs
}

// ── Mailbox ──────────────────────────────────────────────────────────────

func TestMailboxOrderAndCursor(t *testing.T) {
	m := NewPortalMailbox(4)
	for i := 0; i < 5; i++ {
		m.Push(PortalEvent{Category: PortalQueue, Kind: fmt.Sprintf("e%d", i), At: time.Now().UTC()})
	}

	// Bounded: the oldest event was dropped.
	evs, next, lost := m.ReadAfter(0)
	if len(evs) != 4 || evs[0].Seq != 2 || evs[3].Seq != 5 {
		t.Errorf("ring wrong: %+v", evs)
	}
	if next != 6 || !lost {
		t.Errorf("cursor=%d lost=%v, want 6/true", next, lost)
	}

	// Resume from the returned cursor: nothing duplicated, nothing
	// lost (the cursor is the smallest unseen sequence).
	evs, next, lost = m.ReadAfter(6)
	if len(evs) != 0 || next != 6 || lost {
		t.Errorf("after-cursor read: %+v next=%d lost=%v", evs, next, lost)
	}

	// A cursor below the truthful one re-delivers from there (the
	// consumer asks for a sequence it already saw).
	m.Push(PortalEvent{Category: PortalAck, Kind: "message_routed", Delivery: "steering", At: time.Now().UTC()})
	evs, next, _ = m.ReadAfter(6)
	if len(evs) != 1 || evs[0].Seq != 6 || evs[0].Delivery != "steering" {
		t.Errorf("middle read: %+v", evs)
	}
	if next != 7 {
		t.Errorf("next = %d, want 7", next)
	}
}

func TestMailboxClosedDrops(t *testing.T) {
	m := NewPortalMailbox(8)
	m.Close()
	m.Push(PortalEvent{Category: PortalQueue, Kind: "started", At: time.Now().UTC()})
	evs, _, _ := m.ReadAfter(0)
	if len(evs) != 0 {
		t.Error("closed mailbox accepted events")
	}
}

// ── Queue observation + delivery acks ────────────────────────────────────

func TestPortalFansQueueLifecycle(t *testing.T) {
	q := newQueue(t)
	p := NewPortal(q, allowAuth{})
	mb := p.Subscribe(32)

	if err := q.Enqueue(thoughtT("t1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no thought claimable")
	}

	evs := readAll(mb)
	kinds := make([]string, 0, len(evs))
	seenStarted := false
	for _, ev := range evs {
		if ev.Category != PortalQueue || ev.ThoughtID != "t1" {
			t.Errorf("unexpected event: %+v", ev)
		}
		kinds = append(kinds, ev.Kind)
		if ev.Kind == "started" {
			seenStarted = true
		}
	}
	if !seenStarted {
		t.Errorf("started lifecycle event missing; got %v", kinds)
	}
	// enqueued fires before started in fan order.
	if len(kinds) < 2 || kinds[0] != "enqueued" {
		t.Errorf("fan order wrong: %v", kinds)
	}
}

func TestPortalFansDeliveryAck(t *testing.T) {
	q := newQueue(t)
	p := NewPortal(q, allowAuth{})
	mb := p.Subscribe(32)

	if err := q.Enqueue(thoughtT("t2")); err != nil {
		t.Fatal(err)
	}
	// RouteMessage on a pending thought takes the context-append path
	// (S010312) and emits the delivery-ack event.
	path, err := q.RouteMessageWithPath("t2", "hello")
	if err != nil {
		t.Fatalf("RouteMessage: %v", err)
	}
	if path != thought.PathContextAppend {
		t.Errorf("path = %q, want context-append", path)
	}

	var sawAck bool
	for _, ev := range readAll(mb) {
		if ev.Category == PortalAck && ev.ThoughtID == "t2" && ev.Delivery == "context-append" {
			sawAck = true
		}
	}
	if !sawAck {
		t.Error("delivery-ack event not streamed")
	}
}

// ── Per-thought agent events ─────────────────────────────────────────────

func TestObserveAgentFansToSubscribers(t *testing.T) {
	q := newQueue(t)
	p := NewPortal(q, allowAuth{})
	mb := p.Subscribe(32)

	p.ObserveAgent("t9", agent.AgentEvent{
		Type: agent.AgentEventToolCall,
		Tool: &agent.ToolUpdate{ToolCallID: "call-1", Name: "note_write", Input: json.RawMessage(`{"title":"x"}`)},
	})
	p.ObserveAgent("t9", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "delta one"},
	})

	evs := readAll(mb)
	if len(evs) != 2 {
		t.Fatalf("got %d agent events, want 2", len(evs))
	}
	if evs[0].Category != PortalAgent || evs[0].ThoughtID != "t9" || evs[0].Kind != "tool_call" {
		t.Errorf("tool event wrong: %+v", evs[0])
	}
	if !strings.Contains(evs[0].Detail, "note_write") {
		t.Errorf("tool detail missing name: %q", evs[0].Detail)
	}
	if evs[1].Kind != "content_delta" || evs[1].Detail != "delta one" {
		t.Errorf("delta event wrong: %+v", evs[1])
	}
}

// ── Control channel ──────────────────────────────────────────────────────

func TestControlActionsApplyAndEmit(t *testing.T) {
	q := newQueue(t)
	p := NewPortal(q, allowAuth{})
	mb := p.Subscribe(64)

	if err := p.Enqueue("u1", thoughtT("pending-1")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Steer routes to a pending thought (context-append path).
	if err := p.Steer("u1", "pending-1", "hello"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	got, err := q.GetThought("pending-1")
	if err != nil || !strings.Contains(got.Context, "hello") {
		t.Errorf("steered message not appended: %+v %v", got, err)
	}

	// Pause/resume a running thought; events come back on the stream.
	if err := p.Enqueue("u1", &thought.Thought{ID: "run-1", Prompt: "run me", Priority: 1, Status: thought.ThoughtPending}); err != nil {
		t.Fatalf("Enqueue run: %v", err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no thought claimable for pause test")
	}
	if err := p.Pause("u1", "run-1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := p.Resume("u1", "run-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := p.Cancel("u1", "pending-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	var sawPaused, sawResumed, sawCancelled, sawEnqueued bool
	for _, ev := range readAll(mb) {
		switch ev.Kind {
		case "enqueued":
			sawEnqueued = true
		case "paused":
			sawPaused = true
		case "resumed":
			sawResumed = true
		case "cancelled":
			sawCancelled = true
		}
	}
	if !sawEnqueued || !sawPaused || !sawResumed || !sawCancelled {
		t.Errorf("control events missing (enq=%v pause=%v resume=%v cancel=%v)", sawEnqueued, sawPaused, sawResumed, sawCancelled)
	}
}

func TestUnauthorizedControlRefused(t *testing.T) {
	q := newQueue(t)

	// No authorizer: every action fails closed (S040200).
	p := NewPortal(q, nil)
	if err := p.Attach("u"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("attach: %v, want ErrPortalUnauthorized", err)
	}
	if err := p.Steer("u", "x", "hi"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("steer: %v, want ErrPortalUnauthorized", err)
	}
	if err := p.Pause("u", "x"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("pause: %v, want ErrPortalUnauthorized", err)
	}
	if err := p.Resume("u", "x"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("resume: %v, want ErrPortalUnauthorized", err)
	}
	if err := p.Cancel("u", "x"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("cancel: %v, want ErrPortalUnauthorized", err)
	}
	if err := p.Enqueue("u", thoughtT("x")); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("enqueue: %v, want ErrPortalUnauthorized", err)
	}

	// A wired authorizer that refuses is equally enforced.
	denyPortal := NewPortal(q, denyAuth{})
	if err := denyPortal.Steer("u", "x", "hi"); err == nil || errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("deny-auth steer: %v, want a refusal", err)
	}
}

func TestAttach(t *testing.T) {
	p := NewPortal(newQueue(t), allowAuth{})
	if err := p.Attach("tui"); err != nil {
		t.Fatalf("attach with allow authorizer: %v", err)
	}
	closed := NewPortal(newQueue(t), nil)
	if err := closed.Attach("tui"); !errors.Is(err, ErrPortalUnauthorized) {
		t.Errorf("attach without authorizer: %v, want unauthorized", err)
	}
}

func TestUnsubscribeStopsFan(t *testing.T) {
	q := newQueue(t)
	p := NewPortal(q, allowAuth{})
	mb := p.Subscribe(16)
	p.Unsubscribe(mb)
	if err := q.Enqueue(thoughtT("t1")); err != nil {
		t.Fatal(err)
	}
	if len(readAll(mb)) != 0 {
		t.Error("unsubscribed mailbox still received events")
	}
}
