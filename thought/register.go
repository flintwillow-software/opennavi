// Queue-handle registration seam (S130600 §00): catalog tools bound to
// the navi's OWN thought-queue handle at registration, with full
// registry governance (complete Definition metadata, schema validation,
// active-set membership, access levels per S130400/S040200) flowing
// through registry.Register exactly as for any catalog tool. This seam
// only adds the per-dispatch handle binding — the same shape as
// RegisterStateful's store binding (S130500 §07) — so the thought
// loop itself stays hidden from tool callers behind the public queue
// surface.
//
// Discipline: queue-attached handlers observe the queue through
// snapshots (GetThoughts/GetThought return clones the queue no longer
// mutates) and MUST NOT re-enter the queue from an event handler while
// q.mu is held — the queue emits events outside the lock and the same
// rule governs the metrics collector (#280).
package thought

import (
	"context"
	"errors"
	"fmt"

	"github.com/flintwillow-software/opennavi/tools/registry"
)

// QueueHandle is the per-dispatch handle handed to queue-attached tool
// handlers: the navi's own queue, bound at registration time.
type QueueHandle struct {
	// Queue is the thought queue this registration's tools operate on.
	Queue *Queue
}

// QueueHandler executes a queue-attached tool call. Args arrive
// pre-validated against the definition's InputSchema as a decoded JSON
// object, exactly like registry.Handler; handle carries the queue for
// the navi this registration was bound to.
type QueueHandler func(ctx context.Context, args map[string]any, handle QueueHandle) (string, error)

// QueueTool pairs a complete ToolDefinition with its queue-attached
// handler for RegisterQueueTools.
type QueueTool struct {
	Def     registry.ToolDefinition
	Handler QueueHandler
}

// RegisterQueueTools registers queue-attached tools on reg: every
// definition is validated exactly like a plain registration (complete
// metadata descriptor, namespace_action names, duplicates rejected)
// and its handler receives the QueueHandle on every dispatch. A nil
// registry or queue handle rejects the whole batch; handlers must be
// provided per tool. The registration returns at the first error and
// earlier tools stay registered (the registry applies/validates each
// Register independently, like RegisterStateful).
func RegisterQueueTools(reg *registry.Registry, handle QueueHandle, tools []QueueTool, opts ...registry.Option) error {
	if reg == nil {
		return errors.New("thought: queue-tool registration: registry is required")
	}
	if handle.Queue == nil {
		return errors.New("thought: queue-tool registration: queue handle is required")
	}
	for _, tool := range tools {
		if tool.Handler == nil {
			return fmt.Errorf("register %q: handler is required", tool.Def.Name)
		}
		// Bind the handle into the plain Handler signature so storage,
		// dedup, and dispatch behave identically to any catalog tool
		// from here on.
		h := tool.Handler
		bound := func(ctx context.Context, args map[string]any) (string, error) {
			return h(ctx, args, handle)
		}
		if err := reg.Register(tool.Def, bound, opts...); err != nil {
			return err
		}
	}
	return nil
}
