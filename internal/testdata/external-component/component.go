// Package component is a synthetic OUT-OF-REPO component module
// (ADR 0004, ADR 0005): it publishes a tool, a connector, and a portal
// while importing ONLY the public component packages — tool, thought,
// connector, portal, model, skill. It never imports the composition root
// (navi) or runtime internals.
//
// CI builds this module and asserts the import boundary so a change that
// leaks a runtime type into a component package, or that makes a component
// pull in the runtime, fails on the PR. See
// docs/architecture/extension-model.md.
package component

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

// PingTool is an author tool a navi composes. Its SourceInfo names this
// module, exactly as a real third-party component would.
func PingTool() tool.Tool {
	schema, _ := tool.ParseSchema(json.RawMessage(`{"type":"object"}`))
	return tool.Tool{
		Def: tool.Def{
			Name:          "external_ping",
			Description:   "Synthetic out-of-repo tool: returns pong.",
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        tool.AccessReadOnly,
			Timeout:       5 * time.Second,
			Recovery:      "retry_once",
			Mode:          tool.ToolModeSequential,
			Tags:          []string{"external", "synthetic"},
			Version:       "0.1.0",
			PromptSnippet: "Ping the external component module",
			SourceInfo: tool.SourceInfo{
				Origin: tool.OriginAuthor,
				Path:   "example.com/external-component",
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "pong", nil
		},
	}
}

// Watcher is a minimal connector: it satisfies connector.Connector with no
// transport, which is all the composition root requires structurally.
type Watcher struct {
	started bool
}

var _ connector.Connector = (*Watcher)(nil)

// Start records that the navi supplied Deps; Stop clears it.
func (w *Watcher) Start(ctx context.Context, deps connector.Deps) error {
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("external-component: thought system is required")
	}
	w.started = true
	return nil
}

// Stop shuts the watcher down.
func (w *Watcher) Stop() error {
	w.started = false
	return nil
}

// Dashboard is a minimal, fail-closed portal: it attaches identities
// explicitly and refuses control for anyone else. It implements
// portal.Portal so the composition root satisfies it structurally.
type Dashboard struct {
	attached map[string]bool
}

var _ portal.Portal = (*Dashboard)(nil)

// Start begins serving; the synthetic dashboard has no transport.
func (d *Dashboard) Start(ctx context.Context, deps portal.Deps) error { return nil }

// Stop shuts the dashboard down.
func (d *Dashboard) Stop() error { return nil }

// Attach authorizes identity for control. An empty identity is refused.
func (d *Dashboard) Attach(identity string) error {
	if identity == "" {
		return fmt.Errorf("dashboard: %w", portal.ErrUnauthorized)
	}
	if d.attached == nil {
		d.attached = map[string]bool{}
	}
	d.attached[identity] = true
	return nil
}

func (d *Dashboard) authorize(identity string) error {
	if !d.attached[identity] {
		return fmt.Errorf("dashboard: %w", portal.ErrUnauthorized)
	}
	return nil
}

// Steer injects a message into a running thought.
func (d *Dashboard) Steer(identity, thoughtID, message string) error {
	return d.authorize(identity)
}

// Pause pauses a thought.
func (d *Dashboard) Pause(identity, thoughtID string) error { return d.authorize(identity) }

// Resume resumes a paused thought.
func (d *Dashboard) Resume(identity, thoughtID string) error { return d.authorize(identity) }

// Cancel cancels a thought.
func (d *Dashboard) Cancel(identity, thoughtID string) error { return d.authorize(identity) }

// BumpPriority changes a queued thought's priority.
func (d *Dashboard) BumpPriority(identity, thoughtID string, prio int) error {
	return d.authorize(identity)
}

// Retry re-queues a failed thought.
func (d *Dashboard) Retry(identity, thoughtID string) error { return d.authorize(identity) }

// Enqueue schedules a new thought on behalf of identity.
func (d *Dashboard) Enqueue(identity string, t *thought.Thought) error {
	return d.authorize(identity)
}
