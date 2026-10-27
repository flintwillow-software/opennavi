package connector

// Inbound priority hints (ADR 0035 decision 5, ADR 0036 decision 2).
//
// A connector is closer to the event than the queue is, so it states the
// urgency it knows: a comment where the user is waiting for feedback, a new
// assignment that has a place to fall, an urgent issue that cannot wait. The
// hint decides where the spawned thought lands and whether it interrupts.
//
// A hint is ADVISORY, not a verdict: only the running thought can see the queue
// and judge whether the event is urgent *relative to what the navi is already
// doing* (ADR 0036 decision 2). A connector sees one event and has no view of
// the queue, so it may be revised.
//
// # Why the hint is a pointer
//
// "Unset" and "deliberately urgent" must be distinguishable, and on the
// thought's scale urgent is 0 — which is also an int's zero value. An `int`
// field therefore cannot express both, and the collision is silent: an
// undeclared hint would land every event in the urgent band, where it
// interrupts (cancels) whatever is running. A pointer makes the invalid state
// unrepresentable: nil is unset and resolves to the default, and a pointer to 0
// is a deliberate interrupt. This is the same shape as ADR 0034's
// `allow_fallbacks *bool`, and for the same reason.
//
// PriorityHint is the declared value carried on a subscription's inbound
// declaration. Nil means the subscription declares none, and the thought lands
// at DefaultPriority.
type PriorityHint = *int

// DefaultPriority is where a thought lands when its inbound declaration
// supplies no hint. It is the "planned work" band: it does not interrupt, and
// it does not outrank a genuinely urgent arrival.
const DefaultPriority = 3

// ResolvePriority turns a declared hint into the priority to enqueue at. Nil
// (unset) resolves to DefaultPriority; a declared value is used verbatim,
// including a deliberate 0 (the urgent band, which preempts by cancellation,
// ADR 0035 decision 3).
func ResolvePriority(hint PriorityHint) int {
	if hint == nil {
		return DefaultPriority
	}
	return *hint
}

// PriorityValue returns a pointer to v, for declaring a hint inline in a
// subscription. It exists so a declaration reads as
// `Priority: connector.PriorityValue(0)` rather than needing a local variable
// to take the address of.
func PriorityValue(v int) PriorityHint { return &v }

// EventPriority maps a service's own priority name to a hint, returning nil for
// a value that maps to nothing (an unprioritised issue), so the caller falls
// back to the subscription's declared hint or the default. The names are the
// ones the services use; the mapping is deliberately explicit rather than a
// numeric pass-through, because the services' scales are not the thought
// queue's scale and a pass-through would silently mean something else.
//
// Urgent maps to the thought queue's urgent band (0), which INTERRUPTS — this
// is the one case where a connector should say "cannot wait" (ADR 0035
// decision 5). Everything else is planned work.
func EventPriority(name string) PriorityHint {
	switch name {
	case "urgent", "critical", "blocker":
		return PriorityValue(0)
	case "high":
		return PriorityValue(1)
	case "medium", "normal":
		return PriorityValue(2)
	case "low", "minor":
		return PriorityValue(3)
	default:
		return nil // no-priority, none, unknown: no opinion
	}
}

// HintForEventPriority resolves the priority a spawned thought should carry: an
// event's own priority wins when the service states one, and the subscription's
// declared hint is the fallback (for an event with no priority of its own, or a
// service that does not report one). With neither, the thought lands at
// DefaultPriority.
//
// Event-first rather than subscription-first because the event is the more
// specific signal: a subscription covers a whole project, while an urgent issue
// is urgent on its own terms. A subscription can still steer its own events by
// declaring a hint for the kinds that carry none.
func HintForEventPriority(eventName string, subHint PriorityHint) int {
	if ev := EventPriority(eventName); ev != nil {
		return *ev
	}
	return ResolvePriority(subHint)
}

// ValidPriority reports whether p is on the thought queue's scale (0..10,
// ADR 0035 decision 1). A declared hint outside it is a composition error
// rather than a value silently clamped at the door.
func ValidPriority(p int) bool {
	return p >= 0 && p <= 10
}
