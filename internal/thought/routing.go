package thought

import (
	"errors"
	"fmt"
	"time"
)

// ── Message routing (S010312) ────────────────────────────────────────
//
// Queue-level message routing: messages addressed to a thought by ID,
// delivered with lifecycle-dependent semantics:
//
//	pending   → Context append (path: context-append)
//	running   → agent steering queue at the turn boundary (path:
//	            steering; bridge wired via SetSteeringRouter)
//	paused    → resumed (paused → pending) with the message attached
//	            (path: resume-with-message)
//	failed    → rejected (retry is the S010002 path, not routing)
//	complete / cancelled → rejected (terminal)
//
// Every attempt — delivery or rejection — emits a delivery-ack event
// (EventMessageRouted) carrying the thought id, the delivery path, and
// the outcome, so portal clients (S000406) observe both deliverables
// and rejections.
//
// DISCIPLINE: RouteMessage follows the queue's deferred collect/emit
// pattern — state mutations and save() happen under q.mu, and the
// steering router (external code) plus q.emit happen after unlock.
// Event handlers must never re-enter the queue while q.mu is held;
// the same rule applies to injected steering routers.

// DeliveryPath names the delivery route taken for a routed message
// (S010312): how the message reached (or failed to reach) the
// thought. The ack event reports it so portal clients can display the
// injection point.
type DeliveryPath string

const (
	// PathContextAppend — pending thought: message appended to its
	// Context, picked up when a worker claims it.
	PathContextAppend DeliveryPath = "context-append"
	// PathSteering — running thought: message injected into the
	// agent's steering queue at the next turn boundary (S010310).
	PathSteering DeliveryPath = "steering"
	// PathResume — paused thought: resumed (paused → pending) with
	// the message attached to its Context.
	PathResume DeliveryPath = "resume-with-message"
	// PathNone — rejection: no delivery path was taken.
	PathNone DeliveryPath = ""
)

// Delivery outcomes for routed-message acks.
const (
	Delivered = "delivered"
	Rejected  = "rejected"
)

// EventMessageRouted is the delivery-ack event kind (S010312):
// emitted for every RouteMessage attempt, delivered or rejected, with
// QueueEvent.Delivery carrying the path and QueueEvent.Detail the
// outcome ("delivered" or "rejected: <reason>").
const EventMessageRouted QueueEventKind = "message_routed"

// Errors for message routing.
var (
	// ErrDeliveryRejected wraps rejections: terminal states, failed
	// thoughts, or steering without a bridge. Check with errors.Is.
	ErrDeliveryRejected = errors.New("message delivery rejected")
	// ErrSteeringUnavailable is returned when a message targets a
	// running thought but no steering bridge is registered (the
	// runtime wires the agent bridge; NAVI-320).
	ErrSteeringUnavailable = errors.New("no steering bridge registered for running thoughts")
)

// SteeringRouter injects a message into a running thought's agent
// (turn-boundary steering, S010310). The queue invokes the router
// OUTSIDE q.mu — implementations must not re-enter the queue from
// within (read state via snapshots/GetThought instead). Wired by the
// runtime (NAVI-320); without a router, messages to running thoughts
// are rejected with ErrSteeringUnavailable.
type SteeringRouter func(thoughtID, message string) error

// SetSteeringRouter registers (or replaces) the steering bridge.
// Passing nil unregisters: running-thought messages are then rejected
// with ErrSteeringUnavailable.
func (q *ThoughtQueue) SetSteeringRouter(fn SteeringRouter) {
	q.mu.Lock()
	q.steeringRouter = fn
	q.mu.Unlock()
}

// slotBy returns the slot with the given ID (caller holds q.mu).
func (q *ThoughtQueue) slotBy(slotID string) *Slot {
	for i := range q.slots {
		if q.slots[i].ID == slotID {
			return &q.slots[i]
		}
	}
	return nil
}

// locate finds the thought by id across all queue lists and reports
// which list holds it (caller holds q.mu). The returned pointer is
// the queue's own thought — mutation is safe under q.mu.
func (q *ThoughtQueue) locate(id string) (*Thought, string, int, bool) {
	if t, i := q.findIn(q.pending, id); t != nil {
		return t, "pending", i, true
	}
	if t, i := q.findIn(q.running, id); t != nil {
		return t, "running", i, true
	}
	if t, i := q.findIn(q.paused, id); t != nil {
		return t, "paused", i, true
	}
	if t, i := q.findIn(q.terminal, id); t != nil {
		return t, "terminal", i, true
	}
	return nil, "", -1, false
}

// RouteMessage delivers a message to the thought identified by id,
// with semantics per its lifecycle state (S010312):
//
//   - pending: message appended to Context (path context-append)
//   - running: handed to the steering router for turn-boundary
//     injection (path steering); rejected if no bridge is registered
//   - paused: resumed (paused → pending) with the message attached
//     (path resume-with-message)
//   - failed: rejected — the retry path is Retry (S010002)
//   - complete/cancelled: rejected — terminal
//
// Returns the delivery path taken (empty on rejection) and an error
// on rejection. Every attempt emits a delivery-ack event
// (EventMessageRouted: thought id, path, outcome) so portal clients
// observe delivery and rejections alike. Unknown ids return
// ErrThoughtNotFound with a rejected ack (the attempt itself is
// observable).
func (q *ThoughtQueue) RouteMessage(id, msg string) (DeliveryPath, error) {
	q.mu.Lock()
	t, _, idx, ok := q.locate(id)
	if !ok {
		q.mu.Unlock()
		q.emit(rejectedAck(id, PathNone, fmt.Errorf("route %q: %w", id, ErrThoughtNotFound)))
		return "", fmt.Errorf("thought: route %q: %w", id, ErrThoughtNotFound)
	}

	var evts []QueueEvent
	var saveErr error

	switch t.Status {
	case ThoughtPending:
		// Queued work: append to Context; the worker picks the message
		// up from the snapshot at claim time.
		t.AppendMessage(msg)
		saveErr = q.save(t)
		evts = append(evts, routedAck(id, PathContextAppend, Delivered))
		q.mu.Unlock()
		q.emit(evts)
		if saveErr != nil {
			return PathContextAppend, saveErr
		}
		return PathContextAppend, nil

	case ThoughtRunning:
		// Capture the bridge under lock, then hand off outside it —
		// injected code (the runtime's steering bridge, NAVI-320)
		// must never run while q.mu is held.
		router := q.steeringRouter
		q.mu.Unlock()

		var steerErr error
		if router == nil {
			steerErr = ErrSteeringUnavailable
		} else {
			steerErr = router(id, msg)
		}
		if steerErr != nil {
			q.emit(rejectedAck(id, PathSteering, fmt.Errorf("steering: %w", steerErr)))
			return PathSteering, fmt.Errorf("thought: route %s via steering: %w", id, steerErr)
		}
		q.emit(routedAckSlice(id, PathSteering, Delivered))
		return PathSteering, nil

	case ThoughtPaused:
		// Resume with the message attached (spec: paused → pending,
		// message in Context for the resumed run). idx indexes
		// q.paused.
		t.AppendMessage(msg)
		if err := t.Transition(ThoughtPending); err != nil {
			q.mu.Unlock()
			q.emit(rejectedAck(id, PathResume, fmt.Errorf("resume %s: %w", id, err)))
			return PathResume, err
		}
		q.paused = append(q.paused[:idx], q.paused[idx+1:]...)
		q.pending = append(q.pending, t)
		q.sortPending()
		saveErr = q.save(t)
		evts = append(evts,
			QueueEvent{Kind: EventResumed, ThoughtID: id, Detail: "resumed with routed message", At: timeNowUTC()},
			routedAck(id, PathResume, Delivered),
		)
		q.mu.Unlock()
		q.emit(evts)
		if saveErr != nil {
			return PathResume, saveErr
		}
		return PathResume, nil

	case ThoughtFailed:
		q.mu.Unlock()
		err := fmt.Errorf("thought: route %s: %w (failed thoughts retry via Retry, S010002)", id, ErrDeliveryRejected)
		q.emit(rejectedAck(id, PathNone, err))
		return "", err

	case ThoughtComplete, ThoughtCancelled:
		q.mu.Unlock()
		err := fmt.Errorf("thought: route %s: %w (terminal state %s)", id, ErrDeliveryRejected, t.Status)
		q.emit(rejectedAck(id, PathNone, err))
		return "", err

	default:
		err := fmt.Errorf("thought: route %s: %w (state %s)", id, ErrDeliveryRejected, t.Status)
		q.mu.Unlock()
		return "", err
	}
}

// AppendMessage appends msg to the thought's Context on a newline
// boundary and stamps UpdatedAt. Used by message routing (S010312:
// pending deliveries and paused resume-with-message).
func (t *Thought) AppendMessage(msg string) {
	if t.Context == "" {
		t.Context = msg
	} else {
		t.Context += "\n" + msg
	}
	t.UpdatedAt = timeNowUTC()
}

// routedAck builds a delivery-ack event (S010312): thought id,
// delivery path, outcome.
func routedAck(id string, path DeliveryPath, outcome string) QueueEvent {
	return QueueEvent{
		Kind:      EventMessageRouted,
		ThoughtID: id,
		Delivery:  string(path),
		Detail:    outcome,
		At:        timeNowUTC(),
	}
}

// rejectedAck wraps a rejection into an ack event.
func rejectedAck(id string, path DeliveryPath, err error) []QueueEvent {
	return []QueueEvent{routedAck(id, path, Rejected+": "+err.Error())}
}

// timeNowUTC is the routing stamp clock (swappable in tests).
var timeNowUTC = func() time.Time { return time.Now().UTC() }

// routedAckSlice wraps a single ack into the emit-able slice form.
func routedAckSlice(id string, path DeliveryPath, outcome string) []QueueEvent {
	return []QueueEvent{routedAck(id, path, outcome)}
}
