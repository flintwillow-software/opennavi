// Package interfaces implements the interface set abstraction (spec
// 00-navi-core/04-interface-set.md): interfaces are delivery modes
// (CLI, TUI, Gateway, ...) that a navi binary can enable in any
// combination. This package defines the contract and selection
// machinery; concrete adapters register themselves by name.
//
// Build-time pruning (compiling only selected interfaces into the
// binary) lands with the builder/compiler (#140); until then selection
// is runtime, over constructors registered at init time. The
// Interface/registry seam is designed so the builder can drive it
// without changes.
//
// The portal observer contract (S000407) — observation + control for
// a navi's thought process, transport-agnostic — lives at the bottom
// of this file.
package interfaces

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// InterfaceConfig is the generic per-interface configuration block
// (spec §01). Concrete adapters extend it via their own option structs;
// the spec's per-interface fields (ports, tokens, themes) live there.
type InterfaceConfig struct {
	Enabled bool
	Port    int
	Addr    string
}

// InterfaceSet mirrors the navi definition's interface selection (spec
// §00/§02): any combination of delivery modes can be enabled.
type InterfaceSet struct {
	CLI     bool            // interactive terminal chat (REPL for now, TUI later)
	WebUI   InterfaceConfig // HTTP + WebSocket chat
	Gateway InterfaceConfig // webhook daemon
	Cron    InterfaceConfig // scheduled tasks
	// Platform adapters (Telegram/Discord/Slack) are follow-ups on the
	// Gateway contract; they register with the same registry.
	Extra map[string]bool
}

// Enabled reports whether the named interface is enabled in the set.
func (s InterfaceSet) Enabled(name string) bool {
	switch name {
	case "cli":
		return s.CLI
	case "webui":
		return s.WebUI.Enabled
	case "gateway":
		return s.Gateway.Enabled
	case "cron":
		return s.Cron.Enabled
	default:
		return s.Extra[name]
	}
}

// ConfigFor returns the generic config block for the named interface
// (empty for interfaces without per-interface config).
func (s InterfaceSet) ConfigFor(name string) InterfaceConfig {
	switch name {
	case "webui":
		return s.WebUI
	case "gateway":
		return s.Gateway
	case "cron":
		return s.Cron
	default:
		return InterfaceConfig{}
	}
}

// ── Portal observation + control (S000407) ──────────────────────────────

// ErrPortalUnauthorized is returned when an attach/control action is
// refused: either no authorizer is wired (fail-closed — the default,
// S040200) or the caller's identity is not permitted. Unauthorized
// control attempts are NEVER defaulted open.
var ErrPortalUnauthorized = errors.New("portal: unauthorized")

// PortalCategory buckets portal events by source.
type PortalCategory string

const (
	// PortalQueue is a ThoughtQueue lifecycle transition
	// (enqueued/started/paused/resumed/completed/failed/cancelled/
	// preempted/persist_failed).
	PortalQueue PortalCategory = "queue"
	// PortalAck is an EventMessageRouted delivery ack (S010312),
	// carrying the routing path in Delivery.
	PortalAck PortalCategory = "ack"
	// PortalAgent is a per-thought agent event (content deltas, tool
	// calls, lifecycle transitions) from the running agent's event
	// bus.
	PortalAgent PortalCategory = "agent"
)

// PortalEvent is one normalized observation on a portal stream.
// Transport-agnostic by contract: the contract defines WHAT is
// streamed; framing belongs to the wire protocol (05-messaging, not
// landed). Seq is the per-mailbox cursor used by consumers to resume
// without gaps.
type PortalEvent struct {
	Seq       int
	Category  PortalCategory
	Kind      string // source event kind ("started", "content_delta", ...)
	ThoughtID string
	Detail    string // human-short summary (truncated; never raw credentials)
	Delivery  string // routing path for acks (S010312); empty otherwise
	At        time.Time
}

// PortalAction enumerates portal control operations for
// authorization (S040200).
type PortalAction string

const (
	PortalActAttach  PortalAction = "attach"
	PortalActSteer   PortalAction = "steer"
	PortalActPause   PortalAction = "pause"
	PortalActResume  PortalAction = "resume"
	PortalActCancel  PortalAction = "cancel"
	PortalActEnqueue PortalAction = "enqueue"
	// TUI per-thought actions (NAVI-319): bump priority / retry a
	// failed thought. The queue emits no event for these (scope: the
	// TUI surfaces the operation result), so the portal returns them
	// directly; authorization is applied like every other control.
	PortalActBump  PortalAction = "bump"
	PortalActRetry PortalAction = "retry"
)

// PortalAuthorizer governs attach + control (S040200). Authorizers are
// injected by the wiring layer; NIL fails closed — every action is
// refused — the same fail-closed pattern as the http tool's nil
// AuthResolver (S100400). IAM lands later; this is the stub surface
// until then.
type PortalAuthorizer interface {
	// Authorize returns nil when identity may perform action, or a
	// refusal error naming the action.
	Authorize(action PortalAction, identity string) error
}

// PortalMailbox is one subscriber's bounded event mailbox. Producers
// (the queue/agent fanouts) Push under the portal lock; the consumer
// drains via ReadAfter. Bounded with drop-oldest, so one stuck or
// slow consumer never blocks the fanout or starves later observation
// (the same slow-consumer discipline the metrics collector and agent
// EventBus follow, #280).
type PortalMailbox struct {
	mu     sync.Mutex
	ring   []PortalEvent // oldest first, capped at cap
	cap    int
	closed bool
	seq    int // next Seq to assign (1-based on first push)
}

// NewPortalMailbox builds a bounded mailbox.
func NewPortalMailbox(cap int) *PortalMailbox {
	if cap <= 0 {
		cap = 512
	}
	return &PortalMailbox{cap: cap}
}

// Push appends an event, assigning its per-mailbox sequence. When the
// mailbox is full the OLDEST event is dropped (bounded memory, slow-
// consumer safety); a closed mailbox drops everything.
func (m *PortalMailbox) Push(ev PortalEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.seq++
	ev.Seq = m.seq
	m.ring = append(m.ring, ev)
	if len(m.ring) > m.cap {
		m.ring = m.ring[1:]
	}
}

// ReadAfter returns the events after the given cursor plus the next
// cursor to use, and whether events before the cursor were dropped
// (lost=true: the consumer fell behind the bounded window and must
// expect a gap).
func (m *PortalMailbox) ReadAfter(after int) ([]PortalEvent, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ring) == 0 {
		return []PortalEvent{}, after, false
	}
	first := m.ring[0].Seq
	// The cursor precedes the bounded window: events were dropped
	// while the consumer was away (slow-consumer gap).
	lost := after < first-1
	// ReadAfter returns the events from the cursor onward: the cursor
	// is the smallest sequence the consumer has NOT received yet, so
	// events landing between polls are never skipped (seq >= after).
	// `next` is the cursor for the next poll (last delivered seq + 1).
	start := len(m.ring)
	for i, ev := range m.ring {
		if ev.Seq >= after {
			start = i
			break
		}
	}
	out := append([]PortalEvent{}, m.ring[start:]...)
	if len(out) > 0 {
		return out, out[len(out)-1].Seq + 1, lost
	}
	return out, after, lost
}

// Close stops the mailbox from accepting new events; already-queued
// events remain readable until drained.
func (m *PortalMailbox) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// Portal is the observation + control surface for one navi (S000407),
// transport-agnostic: the same consumption/control pattern serves the
// TUI in-process (binary run) or a remote gateway attachment. Queue
// lifecycle events (and their delivery acks) fan from the queue's
// OnEvent stream; per-thought agent events arrive via the runner
// hook (ObserveAgent). Control actions arc through the public queue
// API, so they emit the corresponding events back on the same stream.
type Portal struct {
	queue *thought.ThoughtQueue
	auth  PortalAuthorizer
	mu    sync.Mutex
	subs  []*PortalMailbox
}

// NewPortal builds the portal over the navi's queue and subscribes it
// to the queue's OnEvent fanout. auth is the authorizer (nil fails
// closed; S040200).
func NewPortal(q *thought.ThoughtQueue, auth PortalAuthorizer) *Portal {
	p := &Portal{queue: q, auth: auth}
	if q != nil {
		q.OnEvent(func(ev thought.QueueEvent) {
			p.fan(PortalEvent{
				Category:  queueCategory(ev.Kind),
				Kind:      string(ev.Kind),
				ThoughtID: ev.ThoughtID,
				Detail:    ev.Detail,
				Delivery:  ev.Delivery,
				At:        ev.At,
			})
		})
	}
	return p
}

// queueCategory buckets a queue event: delivery acks (S010312) are
// PortalAck, everything else is a lifecycle PortalQueue event.
func queueCategory(kind thought.QueueEventKind) PortalCategory {
	if kind == thought.EventMessageRouted {
		return PortalAck
	}
	return PortalQueue
}

// Subscribe registers a mailbox and returns it to the caller; the
// caller owns draining it (the serve path drains it per client).
func (p *Portal) Subscribe(cap int) *PortalMailbox {
	m := NewPortalMailbox(cap)
	p.mu.Lock()
	p.subs = append(p.subs, m)
	p.mu.Unlock()
	return m
}

// Unsubscribe removes a mailbox from the fanout and closes it.
func (p *Portal) Unsubscribe(m *PortalMailbox) {
	p.mu.Lock()
	for i, s := range p.subs {
		if s == m {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	m.Close()
}

// fan appends an event to every subscribed mailbox. Cheap by design
// (lock + bounded append): called from the queue's event fanout,
// which several emits run under q.mu — it must never re-enter the
// queue (the #280 discipline; all queue control goes through the
// public queue API from the control methods below).
func (p *Portal) fan(ev PortalEvent) {
	p.mu.Lock()
	for _, m := range p.subs {
		m.Push(ev)
	}
	p.mu.Unlock()
}

// agentDetail summarizes an agent event for the stream (bounded
// length; never raw credentials).
func agentDetail(ev agent.AgentEvent) string {
	if ev.Tool != nil {
		snippet := string(ev.Tool.Input)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		return strings.TrimSpace(ev.Tool.Name + " " + snippet)
	}
	if ev.Message != nil {
		c := strings.TrimSpace(ev.Message.Content)
		if len(c) > 256 {
			c = c[:256] + "…"
		}
		return c
	}
	if ev.Error != nil {
		return ev.Error.Error()
	}
	return ""
}

// ObserveAgent fans a per-thought agent event (content deltas, tool
// calls, transitions) onto the stream. The runner forwards events
// with the thought id it belongs to; the agent's own EventBus already
// delivers asynchronously, so a slow observer never blocks the agent.
func (p *Portal) ObserveAgent(thoughtID string, ev agent.AgentEvent) {
	p.fan(PortalEvent{
		Category:  PortalAgent,
		Kind:      string(ev.Type),
		ThoughtID: thoughtID,
		Detail:    agentDetail(ev),
		At:        time.Now().UTC(),
	})
}

// authorize applies the authorizer, if any. A nil authorizer refuses
// everything (fail closed).
func (p *Portal) authorize(action PortalAction, identity string) error {
	if p.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", ErrPortalUnauthorized, action)
	}
	return p.auth.Authorize(action, identity)
}

// Attach authorizes a client identity for the portal attachment
// (S000407); the serve path begins streaming only for authorized
// attachments. Unauthorized attempts are refused, never defaulted
// open.
func (p *Portal) Attach(identity string) error {
	return p.authorize(PortalActAttach, identity)
}

// Steer routes a steering message to a running thought via the queue's
// RouteMessage (S010312): the routing ack comes back on the stream.
func (p *Portal) Steer(identity, thoughtID, text string) error {
	if err := p.authorize(PortalActSteer, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	if _, err := p.queue.RouteMessage(thoughtID, text); err != nil {
		return err
	}
	return nil
}

// Pause parks a running thought (park-with-checkpoint semantics,
// S010002); the paused event comes back on the stream.
func (p *Portal) Pause(identity, thoughtID string) error {
	if err := p.authorize(PortalActPause, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.Pause(thoughtID, nil)
}

// Resume re-queues a paused thought; the resumed event comes back.
func (p *Portal) Resume(identity, thoughtID string) error {
	if err := p.authorize(PortalActResume, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.Resume(thoughtID)
}

// Cancel removes queued/paused work (running thoughts must be parked
// first, per the queue); the cancelled event comes back.
func (p *Portal) Cancel(identity, thoughtID string) error {
	if err := p.authorize(PortalActCancel, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.Cancel(thoughtID)
}

// Enqueue schedules a thought through the queue; the enqueued event
// comes back.
func (p *Portal) Enqueue(identity string, t *thought.Thought) error {
	if err := p.authorize(PortalActEnqueue, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.Enqueue(t)
}

// BumpPriority changes a queued thought's priority through the queue's
// BumpPriority (clamped by the queue; the operation result is the
// immediate feedback — the queue itself emits no event for it).
func (p *Portal) BumpPriority(identity, thoughtID string, prio int) error {
	if err := p.authorize(PortalActBump, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.BumpPriority(thoughtID, prio)
}

// Retry re-queues a failed thought (S010002): the queue's Retry flips
// it back to pending; the enqueued(retry) event comes back on the
// stream.
func (p *Portal) Retry(identity, thoughtID string) error {
	if err := p.authorize(PortalActRetry, identity); err != nil {
		return err
	}
	if p.queue == nil {
		return fmt.Errorf("portal: no queue bound")
	}
	return p.queue.Retry(thoughtID)
}

// Queue returns the bound queue (serve paths use it to seed tests /
// enqueue external work).
func (p *Portal) Queue() *thought.ThoughtQueue {
	return p.queue
}
