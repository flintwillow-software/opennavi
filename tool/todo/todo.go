// Package todo implements the durable todo tools:
// `todo_read`, `todo_write`, and `todo_update`. The tools give a navi
// a persistent plan — read the current items, atomically replace the
// whole plan, or apply partial updates by id.
//
// All three register through tool/registry.RegisterStateful: each
// handler receives a per-navi store handle at dispatch and persists
// through store.StatefulToolStore, so todo state survives restarts
// and is scoped per navi identity. Register the package with
// RegisterTools (or one tool with RegisterOne) on a bound registry.
package todo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// Statuses accepted by todo_read's optional status filter and
// todo_update's status transitions.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

// Definitions returns the complete ToolDefinition descriptors for the
// three todo tools. Handlers are bound to the given
// registry at registration time.
func Definitions() []registry.ToolDefinition {
	return []registry.ToolDefinition{readDef(), writeDef(), updateDef()}
}

// RegisterTools registers all three todo tools on reg for the navi
// identity naviID, persisting through state (seam).
func RegisterTools(reg *registry.Registry, naviID string, state store.StatefulToolStore) error {
	if reg == nil {
		return errors.New("todo: registry is required")
	}
	if state == nil {
		return errors.New("todo: stateful tool store is required")
	}
	for _, tool := range []struct {
		def     registry.ToolDefinition
		handler registry.StatefulHandler
	}{
		{readDef(), handleRead},
		{writeDef(), handleWrite},
		{updateDef(), handleUpdate},
	} {
		if err := reg.RegisterStateful(tool.def, naviID, state, tool.handler); err != nil {
			return fmt.Errorf("todo: register %s: %w", tool.def.Name, err)
		}
	}
	return nil
}

// RegisterOne registers a single todo tool by name on reg — for
// selective activation (a navi may enable todo_read without the
// writers).
func RegisterOne(reg *registry.Registry, naviID string, state store.StatefulToolStore, name string) error {
	switch name {
	case "todo_read":
		return reg.RegisterStateful(readDef(), naviID, state, handleRead)
	case "todo_write":
		return reg.RegisterStateful(writeDef(), naviID, state, handleWrite)
	case "todo_update":
		return reg.RegisterStateful(updateDef(), naviID, state, handleUpdate)
	default:
		return fmt.Errorf("todo: unknown tool %q (want todo_read, todo_write, or todo_update)", name)
	}
}

// Tools returns the three todo tools as public component tools bound to
// naviID and state — the collection form of RegisterTools for the navi
// composition root.
func Tools(naviID string, state store.StatefulToolStore) ([]tool.Tool, error) {
	if naviID == "" {
		return nil, errors.New("todo: navi identity is required")
	}
	if state == nil {
		return nil, errors.New("todo: stateful tool store is required")
	}
	stateCtx := registry.StateContext{NaviID: naviID, Store: state}
	pairs := []struct {
		def     registry.ToolDefinition
		handler registry.StatefulHandler
	}{
		{readDef(), handleRead},
		{writeDef(), handleWrite},
		{updateDef(), handleUpdate},
	}
	out := make([]tool.Tool, 0, len(pairs))
	for _, pair := range pairs {
		def, err := registry.ToDef(pair.def)
		if err != nil {
			return nil, err
		}
		h := pair.handler
		out = append(out, tool.Tool{
			Def: def,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return h(ctx, args, stateCtx)
			},
		})
	}
	return out, nil
}

func readDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"status": { "type": "string", "enum": ["pending", "in_progress", "done"] },
			"limit": { "type": "integer", "minimum": 1 }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "todo_read",
		Description:   "Returns the navi's durable todo list: id, text, status, priority, and timestamps per item, ordered by priority then most recent activity. Optionally filter by status (pending, in_progress, done) and cap the result count.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"todo", "plan", "durability"},
		PromptSnippet: "todo_read — read the navi's durable todo list (filter by status, limit count)",
		PromptGuidelines: []string{
			"Call todo_read before todo_write to preserve items you want to keep — todo_write replaces the ENTIRE list.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

func writeDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["items"],
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["text"],
					"properties": {
						"text": { "type": "string", "minLength": 1 },
						"status": { "type": "string", "enum": ["pending", "in_progress", "done"] },
						"priority": { "type": "integer" }
					},
					"additionalProperties": false
				}
			}
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "todo_write",
		Description:   "Atomically replaces the navi's entire todo list with the given items and returns them with their assigned ids. Any previous list is discarded — include every item you want to keep.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "atomic_rollback",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"todo", "plan", "durability"},
		PromptSnippet: "todo_write — replace the entire todo list atomically (returns assigned ids)",
		PromptGuidelines: []string{
			"todo_write discards all existing items — read the list first and re-include anything still relevant.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

func updateDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["updates"],
		"properties": {
			"updates": {
				"type": "array",
				"minItems": 1,
				"items": {
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": { "type": "string", "minLength": 1 },
						"text": { "type": "string", "minLength": 1 },
						"status": { "type": "string", "enum": ["pending", "in_progress", "done"] },
						"priority": { "type": "integer" },
						"remove": { "type": "boolean" }
					},
					"additionalProperties": false
				}
			}
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "todo_update",
		Description:   "Applies partial updates to todo items by id — status transitions, text edits, priority changes, or removal — atomically. Either every update lands or none does; an unknown id rejects the whole batch.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "atomic_rollback",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"todo", "plan", "durability"},
		PromptSnippet: "todo_update — update/remove todo items by id atomically (batch or none)",
		PromptGuidelines: []string{
			"todo_update applies to existing ids only — todo_read first to learn the ids; use todo_write to replace the whole list.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

// handleRead implements todo_read: list items ordered
// by priority then most recent activity, optionally filtered by
// status, optionally capped.
func handleRead(ctx context.Context, args map[string]any, state registry.StateContext) (string, error) {
	status := store.TodoStatus("")
	if raw, ok := args["status"]; ok {
		s, err := parseStatus(raw)
		if err != nil {
			return "", fmt.Errorf("todo_read: %w", err)
		}
		status = s
	}
	limit := 0
	if raw, ok := args["limit"]; ok {
		n, err := parseIntArg(raw)
		if err != nil {
			return "", fmt.Errorf("todo_read: invalid limit: %w", err)
		}
		limit = n
	}
	items, err := state.Store.ListTodos(state.NaviID, status, limit)
	if err != nil {
		return "", fmt.Errorf("todo_read: %w", err)
	}
	return marshalTodos(items)
}

// handleWrite implements todo_write: atomic full-replace
// of the todo list; returns the persisted items with assigned ids.
func handleWrite(ctx context.Context, args map[string]any, state registry.StateContext) (string, error) {
	rawItems, ok := args["items"].([]any)
	if !ok {
		return "", errors.New("todo_write: items must be an array")
	}
	items := make([]store.Todo, 0, len(rawItems))
	for i, raw := range rawItems {
		obj, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("todo_write: items[%d] must be an object", i)
		}
		item := store.Todo{Status: store.TodoPending}
		text, err := stringField(obj, "text")
		if err != nil {
			return "", fmt.Errorf("todo_write: items[%d]: %w", i, err)
		}
		item.Text = text
		if raw, ok := obj["status"]; ok {
			s, err := parseStatus(raw)
			if err != nil {
				return "", fmt.Errorf("todo_write: items[%d]: %w", i, err)
			}
			item.Status = s
		}
		if raw, ok := obj["priority"]; ok {
			p, err := parseIntArg(raw)
			if err != nil {
				return "", fmt.Errorf("todo_write: items[%d]: invalid priority: %w", i, err)
			}
			item.Priority = p
		}
		items = append(items, item)
	}
	persisted, err := state.Store.ReplaceTodos(state.NaviID, items)
	if err != nil {
		// The store leaves the previous list intact on failure; the
		// failure reason goes back to the LLM as the tool error.
		return "", fmt.Errorf("todo_write: replace failed (previous list intact): %w", err)
	}
	return marshalTodos(persisted)
}

// handleUpdate implements todo_update: partial updates
// by id, applied atomically; an unknown id rejects the whole batch
// (surfaced from the store's ErrTodoNotFound wrap).
func handleUpdate(ctx context.Context, args map[string]any, state registry.StateContext) (string, error) {
	rawUpdates, ok := args["updates"].([]any)
	if !ok || len(rawUpdates) == 0 {
		return "", errors.New("todo_update: updates must be a non-empty array")
	}
	updates := make([]store.TodoUpdate, 0, len(rawUpdates))
	for i, raw := range rawUpdates {
		obj, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("todo_update: updates[%d] must be an object", i)
		}
		u := store.TodoUpdate{}
		id, err := stringField(obj, "id")
		if err != nil {
			return "", fmt.Errorf("todo_update: updates[%d]: %w", i, err)
		}
		u.ID = id
		if raw, ok := obj["text"]; ok {
			s, err := stringField(map[string]any{"text": raw}, "text")
			if err != nil {
				return "", fmt.Errorf("todo_update: updates[%d]: invalid text: %w", i, err)
			}
			u.Text = &s
		}
		if raw, ok := obj["status"]; ok {
			s, err := parseStatus(raw)
			if err != nil {
				return "", fmt.Errorf("todo_update: updates[%d]: %w", i, err)
			}
			u.Status = &s
		}
		if raw, ok := obj["priority"]; ok {
			p, err := parseIntArg(raw)
			if err != nil {
				return "", fmt.Errorf("todo_update: updates[%d]: invalid priority: %w", i, err)
			}
			u.Priority = &p
		}
		if raw, ok := obj["remove"]; ok {
			b, ok := raw.(bool)
			if !ok {
				return "", fmt.Errorf("todo_update: updates[%d]: remove must be a boolean", i)
			}
			u.Remove = b
		}
		updates = append(updates, u)
	}
	items, err := state.Store.UpdateTodos(state.NaviID, updates)
	if err != nil {
		// Atomic batch rejection: the store applied none of the
		// updates; surface the reason (incl. the unknown id) to the
		// LLM.
		return "", fmt.Errorf("todo_update: batch rejected (no changes applied): %w", err)
	}
	return marshalTodos(items)
}

func parseStatus(raw any) (store.TodoStatus, error) {
	s, ok := raw.(string)
	if !ok {
		return "", errors.New("status must be a string")
	}
	st := store.TodoStatus(s)
	if !st.Valid() {
		return "", fmt.Errorf("status must be one of pending, in_progress, done (got %q)", s)
	}
	return st, nil
}

func stringField(obj map[string]any, key string) (string, error) {
	raw, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return s, nil
}

func parseIntArg(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, fmt.Errorf("must be an integer (got %T)", raw)
	}
}

func marshalTodos(items []store.Todo) (string, error) {
	type todoView struct {
		ID        string           `json:"id"`
		Text      string           `json:"text"`
		Status    store.TodoStatus `json:"status"`
		Priority  int              `json:"priority"`
		CreatedAt string           `json:"created_at"`
		UpdatedAt string           `json:"updated_at"`
	}
	if items == nil {
		items = []store.Todo{}
	}
	views := make([]todoView, 0, len(items))
	for _, t := range items {
		views = append(views, todoView{
			ID:        t.ID,
			Text:      t.Text,
			Status:    t.Status,
			Priority:  t.Priority,
			CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	blob, err := json.Marshal(views)
	if err != nil {
		return "", fmt.Errorf("todo: encode output: %w", err)
	}
	return string(blob), nil
}
