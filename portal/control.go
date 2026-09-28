package portal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the public control counterpart of the observation contract:
// a Portal implementation that authorizes attach/control actions and
// applies them through the thought-system handle, binding both at Start
// from Deps. The gateway frames it over HTTP; the in-process TUI controls
// the thought system directly. Authorization fails closed with no
// authorizer wired: IAM lands later.

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
// action (fail closed).
type Authorizer interface {
	// Authorize returns nil when identity may perform action, or a
	// refusal error naming the action.
	Authorize(action Action, identity string) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(action Action, identity string) error

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(action Action, identity string) error {
	return f(action, identity)
}

// AllowAll trusts every identity for every action — the pre-IAM local-trust
// stand-in for a console or otherwise trusted front-end. NEVER wire it on
// an untrusted network surface without a real authorizer.
func AllowAll() Authorizer { return AuthorizerFunc(func(Action, string) error { return nil }) }

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

func (c *Control) authorize(action Action, identity string) error {
	if c.auth == nil {
		return fmt.Errorf("%w: %s (no authorizer wired — fail closed)", ErrUnauthorized, action)
	}
	return c.auth.Authorize(action, identity)
}

func (c *Control) thoughtSystem() (thought.ThoughtSystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ts == nil {
		return nil, fmt.Errorf("portal: no thought system bound")
	}
	return c.ts, nil
}

// Attach authorizes a client identity for the portal attachment.
func (c *Control) Attach(identity string) error {
	return c.authorize(ActionAttach, identity)
}

// Steer routes a steering message to a thought.
func (c *Control) Steer(identity, thoughtID, message string) error {
	if err := c.authorize(ActionSteer, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.RouteMessage(thoughtID, message)
}

// Pause parks a running thought.
func (c *Control) Pause(identity, thoughtID string) error {
	if err := c.authorize(ActionPause, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Pause(thoughtID)
}

// Resume re-queues a paused thought.
func (c *Control) Resume(identity, thoughtID string) error {
	if err := c.authorize(ActionResume, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Resume(thoughtID)
}

// Cancel removes queued/paused work.
func (c *Control) Cancel(identity, thoughtID string) error {
	if err := c.authorize(ActionCancel, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Cancel(thoughtID)
}

// BumpPriority changes a queued thought's priority.
func (c *Control) BumpPriority(identity, thoughtID string, prio int) error {
	if err := c.authorize(ActionBump, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.BumpPriority(thoughtID, prio)
}

// Retry re-queues a failed thought.
func (c *Control) Retry(identity, thoughtID string) error {
	if err := c.authorize(ActionRetry, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Retry(thoughtID)
}

// Enqueue schedules a thought on behalf of identity.
func (c *Control) Enqueue(identity string, t *thought.Thought) error {
	if err := c.authorize(ActionEnqueue, identity); err != nil {
		return err
	}
	ts, err := c.thoughtSystem()
	if err != nil {
		return err
	}
	return ts.Enqueue(t)
}

// Compile-time check: Control is a portal component.
var _ Portal = (*Control)(nil)
