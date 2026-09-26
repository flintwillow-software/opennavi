// Package portal is the public portal contract (ADR 0004/0010): a
// portal is a human observation and control surface (CLI, TUI,
// WebUI) — bidirectional, list/schedule/steer. It receives its
// dependencies through portal.Deps and NEVER imports navi (rule 2:
// interfaces satisfied structurally by the composition root).
// Stdlib-only.
package portal

import (
	"log/slog"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// Deps hands a portal the handles it needs (ADR 0004 rule 2): the
// thought system for observation/control, the model registry, and
// the logger. NO memory handle — short-term memory is navi-internal
// working state (not an extension surface) and long-term memory
// search is a tool against the M4 contract (same rationale as
// connector.Deps).
type Deps struct {
	// ThoughtSystem is the navi's queue handle.
	ThoughtSystem thought.ThoughtSystem
	// Model is the provider registry.
	Model model.Registry
	// Log is the shared structured logger (stdlib log/slog).
	Log *slog.Logger
}

// Portal is the human-facing control surface over a thought system:
// attach (authorize an identity), the lifecycle/steering control
// actions, and scheduling. Observation arrives through
// Deps.ThoughtSystem.OnEvent — the portal subscribes to the queue
// event stream; the contract stays control-only here.
type Portal interface {
	// Attach authorizes an identity for control (fail-closed until
	// authorization is granted).
	Attach(identity string) error
	Steer(identity, thoughtID, message string) error
	Pause(identity, thoughtID string) error
	Resume(identity, thoughtID string) error
	Cancel(identity, thoughtID string) error
	// Enqueue schedules a new thought on behalf of identity
	// (session-created thoughts claim the foreground slot).
	Enqueue(identity string, t *thought.Thought) error
}
