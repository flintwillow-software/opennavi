package navi

import (
	"fmt"
	"sync"

	ithought "github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/thought"
)

// Thoughts is the navi's thought system — the core collection, always
// present and constructed by New rather than added (ADR 0003). It is the
// handle connectors and portals operate through: they receive it as
// Deps.ThoughtSystem.
//
// It wraps the runtime queue behind the public contract. The bridge is
// deliberate and temporary: M3 promotes internal/thought onto the public
// thought package, at which point the two type universes collapse and
// this translation disappears. It exists now so M2 can serve a genuinely
// working thought system instead of a stub.
type Thoughts struct {
	q *ithought.ThoughtQueue

	mu   sync.RWMutex
	subs []func(thought.Event)
}

// newThoughts binds the handle to a queue and starts translating queue
// events onto the public stream.
func newThoughts(q *ithought.ThoughtQueue) *Thoughts {
	t := &Thoughts{q: q}
	q.OnEvent(t.publish)
	return t
}

// publish translates a runtime queue event onto the public stream.
// Every event is fanned out in registration order; a slow or panicking
// subscriber is the subscriber's problem, not the queue's — the runtime
// queue already bounds that on its side.
func (t *Thoughts) publish(ev ithought.QueueEvent) {
	pub := thought.Event{
		Kind:      thought.EventKind(ev.Kind),
		ThoughtID: ev.ThoughtID,
		Detail:    ev.Detail,
		Delivery:  ev.Delivery,
		At:        ev.At,
	}
	t.mu.RLock()
	subs := make([]func(thought.Event), len(t.subs))
	copy(subs, t.subs)
	t.mu.RUnlock()
	for _, fn := range subs {
		fn(pub)
	}
}

// Enqueue schedules a thought. The Thought carries its own deferral
// (ADR 0009 per-thought provider/model overrides included).
func (t *Thoughts) Enqueue(pt *thought.Thought) error {
	if pt == nil {
		return fmt.Errorf("thoughts: enqueue nil thought")
	}
	// A thought the author enqueues without a status is a NEW thought;
	// the runtime queue insists on pending and rejects anything else as
	// an invalid transition, so an unset status means pending rather than
	// a rejection the caller cannot act on.
	status := ithought.ThoughtStatus(pt.Status)
	if status == "" {
		status = ithought.ThoughtPending
	}
	it := toRuntime(pt)
	it.Status = status
	return t.q.Enqueue(it)
}

// RouteMessage delivers a message to a thought, with lifecycle-dependent
// delivery: queued appends to context, running steers, paused resumes,
// terminal is rejected (S010312).
func (t *Thoughts) RouteMessage(id, text string) error {
	_, err := t.q.RouteMessage(id, text)
	return err
}

// Pause parks a thought. NOTE: the public contract's Pause takes no
// checkpoint, while the runtime can persist one — park-with-checkpoint is
// the durability promise (NAVI-338). Wiring a checkpoint through this
// handle is an M3 signature decision, deliberately not guessed at here;
// nil keeps today's behaviour (park without a new checkpoint).
func (t *Thoughts) Pause(id string) error { return t.q.Pause(id, nil) }

// Resume re-queues a paused thought.
func (t *Thoughts) Resume(id string) error { return t.q.Resume(id) }

// Cancel cancels a thought that has not reached a terminal state.
func (t *Thoughts) Cancel(id string) error { return t.q.Cancel(id) }

// BumpPriority changes a queued thought's priority (clamped to 1..10).
func (t *Thoughts) BumpPriority(id string, prio int) error {
	return t.q.BumpPriority(id, prio)
}

// Retry re-queues a failed thought.
func (t *Thoughts) Retry(id string) error { return t.q.Retry(id) }

// GetThought returns a copy of one thought.
func (t *Thoughts) GetThought(id string) (*thought.Thought, error) {
	it, err := t.q.GetThought(id)
	if err != nil {
		return nil, err
	}
	return toPublic(it), nil
}

// GetThoughts returns a copy of the whole queue.
func (t *Thoughts) GetThoughts() []*thought.Thought {
	its := t.q.GetThoughts()
	out := make([]*thought.Thought, 0, len(its))
	for _, it := range its {
		out = append(out, toPublic(it))
	}
	return out
}

// OnEvent registers an observer on the thought event stream. This is how
// portals observe — they subscribe here rather than polling.
func (t *Thoughts) OnEvent(fn func(thought.Event)) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.subs = append(t.subs, fn)
}

// Close releases the handle. The runtime queue's background loops
// (dirty-set flusher, store reconcile) are deliberately NOT started at
// M2: there is no durable store yet, so both would be no-ops guarding
// nothing. They start when the store is wired, and this is where their
// stop closures will be drained.
func (t *Thoughts) Close() {}

// dequeue claims the next claimable thought, as a copy the caller owns.
// Internal on purpose: Dequeue is scheduler mechanics, and the public
// ThoughtSystem contract is about control and observation — exposing
// "take the next thought" there would let any component become a worker.
func (t *Thoughts) dequeue() (*thought.Thought, bool) {
	it, ok := t.q.Dequeue()
	if !ok {
		return nil, false
	}
	return toPublic(it), true
}

// complete marks a thought terminal-success, recording its completion
// message.
// Complete reports a thought's outcome, recording its completion message.
func (t *Thoughts) Complete(id, result string) error { return t.q.Complete(id, result) }

// Fail reports a thought's failure with a detail message.
func (t *Thoughts) Fail(id, detail string) error { return t.q.Fail(id, detail) }

// fail marks a thought failed with a detail message.
func (t *Thoughts) fail(id, detail string) error { return t.q.Fail(id, detail) }

// watchForWork wakes the execution pool whenever the thought system
// reports activity — enqueue, resume, retry, or a freed slot.
func (n *Navi) watchForWork() {
	n.Thoughts.OnEvent(func(thought.Event) { n.dispatch.signal() })
}

// toRuntime converts a public thought into the runtime's type. Status is
// left for Enqueue to default.
//
// Mode is NOT carried: the runtime queue has no such field, and per-thought
// mode selection reaches the runner in a later milestone (ADR 0007). Nothing
// consumes it yet, so dropping it changes no behaviour — but it IS dropped
// rather than persisted, so do not read Mode back out of the queue expecting
// it to survive.
func toRuntime(pt *thought.Thought) *ithought.Thought {
	return &ithought.Thought{
		ID:         pt.ID,
		CreatedAt:  pt.CreatedAt,
		UpdatedAt:  pt.UpdatedAt,
		Prompt:     pt.Prompt,
		Context:    pt.Context,
		Result:     pt.Result,
		Metadata:   copyMeta(pt.Metadata),
		Priority:   pt.Priority,
		DeferUntil: pt.DeferUntil,
		Provider:   pt.Provider,
		Model:      pt.Model,
		Status:     ithought.ThoughtStatus(pt.Status), SessionID: pt.SessionID,
		Foreground: pt.Foreground,
	}
}

// toPublic converts a runtime thought into the public contract's type.
// Runtime-only fields (slot, worker, checkpoint, preemption, log path)
// stay inside the runtime — the public contract is what components act
// on, not the scheduler's bookkeeping.
func toPublic(it *ithought.Thought) *thought.Thought {
	if it == nil {
		return nil
	}
	return &thought.Thought{
		ID:         it.ID,
		Prompt:     it.Prompt,
		Context:    it.Context,
		Result:     it.Result,
		Metadata:   copyMeta(it.Metadata),
		Priority:   it.Priority,
		Status:     string(it.Status),
		Provider:   it.Provider,
		Model:      it.Model,
		DeferUntil: it.DeferUntil,
		SessionID:  it.SessionID,
		Foreground: it.Foreground,
		CreatedAt:  it.CreatedAt,
		UpdatedAt:  it.UpdatedAt,
	}
}

func copyMeta(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// compile-time proof that the handle satisfies the contract components
// depend on (ADR 0004 rule 2 — satisfied structurally, never named).
var _ thought.ThoughtSystem = (*Thoughts)(nil)
