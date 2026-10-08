package navi

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// This file is the write path of the typed-object surface (ADR 0017 decision
// 4): the argument validation the surface performs before dispatch, and the
// per-type action handlers the navi's own aggregates register. The surface
// owns authorization and schema enforcement; a handler only applies the
// mutation through the view its type was bound to.

// validateActionArgs enforces a declared action's argument schema with the
// same pragmatic JSON Schema subset a tool call is checked against. A nil
// schema means the action takes no arguments.
func validateActionArgs(a object.Action, args map[string]any) error {
	if len(a.Args) == 0 {
		return nil
	}
	if err := registry.ValidateArgs(a.Args, args); err != nil {
		return fmt.Errorf("%w: %s: %v", object.ErrInvalidArgs, a.Name, err)
	}
	return nil
}

// ── thought ─────────────────────────────────────────────────────────────

// act dispatches a thought's declared actions to the thought system. The
// surface has already authorized the caller and validated the arguments.
func (p *thoughtProvider) act(id, action string, args map[string]any) error {
	if p.n.Thoughts == nil {
		return fmt.Errorf("%w: no thought system", object.ErrUnsupported)
	}
	switch action {
	case "steer":
		message, _ := args["message"].(string)
		return p.n.Thoughts.RouteMessage(id, message)
	case "pause":
		return p.n.Thoughts.Pause(id)
	case "resume":
		return p.n.Thoughts.Resume(id)
	case "cancel":
		return p.n.Thoughts.Cancel(id)
	case "bump":
		priority, err := intArg(args, "priority")
		if err != nil {
			return fmt.Errorf("%w: bump: %v", object.ErrInvalidArgs, err)
		}
		return p.n.Thoughts.BumpPriority(id, priority)
	case "retry":
		return p.n.Thoughts.Retry(id)
	default:
		return fmt.Errorf("%w: thought.%s", object.ErrUnknownAction, action)
	}
}

// ── todo ────────────────────────────────────────────────────────────────

// act applies a todo's declared actions through the stateful view the type
// was registered with, scoped to the navi identity (ADR 0017 decision 8).
func (p *todoProvider) act(id, action string, args map[string]any) error {
	if p.view == nil {
		return fmt.Errorf("%w: no stateful view bound", object.ErrUnsupported)
	}
	update := datastore.TodoUpdate{ID: id}
	switch action {
	case "complete":
		status := datastore.TodoDone
		update.Status = &status
	case "start":
		status := datastore.TodoInProgress
		update.Status = &status
	case "remove":
		update.Remove = true
	default:
		return fmt.Errorf("%w: todo.%s", object.ErrUnknownAction, action)
	}
	if _, err := p.view.UpdateTodos(p.naviID, []datastore.TodoUpdate{update}); err != nil {
		if errors.Is(err, datastore.ErrTodoNotFound) {
			return fmt.Errorf("%w: todo %s", object.ErrNotFound, id)
		}
		return err
	}
	return nil
}

// ── note ────────────────────────────────────────────────────────────────

// act applies a note's declared actions through the stateful view, scoped to
// the navi identity.
func (p *noteProvider) act(id, action string, args map[string]any) error {
	if p.view == nil {
		return fmt.Errorf("%w: no stateful view bound", object.ErrUnsupported)
	}
	switch action {
	case "remove":
		if err := p.view.RemoveNote(p.naviID, id); err != nil {
			if errors.Is(err, datastore.ErrNoteNotFound) {
				return fmt.Errorf("%w: note %s", object.ErrNotFound, id)
			}
			return err
		}
		return nil
	default:
		return fmt.Errorf("%w: note.%s", object.ErrUnknownAction, action)
	}
}

// intArg reads an integer action argument, accepting the numeric shapes a
// JSON decode and a Go caller produce.
func intArg(args map[string]any, key string) (int, error) {
	raw, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("%s must be an integer (got %T)", key, raw)
	}
}
