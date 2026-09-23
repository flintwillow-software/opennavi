package registry

import (
	"context"
	"fmt"

	"github.com/flintwillow-software/opennavi/store"
)

// StateContext is the per-navi persistence handle handed to stateful
// tool handlers on every dispatch (S130500 §07). It carries the navi
// identity the registration was bound to plus the stateful tool
// store; handlers persist through it and per-navi scoping is enforced
// at the store layer (every store method is navi-filtered).
type StateContext struct {
	// NaviID is the navi identity state is scoped to — the identity
	// this registration was bound to.
	NaviID string
	// Store is the durable stateful tool store backing this
	// registration. State written through it survives restarts.
	Store store.StatefulToolStore
}

// StatefulHandler executes a stateful tool call. Args arrive
// pre-validated against the definition's InputSchema as a decoded
// JSON object, exactly like Handler; state carries the per-navi
// persistence handle.
type StatefulHandler func(ctx context.Context, args map[string]any, state StateContext) (string, error)

// RegisterStateful registers a stateful tool (S130500 §07): the
// handler receives a per-navi store handle on every dispatch, so todo
// and note state persists across restarts and sessions, scoped per
// navi identity. Stateless tools register through Register and are
// unaffected.
//
// The descriptor is validated exactly like Register (complete
// metadata, namespace_action name); duplicates are rejected and the
// original registration stays intact. state and naviID are required —
// a stateful tool without a store handle cannot be registered.
func (r *Registry) RegisterStateful(def ToolDefinition, naviID string, state store.StatefulToolStore, handler StatefulHandler, opts ...Option) error {
	if handler == nil {
		return fmt.Errorf("register %q: handler is required", def.Name)
	}
	if state == nil {
		return fmt.Errorf("register %q: stateful store handle is required", def.Name)
	}
	if naviID == "" {
		return fmt.Errorf("register %q: navi identity is required", def.Name)
	}
	// Wrap the stateful handler into the plain Handler signature so
	// storage, dedup, and dispatch behave identically to stateless
	// tools from here on.
	stateCtx := StateContext{NaviID: naviID, Store: state}
	bound := func(ctx context.Context, args map[string]any) (string, error) {
		return handler(ctx, args, stateCtx)
	}
	return r.Register(def, bound, opts...)
}