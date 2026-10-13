// Package portal is the public portal component (ADR 0004/0010): a
// portal is a human observation and control surface (CLI, TUI,
// WebUI) — bidirectional, list/schedule/steer. It receives its
// dependencies through portal.Deps and NEVER imports navi (rule 2:
// interfaces satisfied structurally by the composition root).
// Stdlib-only.
package portal

import (
	"context"
	"errors"
	"log/slog"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/thought"
)

// ErrUnauthorized is returned when an attach or control action is refused:
// either no authorizer is wired (fail-closed) or the caller's identity is
// not permitted. Unauthorized control attempts are NEVER defaulted open.
// A portal implementation maps it to its transport's refusal status (HTTP
// 403, a rejected frame, ...).
var ErrUnauthorized = errors.New("portal: unauthorized")

// Deps hands a portal the handles it needs (ADR 0004 rule 2): the
// thought system for observation/control, the model registry, the
// bounded observation stream, and the logger. NO memory handle —
// short-term memory is navi-internal working state (not an extension
// surface) and long-term memory search is a tool against the M4
// interface (same rationale as connector.Deps).
type Deps struct {
	// ThoughtSystem is the navi's queue handle.
	ThoughtSystem thought.ThoughtSystem
	// Model is the provider registry.
	Model model.Registry
	// Observation is the bounded, cursor-ed view of queue + running-agent
	// events (NAVI-352). A portal renders a running thought's output from
	// this alone, with no runtime hook.
	Observation Observer
	// Objects is the navi's typed-object surface (ADR 0017): the exposed
	// type descriptors and bounded object/detail reads. A portal renders
	// whatever the navi exposes without knowing a concrete type. Nil when no
	// types are exposed.
	Objects object.Surface
	// Log is the shared structured logger (stdlib log/slog).
	Log *slog.Logger
	// Authenticator resolves a presented credential to a verified principal
	// (ADR 0021 decisions 6-8). The navi supplies it; a portal presents a
	// credential and acts on the answer, never evaluating policy (ADR 0022
	// decision 8). Nil means no connection can be authenticated — a portal
	// that serves a transport then refuses every attachment (fail closed).
	Authenticator Authenticator
}

// Addressed is an OPTIONAL capability a portal may implement: a portal
// that serves on a network address reports it, so the composition root
// can detect port conflicts between portals at Run (cross-cutting
// validation). Portals that serve no address — a terminal UI, an
// in-process surface — simply do not implement it, and declaring every
// portal addressless would be worse than declaring nothing.
//
// Return the listen address as it would be passed to net.Listen
// ("host:port", ":8090"). An empty string means "no address", and is
// treated as not implementing the capability.
type Addressed interface {
	Addr() string
}

// Portal is the human-facing control surface over a thought system:
// a serve lifecycle, attach (authorize an identity), the
// lifecycle/steering control actions, and scheduling. Observation
// arrives through Deps.ThoughtSystem.OnEvent — the portal subscribes to
// the queue event stream; the methods below are the write path.
type Portal interface {
	// Start begins serving with the handles the navi supplies; ctx
	// cancellation requests shutdown. Deps arrives here rather than at
	// construction for the same reason as connector.Connector.Start: the
	// composition root only assembles it in Run, and passing it through
	// the lifecycle call keeps constructors config-only (ADR 0002) and
	// this package free of any navi import (ADR 0004 rule 1). A portal
	// without this call has no way to obtain the ThoughtSystem it is
	// meant to observe.
	Start(ctx context.Context, deps Deps) error
	// Stop shuts the portal down.
	Stop() error
	// Attach authorizes a verified actor for control (fail-closed until
	// authorization is granted). The actor carries the principal the navi
	// resolved from the connection's credential, never a value the client
	// asserted, together with the request facts the binding supplied.
	Attach(actor Actor) error
	Steer(actor Actor, thoughtID, message string) error
	Pause(actor Actor, thoughtID string) error
	Resume(actor Actor, thoughtID string) error
	Cancel(actor Actor, thoughtID string) error
	// BumpPriority changes a queued thought's priority (NAVI-319 added the
	// operation to the portal surface; the queue clamps it).
	BumpPriority(actor Actor, thoughtID string, prio int) error
	// Retry re-queues a failed thought.
	Retry(actor Actor, thoughtID string) error
	// Enqueue schedules a new thought on behalf of actor
	// (session-created thoughts claim the foreground slot). The actor is
	// recorded as the thought's originator (ADR 0022 decision 2).
	Enqueue(actor Actor, t *thought.Thought) error
}
