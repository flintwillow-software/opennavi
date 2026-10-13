// Package bridge connects the public tool registry to the
// agent runtime's ToolExecutor. The registry owns the LLM-facing tool
// metadata and dispatch enforcement (schema validation, argument
// preparation, active-set membership); the executor owns the run loop's
// tool surface. Bind is the seam between them: it registers the
// registry's active, permitted tools into an executor so the agent loop
// can offer them to the provider, with every call dispatched back
// through the registry.
//
// Binding is a snapshot taken at assembly time. The registry's active
// set is enforced per dispatch (an inactive tool refuses calls even
// while still bound), but structural changes — newly activated tools —
// require a re-bind to appear in the executor's tool list.
//
// Per-tool Mode metadata is not yet enforced by the executor (it runs
// all tools per its own configured mode); per-mode scheduling lands
// with the recovery work.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// Policy is the runtime tool policy (the public counterpart of
// internal/config ToolPolicy): allowed/blocked name lists and per-tool
// timeout overrides applied at bind time. Blocked always wins over
// Allowed; a non-empty Allowed list restricts binding to those tools.
type Policy struct {
	// Allowed, if non-empty, restricts binding to these tool names.
	Allowed []string
	// Blocked tools are never bound (deny wins over Allowed).
	Blocked []string
	// Timeouts overrides a tool's declared Timeout by name.
	Timeouts map[string]time.Duration
}

// Options tunes Bind.
type Options struct {
	// Policy applies allowed/blocked lists and timeout overrides.
	Policy Policy
	// Access, when non-empty, limits binding to tools whose access
	// level is listed (enforcement seam: IAM grants map onto
	// these levels; until IAM lands, callers filter directly).
	Access []registry.ToolAccess
	// Authorize, when non-nil, is consulted before EVERY dispatched call
	// (ADR 0022 decision 5): it returns nil to allow or a refusal error. It is
	// per-call rather than a one-time surface filter, so a revocation takes
	// effect on the next call and a confused-deputy action is refused as the
	// originator rather than as the navi. The composition root supplies it;
	// this package stays free of any policy vocabulary.
	Authorize func(name string) error
}

// Bind registers every active registry tool that passes the Options
// filters into the executor and returns the bound tool names in bind
// order. Each bound call dispatches through reg.Dispatch, so schema
// validation and active-set rules stay in the registry; the executor's
// handler only applies the timeout envelope.
//
// The timeout applies the registry definition's Timeout (or the policy
// override) even against handlers that ignore ctx cancellation: the
// call is aborted with a timeout error while the abandoned handler
// drains into a buffered channel. Hard-kill semantics (recovering
// partial output from the abandoned handler) land with the
// recovery work.
func Bind(ex *agent.ToolExecutor, reg *registry.Registry, opts Options) ([]string, error) {
	if ex == nil {
		return nil, fmt.Errorf("bridge: executor is required")
	}
	if reg == nil {
		return nil, fmt.Errorf("bridge: registry is required")
	}

	blocked := make(map[string]bool, len(opts.Policy.Blocked))
	for _, name := range opts.Policy.Blocked {
		blocked[name] = true
	}
	allowed := make(map[string]bool, len(opts.Policy.Allowed))
	for _, name := range opts.Policy.Allowed {
		allowed[name] = true
	}
	permittedAccess := make(map[registry.ToolAccess]bool, len(opts.Access))
	for _, a := range opts.Access {
		permittedAccess[a] = true
	}

	var bound []string
	for _, name := range reg.ActiveNames() {
		if blocked[name] {
			continue
		}
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		def, ok := reg.Get(name)
		if !ok {
			return nil, fmt.Errorf("bridge: active tool %q has no definition", name)
		}
		if len(permittedAccess) > 0 && !permittedAccess[def.Access] {
			continue
		}

		timeout := def.Timeout
		if override, ok := opts.Policy.Timeouts[name]; ok {
			timeout = override
		}

		ex.RegisterTool(agentTool(def), dispatchHandler(reg, def.Name, timeout, opts.Authorize))
		bound = append(bound, name)
	}
	sort.Strings(bound)
	return bound, nil
}

// agentTool renders a registry definition as the executor's LLM-facing
// descriptor: the schema document becomes the provider-facing
// inputSchema.
func agentTool(def registry.ToolDefinition) agent.AgentTool {
	return agent.AgentTool{
		Name:        def.Name,
		Description: def.Description,
		Parameters:  def.InputSchema.Raw(),
	}
}

// dispatchHandler wraps reg.Dispatch with the per-call authorization check
// and the timeout envelope. The handler's signature matches agent.ToolExecutor's
// (no ctx): the timeout context is built here.
func dispatchHandler(reg *registry.Registry, name string, timeout time.Duration, authorize func(string) error) func(map[string]any) (string, error) {
	return func(args map[string]any) (string, error) {
		// Authorization precedes argument handling: a caller who may not call
		// the tool learns nothing about what it expects, and no work is done
		// on its behalf.
		if authorize != nil {
			if err := authorize(name); err != nil {
				return "", err
			}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("%s: encode arguments: %w", name, err)
		}

		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		// Dispatch runs on its own goroutine so a handler that ignores
		// ctx cancellation cannot wedge the executor: the call returns
		// a timeout error while the abandoned goroutine drains into the
		// buffered channel (no leak — the send never blocks).
		type outcome struct {
			result string
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := reg.Dispatch(ctx, name, raw)
			done <- outcome{result: result, err: err}
		}()
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%s: timed out after %s", name, timeout)
		case out := <-done:
			return out.result, out.err
		}
	}
}
