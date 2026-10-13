package portal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the public control counterpart of the observation interface:
// a Portal implementation that authorizes attach/control actions and
// applies them through the thought-system handle, binding both at Start
// from Deps. The gateway frames it over HTTP; the in-process TUI controls
// the thought system directly. Authorization fails closed with no
// authorizer wired, and it is asked about a verified Actor only — the
// principal comes from the navi's authenticator and the request facts from
// the binding, never from the client (ADR 0021 decisions 6-9, ADR 0022
// decision 8).

// Action enumerates portal control operations for authorization.
type Action string

const (
	ActionAttach  Action = "attach"
	ActionSteer   Action = "steer"
	ActionPause   Action = "pause"
	ActionResume  Action = "resume"
	ActionCancel  Action = "cancel"
	ActionEnqueue Action = "enqueue"
	ActionBump    Action = "bump"
	ActionRetry   Action = "retry"
)

// Authorizer governs attach + control. A nil authorizer refuses every
// action (fail closed). It is asked about a VERIFIED actor — the principal
// the navi resolved from the connection's credential, together with the
// request facts the binding supplied (ADR 0021 decisions 6-9) — never a
// value the client asserted, and it is the seam the compiled policy engine
// replaces (ADR 0022 decision 8; NAVI-399, NAVI-404).
type Authorizer interface {
	// Authorize returns nil when actor may perform action, or a refusal error
	// naming the action and the reason. The reason is the engine's, so a
	// refusal is loud at the call site rather than looking like nothing
	// happened.
	Authorize(action Action, actor Actor) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(action Action, actor Actor) error

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(action Action, actor Actor) error {
	return f(action, actor)
}

// AllowAll trusts every verified actor for every action — a DEVELOPMENT
// DEFAULT for a console or otherwise trusted front-end, not the shipped
// model. It is a real improvement over the pre-IAM stand-in only when paired
// with a real authenticator: the principal it is asked about is now verified
// rather than asserted. NEVER wire it on an untrusted network surface without
// a real authorizer (the compiled policy of NAVI-399).
func AllowAll() Authorizer { return AuthorizerFunc(func(Action, Actor) error { return nil }) }

// Control is the shared portal control service: it authorizes attach and
// control actions and applies them through the thought system it receives
// at Start. It satisfies Portal (and can be registered in n.Portals) and
// is the control counterpart the gateway delegates to.
type Control struct {
	auth Authorizer

	mu      sync.Mutex
	ts      thought.ThoughtSystem
	obs     Observer
	started bool
	log     *slog.Logger
}

// NewControl builds the control service. auth may be nil (fail closed).
func NewControl(auth Authorizer) *Control { return &Control{auth: auth} }

// Start binds the thought system and observation source from Deps.
// Idempotent.
func (c *Control) Start(ctx context.Context, deps Deps) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	if deps.Log != nil {
		c.log = deps.Log
	}
	c.ts = deps.ThoughtSystem
	c.obs = deps.Observation
	c.started = true
	return nil
}

// Stop ends serving. Control methods stay callable.
func (c *Control) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = false
	return nil
}

// Serving reports whether Start has run.
func (c *Control) Serving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// Subscribe registers a bounded subscription on the bound observation
// source; before Start it returns an empty, detached mailbox.
func (c *Control) Subscribe(capacity int) Subscription {
	c.mu.Lock()
	obs := c.obs
	c.mu.Unlock()
	if obs == nil {
		return NewMailbox(capacity)
	}
	return obs.Subscribe(capacity)
}

func (c *Control) authorize(action Action, actor Actor) error {
	if c.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", ErrUnauthorized, action)
	}
	return c.auth.Authorize(action, actor)
}

func (c *Control) thoughtSystem() (thought.ThoughtSystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ts == nil {
		return nil, fmt.Errorf("portal: no thought system bound")
	}
	return c.ts, nil
}

// Attach authorizes a verified actor for the portal attachment.
func (c *Control) Attach(actor Actor) error {
	return c.authorize(ActionAttach, actor)
}

// Steer routes a steering message to a thought.
func (c *Control) Steer(actor Actor, thoughtID, message string) error {
	if err := c.authorize(ActionSteer, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.RouteMessage(thoughtID, message)
}

// Pause parks a running thought.
func (c *Control) Pause(actor Actor, thoughtID string) error {
	if err := c.authorize(ActionPause, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Pause(thoughtID)
}

// Resume re-queues a paused thought.
func (c *Control) Resume(actor Actor, thoughtID string) error {
	if err := c.authorize(ActionResume, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Resume(thoughtID)
}

// Cancel removes queued/paused work.
func (c *Control) Cancel(actor Actor, thoughtID string) error {
	if err := c.authorize(ActionCancel, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Cancel(thoughtID)
}

// BumpPriority changes a queued thought's priority.
func (c *Control) BumpPriority(actor Actor, thoughtID string, prio int) error {
	if err := c.authorize(ActionBump, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.BumpPriority(thoughtID, prio)
}

// Retry re-queues a failed thought.
func (c *Control) Retry(actor Actor, thoughtID string) error {
	if err := c.authorize(ActionRetry, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Retry(thoughtID)
}

// Enqueue schedules a thought on behalf of actor. The actor is the
// thought's ORIGINATOR: the runtime records it on the thought (with the
// request facts) so every later authorization — a follow-up, a deferred
// stage, a tool call — is decided for the same principal (ADR 0022
// decisions 2, 3 and 5).
func (c *Control) Enqueue(actor Actor, t *thought.Thought) error {
	if err := c.authorize(ActionEnqueue, actor); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	recordOrigin(t, actor)
	return ts.Enqueue(t)
}

// Compile-time check: Control is a portal component.
var _ Portal = (*Control)(nil)
