// Package connector is the public connector component (ADR 0004/0005):
// a connector is a transport integration that brings work IN to the
// navi — gateway webhooks, Discord/Slack/MQTT/email adapters,
// schedules — modelled as a thing that spawns/attaches/steers
// thoughts. It receives its dependencies through connector.Deps and
// NEVER imports navi (rule 2: interfaces are satisfied structurally
// by the composition root). Stdlib-only.
//
// Egress tools (ADR 0010): `discord_send`, `websocket_send`, etc.
// are TOOLS that close over a connector's live client. They are added
// EXPLICITLY by the connector author via connector.SendTool() — never
// auto-registered — so the capability surface stays visible in
// main.go. The exact SendTool signature binds the tool registry, which
// is not a component package yet (the registry alignment lands at M4);
// M1 therefore documents the rule here and leaves the helper out of
// the ABI until then.
package connector

import (
	"context"
	"log/slog"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// Deps hands a connector the handles it needs (ADR 0004 rule 2): the
// thought system for inbound work and the model registry for provider
// resolution, plus the logger. A navi satisfies these structurally.
// NOTE — no memory handle: short-term memory is navi-internal working
// state, not an extension surface (ADR 0010's three layers are
// tools/connectors/portals); long-term on-demand memory search is a
// TOOL against the component interface, not a Deps field.
type Deps struct {
	// ThoughtSystem is the navi's queue handle.
	ThoughtSystem thought.ThoughtSystem
	// Model is the provider registry.
	Model model.Registry
	// Log is the shared structured logger (stdlib log/slog).
	Log *slog.Logger
}

// Connector is the transport integration interface: Start begins
// ingestion and receives the navi's Deps at that point; ctx
// cancellation requests shutdown. Stop shuts the connector down.
//
// Start/Stop convention: if Start returns an error the connector is NOT
// considered started, and Stop will not be called — Start must release
// anything it acquired before failing. Conversely Stop is called only
// for connectors whose Start succeeded, so it can assume initialisation
// completed.
//
// Deps is a lifecycle ARGUMENT rather than a constructor input. The
// composition root cannot supply it earlier: a navi declares its
// collections first and only assembles them in Run, so a connector
// constructed before Run has nothing to receive. Threading it through
// Start keeps constructors config-only (ADR 0002 —
// `connector.Discord(connector.Config{...})`) and keeps this package
// free of any navi import (ADR 0004 rule 1).
type Connector interface {
	// Start begins the connector's inbound loop with the handles the
	// navi supplies; ctx cancellation requests shutdown.
	Start(ctx context.Context, deps Deps) error
	// Stop shuts the connector down.
	Stop() error
}
