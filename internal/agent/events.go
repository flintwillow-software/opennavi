package agent

import "sync"

// ── Event Bus ────────────────────────────────────────────────────────────

// subscriber is a registered event handler with its own mailbox. A
// dedicated goroutine drains the mailbox in FIFO order, so a subscriber
// observes events in the order Fire was called — even when Fire itself
// runs concurrently (e.g. streaming deltas).
type subscriber struct {
	id   int
	fn   func(AgentEvent)
	once bool

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []AgentEvent
	closed bool
}

func newSubscriber(fn func(AgentEvent), once bool) *subscriber {
	s := &subscriber{id: -1, fn: fn, once: once}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// enqueue appends an event to the mailbox. Returns false if the
// subscriber is closed (unsubscribed) and the event was not queued.
func (s *subscriber) enqueue(ev AgentEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.queue = append(s.queue, ev)
	s.cond.Signal()
	return true
}

// close marks the mailbox closed and wakes the drain goroutine.
// Already-queued events are still delivered before it exits.
func (s *subscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Signal()
	s.mu.Unlock()
}

// drain processes queued events in order until the mailbox is closed
// and empty. Each delivered event counts against wg so WaitDispatch
// can track in-flight work.
func (s *subscriber) drain(wg *sync.WaitGroup) {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()

		s.fn(ev)
		wg.Done()
	}
}

// EventBus delivers events to registered subscribers. Each subscriber
// has a mailbox drained by its own goroutine: handlers run
// asynchronously, but per-subscriber delivery order matches Fire order.
type EventBus struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]*subscriber
	order  []int
	wg     sync.WaitGroup // tracks events queued but not yet delivered
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[int]*subscriber)}
}

// Subscribe registers a handler for events. If once is true, the
// handler is removed after its first fire. Returns an unsubscribe func.
func (eb *EventBus) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
	sub := newSubscriber(fn, once)
	eb.mu.Lock()
	id := eb.nextID
	eb.nextID++
	sub.id = id
	eb.subs[id] = sub
	eb.order = append(eb.order, id)
	eb.mu.Unlock()

	go sub.drain(&eb.wg)

	return func() { eb.remove(id) }
}

func (eb *EventBus) remove(id int) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.removeLocked(id)
}

// removeLocked deletes a subscriber and closes its mailbox. Caller
// must hold eb.mu. Already-queued events are still delivered by the
// drain goroutine before it exits.
func (eb *EventBus) removeLocked(id int) {
	sub, ok := eb.subs[id]
	if !ok {
		return
	}
	delete(eb.subs, id)
	for i, v := range eb.order {
		if v == id {
			eb.order = append(eb.order[:i], eb.order[i+1:]...)
			break
		}
	}
	sub.close()
}

// Fire queues an event for all current subscribers. One-shot
// subscribers are removed before dispatch. Each subscriber's handlers
// run in their own goroutine and observe events in Fire order.
func (eb *EventBus) Fire(event AgentEvent) {
	eb.mu.Lock()
	for _, id := range eb.order {
		sub := eb.subs[id]
		if sub == nil {
			continue
		}
		// Add before enqueue: the drain goroutine may deliver and Done
		// before enqueue returns, so the count must already include it.
		eb.wg.Add(1)
		if !sub.enqueue(event) {
			eb.wg.Done() // closed mailbox, event not queued
			continue
		}
		if sub.once {
			// The event is queued; close the mailbox so this was the
			// subscriber's last delivery.
			eb.removeLocked(id)
		}
	}
	eb.mu.Unlock()
}

// WaitDispatch blocks until every handler dispatched so far has
// completed. WaitForIdle uses this so callers that block on the run
// loop observe all events the run fired, even though handlers run
// asynchronously.
func (eb *EventBus) WaitDispatch() {
	eb.wg.Wait()
}

// ── End Barrier ──────────────────────────────────────────────────────────

// AgentEndBarrier ensures all one-shot subscribers have completed before
// the agent end event is fired. It is reusable: each time the pending
// count returns to zero the done channel is "closed", and the next Add
// from zero starts a fresh generation with a new channel.
type AgentEndBarrier struct {
	mu     sync.Mutex
	count  int
	doneCh chan struct{} // closed when count transitions 1 -> 0
}

// NewAgentEndBarrier creates a barrier with a zero count. Wait blocks
// only while the count is above zero.
func NewAgentEndBarrier() *AgentEndBarrier {
	return &AgentEndBarrier{doneCh: make(chan struct{})}
}

// Add increments the pending one-shot subscriber count. If the barrier
// was idle (count 0) a new generation channel is created.
func (b *AgentEndBarrier) Add() {
	b.mu.Lock()
	if b.count == 0 {
		b.doneCh = make(chan struct{})
	}
	b.count++
	b.mu.Unlock()
}

// Signal decrements the count; when it reaches zero the done channel
// closes, releasing any Waiters. Calling Signal with a count already at
// zero is a no-op (guards double-signals).
func (b *AgentEndBarrier) Signal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count <= 0 {
		return
	}
	b.count--
	if b.count == 0 {
		close(b.doneCh)
	}
}

// Count returns the current pending count.
func (b *AgentEndBarrier) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Wait blocks until all pending one-shot subscribers have signalled.
// It captures the current generation channel up front, so a Wait that
// races with Signal is released by the close, and a Wait that races
// with a later generation is unaffected by it.
func (b *AgentEndBarrier) Wait() {
	b.mu.Lock()
	ch := b.doneCh
	pending := b.count
	b.mu.Unlock()
	if pending == 0 {
		return
	}
	<-ch
}
