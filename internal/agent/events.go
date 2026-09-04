package agent

import "sync"

// ── Event Bus ────────────────────────────────────────────────────────────

// EventBus delivers events to registered subscribers.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string]func(AgentEvent)
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string]func(AgentEvent)),
	}
}

// Subscribe registers a handler for events. If once is true, the handler
// is removed after first fire (not yet implemented).
func (eb *EventBus) Subscribe(name string, handler func(AgentEvent), once bool) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subscribers[name] = handler
	_ = once // TODO: implement one-shot subscriptions
}

// Fire broadcasts an event to all subscribers.
func (eb *EventBus) Fire(event AgentEvent) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	for _, handler := range eb.subscribers {
		handler(event)
	}
}

// ── End Barrier ──────────────────────────────────────────────────────────

// AgentEndBarrier ensures all one-shot subscribers have completed before
// the agent end event is fired.
type AgentEndBarrier struct {
	mu     map[string]int
	doneCh chan struct{}
}

// NewAgentEndBarrier creates a barrier for the given subscriber names.
func NewAgentEndBarrier(names []string) *AgentEndBarrier {
	counts := make(map[string]int)
	for _, n := range names {
		counts[n] = 1
	}
	return &AgentEndBarrier{
		mu:     counts,
		doneCh: make(chan struct{}),
	}
}

// Add increments the counter for a subscriber name.
func (b *AgentEndBarrier) Add(name string) {
	b.mu[name]++
}

// Signal decrements the counter; when the last subscriber finishes,
// the done channel is closed.
func (b *AgentEndBarrier) Signal(name string) {
	b.mu[name]--
	if b.mu[name] <= 0 {
		delete(b.mu, name)
		if len(b.mu) == 0 {
			close(b.doneCh)
		}
	}
}

// Wait blocks until the done channel is closed.
func (b *AgentEndBarrier) Wait() {
	<-b.doneCh
}
