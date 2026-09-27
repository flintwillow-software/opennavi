// Package portal implements the navi's portal control plane (S000407):
// transport-agnostic observation and control of a navi's thought process,
// authorized fail-closed.
//
// The retired "interface set" abstraction (spec
// 00-navi-core/04-interface-set.md) used to live here — a single registry
// selecting among delivery modes (CLI, TUI, gateway, cron) in any
// combination. ADR 0010 retired it, because delivery modes are not one
// layer: human surfaces are *portals* (this package) and inbound
// transports are *connectors* (internal/cron, and the gateway contract).
// That is why the selection machinery is gone and only the portal side
// remains — the abstraction existed to unify two things that turned out
// to be different, and its registry had no callers outside itself.
//
// NAVI-352 moved the observation event, subscription, and bounded mailbox
// onto the public portal contract. The Hub is now a *consumer* of that
// contract: it binds the thought system and the observation source at
// Start from Deps rather than taking the concrete queue at construction.
package portal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// ErrPortalUnauthorized is the internal alias of the public sentinel
// (NAVI-352): a refused attach/control action, never defaulted open.
var ErrPortalUnauthorized = pubportal.ErrUnauthorized

// Compatibility aliases: the observation types now live on the public
// contract (NAVI-352). Internal consumers keep their existing names.
type (
	// PortalCategory buckets observation events by source.
	PortalCategory = pubportal.Category
	// PortalEvent is one normalized observation on a portal stream.
	PortalEvent = pubportal.Event
	// PortalMailbox is one subscriber's bounded event mailbox.
	PortalMailbox = pubportal.Mailbox
	// PortalSubscription is a bounded, cursor-ed observation view.
	PortalSubscription = pubportal.Subscription
	// PortalObserver is the observation source.
	PortalObserver = pubportal.Observer
)

// Category constants, aliased for internal callers.
const (
	PortalQueue = pubportal.CategoryQueue
	PortalAck   = pubportal.CategoryAck
	PortalAgent = pubportal.CategoryAgent
)

// NewPortalMailbox builds a bounded mailbox (public implementation).
func NewPortalMailbox(capacity int) *PortalMailbox { return pubportal.NewMailbox(capacity) }

// PortalAction enumerates portal control operations for authorization
// (S040200).
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

// Hub is the shared control service for one navi (S000407),
// transport-agnostic: it authorizes control actions and exposes the
// bounded observation stream (NAVI-352) to front-ends. Observation itself
// is produced by the runtime's broker and handed to the Hub through Deps;
// the Hub no longer owns the queue or the agent-event hook.
//
// It is a SERVICE, not a front-end. Front-ends (the TUI in-process, a
// remote gateway attachment) attach to it and consume a subscription; each
// runs in its own goroutine, and because subscriptions are bounded with
// drop-oldest and resumable cursors, a slow front-end degrades itself and
// never the queue.
//
// It satisfies portal.Portal so the composition root can register it, and
// it is built from Deps alone: Start binds the thought system and the
// observation source.
type Hub struct {
	auth PortalAuthorizer
	log  *slog.Logger

	mu      sync.Mutex
	ts      thought.ThoughtSystem
	obs     pubportal.Observer
	started bool
}

// NewHub builds the portal control + observation service. auth is the
// authorizer (nil fails closed; S040200). Handles arrive at Start.
func NewHub(auth PortalAuthorizer) *Hub {
	return &Hub{auth: auth}
}

// Start binds the hub to the navi-supplied handles: the thought system for
// control and the observation source for streaming. Idempotent: a caller
// that repeats the call does not get a second binding.
func (h *Hub) Start(ctx context.Context, deps pubportal.Deps) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return nil
	}
	if deps.Log != nil {
		h.log = deps.Log
	}
	h.ts = deps.ThoughtSystem
	h.obs = deps.Observation
	h.started = true
	return nil
}

// Stop ends serving. Control methods stay callable — gating them would
// change behaviour for callers that never started a hub, and that is not
// this split's business.
func (h *Hub) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = false
	return nil
}

// Serving reports whether the hub has been started.
func (h *Hub) Serving() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started
}

// Subscribe registers a bounded subscription on the bound observation
// source. Before Start (or with no source) it returns an empty, detached
// mailbox, so a caller never gets nil.
func (h *Hub) Subscribe(capacity int) pubportal.Subscription {
	h.mu.Lock()
	obs := h.obs
	h.mu.Unlock()
	if obs == nil {
		return pubportal.NewMailbox(capacity)
	}
	return obs.Subscribe(capacity)
}

// thoughtSystem returns the bound control handle or a clear error.
func (h *Hub) thoughtSystem() (thought.ThoughtSystem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ts == nil {
		return nil, fmt.Errorf("portal: no thought system bound")
	}
	return h.ts, nil
}

// authorize applies the authorizer, if any. A nil authorizer refuses
// everything (fail closed).
func (h *Hub) authorize(action PortalAction, identity string) error {
	if h.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", ErrPortalUnauthorized, action)
	}
	return h.auth.Authorize(action, identity)
}

// Attach authorizes a client identity for the portal attachment
// (S000407); the serve path begins streaming only for authorized
// attachments. Unauthorized attempts are refused, never defaulted open.
func (h *Hub) Attach(identity string) error {
	return h.authorize(PortalActAttach, identity)
}

// Steer routes a steering message to a running thought via the queue's
// RouteMessage (S010312): the routing ack comes back on the stream.
func (h *Hub) Steer(identity, thoughtID, text string) error {
	if err := h.authorize(PortalActSteer, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.RouteMessage(thoughtID, text)
}

// Pause parks a running thought (park-with-checkpoint semantics,
// S010002); the paused event comes back on the stream.
func (h *Hub) Pause(identity, thoughtID string) error {
	if err := h.authorize(PortalActPause, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Pause(thoughtID)
}

// Resume re-queues a paused thought; the resumed event comes back.
func (h *Hub) Resume(identity, thoughtID string) error {
	if err := h.authorize(PortalActResume, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Resume(thoughtID)
}

// Cancel removes queued/paused work (running thoughts must be parked
// first, per the queue); the cancelled event comes back.
func (h *Hub) Cancel(identity, thoughtID string) error {
	if err := h.authorize(PortalActCancel, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Cancel(thoughtID)
}

// Enqueue schedules a thought through the queue; the enqueued event
// comes back.
func (h *Hub) Enqueue(identity string, t *thought.Thought) error {
	if err := h.authorize(PortalActEnqueue, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Enqueue(t)
}

// BumpPriority changes a queued thought's priority through the queue's
// BumpPriority (clamped by the queue; the operation result is the
// immediate feedback — the queue itself emits no event for it).
func (h *Hub) BumpPriority(identity, thoughtID string, prio int) error {
	if err := h.authorize(PortalActBump, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.BumpPriority(thoughtID, prio)
}

// Retry re-queues a failed thought (S010002): the queue's Retry flips it
// back to pending; the enqueued(retry) event comes back on the stream.
func (h *Hub) Retry(identity, thoughtID string) error {
	if err := h.authorize(PortalActRetry, identity); err != nil {
		return err
	}
	ts, err := h.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Retry(thoughtID)
}

// The hub is the navi's portal component: it satisfies the public
// contract, so the composition root can register it.
var _ pubportal.Portal = (*Hub)(nil)
