package agent

import "sync"

// ── Event Bus ────────────────────────────────────────────────────────────

// subscriber is a registered event handler.
type subscriber struct {
	id   int
	fn   func(AgentEvent)
	once bool
}

// EventBus delivers events to registered subscribers.
// Handlers are invoked in their own goroutines so a slow subscriber
// never blocks the firing path.
type EventBus struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]*subscriber
	order  []int
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[int]*subscriber)}
}

// Subscribe registers a handler for events. If once is true, the
// handler is removed after its first fire. Returns an unsubscribe func.
func (eb *EventBus) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
	eb.mu.Lock()
	id := eb.nextID
	eb.nextID++
	eb.subs[id] = &subscriber{id: id, fn: fn, once: once}
	eb.order = append(eb.order, id)
	eb.mu.Unlock()
	return func() { eb.remove(id) }
}

func (eb *EventBus) remove(id int) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	if _, ok := eb.subs[id]; !ok {
		return
	}
	delete(eb.subs, id)
	for i, v := range eb.order {
		if v == id {
			eb.order = append(eb.order[:i], eb.order[i+1:]...)
			break
		}
	}
}

// Fire broadcasts an event to all subscribers. One-shot subscribers are
// removed before dispatch. Each handler runs in its own goroutine.
func (eb *EventBus) Fire(event AgentEvent) {
	eb.mu.Lock()
	var fns []func(AgentEvent)
	for _, id := range eb.order {
		sub := eb.subs[id]
		if sub == nil {
			continue
		}
		fns = append(fns, sub.fn)
		if sub.once {
			eb.remove(id)
		}
	}
	eb.mu.Unlock()

	for _, fn := range fns {
		go fn(event)
	}
}

// ── End Barrier ──────────────────────────────────────────────────────────

// AgentEndBarrier ensures all one-shot subscribers have completed before
// the agent end event is fired.
type AgentEndBarrier struct {
	mu     sync.Mutex
	count  int
	doneCh chan struct{}
}

// NewAgentEndBarrier creates a barrier with a zero count. Wait blocks
// only while the count is above zero.
func NewAgentEndBarrier() *AgentEndBarrier {
	return &AgentEndBarrier{doneCh: make(chan struct{})}
}

// Add increments the pending one-shot subscriber count.
func (b *AgentEndBarrier) Add() {
	b.mu.Lock()
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
// If nothing is pending it returns immediately.
func (b *AgentEndBarrier) Wait() {
	if b.Count() == 0 {
		return
	}
	<-b.doneCh
}
