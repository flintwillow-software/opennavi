package portal

import (
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the portal observation contract (NAVI-352): a
// bounded, cursor-ed view of everything a running navi emits, so a
// portal can render a running thought's output through the public
// contract alone — no runtime hook, no import of internal/.
//
// The contract defines WHAT is observed; framing belongs to the wire
// protocol (05-messaging, not landed). The bounded mailbox lives here so
// the slow-consumer discipline (drop-oldest + resumable cursor) is part
// of the contract rather than re-invented by every surface.

// Category buckets observation events by source.
type Category string

const (
	// CategoryQueue is a thought-system lifecycle transition
	// (enqueued/started/paused/.../persist_failed).
	CategoryQueue Category = "queue"
	// CategoryAck is a message-routing delivery ack, carrying
	// the routing path in Delivery.
	CategoryAck Category = "ack"
	// CategoryAgent is a per-thought agent event (content deltas, tool
	// calls, lifecycle transitions) from the running agent's event bus.
	CategoryAgent Category = "agent"
)

// Event is one normalized observation on a portal stream.
type Event struct {
	// Seq is the per-subscription cursor: the mailbox that delivered the
	// event assigned it, so a consumer resumes without gaps via ReadAfter.
	Seq int
	// Category buckets the event by source.
	Category Category
	// Kind is the source event kind ("started", "content_delta", ...).
	Kind string
	// ThoughtID identifies the thought the event belongs to.
	ThoughtID string
	// Detail is a human-short summary (truncated; never raw credentials).
	Detail string
	// Delivery is the routing path for acks; empty otherwise.
	Delivery string
	// At is the event time.
	At time.Time
}

// Subscription is a bounded, cursor-ed view of the observation stream.
// ReadAfter returns the events after the cursor, the next cursor to use,
// and whether events before the cursor were dropped (the subscriber fell
// behind the bounded window and must expect a gap).
type Subscription interface {
	ReadAfter(after int) ([]Event, int, bool)
	// Close stops the subscription from accepting new events;
	// already-queued events remain readable until drained.
	Close()
}

// Observer is the observation source handed to a component through Deps.
type Observer interface {
	// Subscribe registers a bounded subscription of the given capacity
	// (<= 0 means the default). The caller owns draining and closing it.
	Subscribe(cap int) Subscription
}

// QueueEvent normalizes a thought-system event into an observation event.
// Producers (the runtime) translate queue and agent events through it (or
// their equivalents) before publishing.
func QueueEvent(ev thought.Event) Event {
	category := CategoryQueue
	if ev.Kind == thought.EventMessageRouted {
		category = CategoryAck
	}
	return Event{
		Category:  category,
		Kind:      string(ev.Kind),
		ThoughtID: ev.ThoughtID,
		Detail:    ev.Detail,
		Delivery:  ev.Delivery,
		At:        ev.At,
	}
}

// Broker is the runtime observation fanout. Producers publish; each
// subscriber gets an independent bounded mailbox, so one stuck or slow
// consumer never blocks the fanout or starves later observation (the
// slow-consumer discipline the metrics collector and agent EventBus
// follow, #280). Publish is cheap by design — it runs inside the
// producer's fanout and must never re-enter the queue.
type Broker struct {
	mu   sync.Mutex
	subs []*Mailbox
}

// NewBroker creates an empty observation fanout.
func NewBroker() *Broker { return &Broker{} }

// Subscribe registers a bounded mailbox and returns it.
func (b *Broker) Subscribe(cap int) Subscription {
	m := NewMailbox(cap)
	b.mu.Lock()
	b.subs = append(b.subs, m)
	b.mu.Unlock()
	return m
}

// Publish appends an event to every subscribed mailbox. The per-mailbox
// sequence is assigned on delivery, so each subscriber has an independent
// cursor.
func (b *Broker) Publish(ev Event) {
	b.mu.Lock()
	for _, m := range b.subs {
		m.Push(ev)
	}
	b.mu.Unlock()
}

// Mailbox is one subscriber's bounded event mailbox. Producers Push under
// the mailbox lock; the consumer drains via ReadAfter. Bounded with
// drop-oldest, so a slow consumer degrades itself and never the fanout.
type Mailbox struct {
	mu     sync.Mutex
	ring   []Event // oldest first, capped at cap
	cap    int
	closed bool
	seq    int // next Seq to assign (1-based on first push)
}

// NewMailbox builds a bounded mailbox. A capacity <= 0 uses the default.
func NewMailbox(capacity int) *Mailbox {
	if capacity <= 0 {
		capacity = 512
	}
	return &Mailbox{cap: capacity}
}

// Push appends an event, assigning its per-mailbox sequence. When the
// mailbox is full the OLDEST event is dropped (bounded memory,
// slow-consumer safety); a closed mailbox drops everything.
func (m *Mailbox) Push(ev Event) {
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

// ReadAfter returns the events after the given cursor plus the next cursor
// to use, and whether events before the cursor were dropped (lost=true:
// the consumer fell behind the bounded window and must expect a gap).
func (m *Mailbox) ReadAfter(after int) ([]Event, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ring) == 0 {
		return []Event{}, after, false
	}
	first := m.ring[0].Seq
	// The cursor precedes the bounded window: events were dropped while
	// the consumer was away (slow-consumer gap).
	lost := after < first-1
	start := len(m.ring)
	for i, ev := range m.ring {
		if ev.Seq >= after {
			start = i
			break
		}
	}
	out := append([]Event{}, m.ring[start:]...)
	if len(out) > 0 {
		return out, out[len(out)-1].Seq + 1, lost
	}
	return out, after, lost
}

// Close stops the mailbox from accepting new events; already-queued events
// remain readable until drained.
func (m *Mailbox) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}
