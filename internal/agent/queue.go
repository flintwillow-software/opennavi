package agent

import "sync"

// ── Steering Queue ───────────────────────────────────────────────────────

// SteeringQueue holds messages that can interrupt or guide the agent
// mid-turn. Behaviour depends on the configured QueueMode:
//
//   - QueueModeOneAtATime: a new message replaces the last queued one.
//   - QueueModeAll: every message is appended in arrival order.
type SteeringQueue struct {
	mode QueueMode
	ch   chan string

	mu    sync.Mutex
	queue []string
}

// NewSteeringQueue creates a steering queue with the given mode. An empty
// mode defaults to QueueModeOneAtATime.
func NewSteeringQueue(mode QueueMode) *SteeringQueue {
	if mode == "" {
		mode = QueueModeOneAtATime
	}
	return &SteeringQueue{
		mode: mode,
		ch:   make(chan string, 8),
	}
}

// Mode returns the queue's configured mode.
func (q *SteeringQueue) Mode() QueueMode {
	return q.mode
}

// Add enqueues a steering message according to the queue mode.
func (q *SteeringQueue) Add(msg string) {
	q.mu.Lock()
	if q.mode == QueueModeOneAtATime && len(q.queue) > 0 {
		q.queue[len(q.queue)-1] = msg // replace last
	} else {
		q.queue = append(q.queue, msg)
	}
	q.mu.Unlock()

	// Non-blocking signal for select-based consumers (Chan).
	select {
	case q.ch <- msg:
	default:
	}
}

// Next returns and removes the oldest queued message.
func (q *SteeringQueue) Next() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return "", false
	}
	msg := q.queue[0]
	q.queue = q.queue[1:]
	return msg, true
}

// Chan exposes a channel for select-based consumption. It receives a
// best-effort signal per Add and may coalesce when full; always drain
// via Next for the authoritative queue contents.
func (q *SteeringQueue) Chan() <-chan string {
	return q.ch
}

// Clear discards all messages.
func (q *SteeringQueue) Clear() {
	q.mu.Lock()
	q.queue = nil
	q.mu.Unlock()
}

// ── Follow-Up Queue ──────────────────────────────────────────────────────

// FollowUpQueue holds messages for after the current turn completes.
type FollowUpQueue struct {
	mu    sync.Mutex
	queue []string
}

// NewFollowUpQueue creates a new follow-up queue.
func NewFollowUpQueue() *FollowUpQueue {
	return &FollowUpQueue{}
}

// Add appends a follow-up message.
func (q *FollowUpQueue) Add(msg string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queue = append(q.queue, msg)
}

// Next returns and removes the oldest message.
func (q *FollowUpQueue) Next() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return "", false
	}
	msg := q.queue[0]
	q.queue = q.queue[1:]
	return msg, true
}

// Clear discards all messages.
func (q *FollowUpQueue) Clear() {
	q.mu.Lock()
	q.queue = nil
	q.mu.Unlock()
}
