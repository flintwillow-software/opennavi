package agent

// ── Steering Queue ───────────────────────────────────────────────────────

// SteeringQueue holds messages that can interrupt or guide the agent.
type SteeringQueue struct {
	queue []string
}

// NewSteeringQueue creates a new steering queue.
func NewSteeringQueue() *SteeringQueue {
	return &SteeringQueue{}
}

// Add appends a message to the queue.
func (q *SteeringQueue) Add(msg string) {
	q.queue = append(q.queue, msg)
}

// Next returns and removes the oldest message, or "" if empty.
func (q *SteeringQueue) Next() (string, bool) {
	if len(q.queue) == 0 {
		return "", false
	}
	msg := q.queue[0]
	q.queue = q.queue[1:]
	return msg, true
}

// Clear discards all messages.
func (q *SteeringQueue) Clear() {
	q.queue = nil
}

// ── Follow-Up Queue ──────────────────────────────────────────────────────

// FollowUpQueue holds messages for after the current turn.
type FollowUpQueue struct {
	queue []string
}

// NewFollowUpQueue creates a new follow-up queue.
func NewFollowUpQueue() *FollowUpQueue {
	return &FollowUpQueue{}
}

// Add appends a follow-up message.
func (q *FollowUpQueue) Add(msg string) {
	q.queue = append(q.queue, msg)
}

// Next returns and removes the oldest message, or "" if empty.
func (q *FollowUpQueue) Next() (string, bool) {
	if len(q.queue) == 0 {
		return "", false
	}
	msg := q.queue[0]
	q.queue = q.queue[1:]
	return msg, true
}

// Clear discards all messages.
func (q *FollowUpQueue) Clear() {
	q.queue = nil
}
