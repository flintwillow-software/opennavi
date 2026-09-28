// Package thoughts implements the thought-management tools
// the navi's door into its own thought queue —
// observe what is queued (thoughts_read), schedule follow-up or
// deferred work (thoughts_create), reshape queued work
// (thoughts_update), and remove records (thoughts_delete).
//
// The tools are queue-attached catalog tools: registered through
// thought.RegisterQueueTools, every handler receives
// the navi's own queue handle per dispatch and operates through the
// queue's public API only — mutations never touch GetThought clones
// (the queue owns its records). Snapshot reads (GetThoughts/GetThought)
// observe; lifecycle mutations go through BumpPriority/Pause/Resume/
// Cancel/Delete.
//
// Safety rails: thoughts created through
// thoughts_create are tagged `origin: tool` so self-directed work is
// distinguishable from connector-sourced work, and the queue clamps
// their priority to the conservative band (EffectiveSelfPriority —
// self-scheduled work never preempts external work). Priority updates
// apply the same clamp for origin-tagged thoughts.
package thoughts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// promptSummaryMax caps the prompt summary in thoughts_read output
// ("prompt summary" — full prompts can be large).
const promptSummaryMax = 200

// Definitions returns the complete ToolDefinition descriptors for the
// four thought-management tools.
func Definitions() []registry.ToolDefinition {
	return []registry.ToolDefinition{readDef(), createDef(), updateDef(), deleteDef()}
}

// Register registers all four tools on reg, bound to the queue the
// navi runs on (queue-attached registration).
func Register(reg *registry.Registry, q *thought.Queue) error {
	if reg == nil {
		return errors.New("thoughts: registry is required")
	}
	if q == nil {
		return errors.New("thoughts: queue is required")
	}
	handle := thought.QueueHandle{Queue: q}
	tools := []thought.QueueTool{
		{Def: readDef(), Handler: handleRead},
		{Def: createDef(), Handler: handleCreate},
		{Def: updateDef(), Handler: handleUpdate},
		{Def: deleteDef(), Handler: handleDelete},
	}
	return thought.RegisterQueueTools(reg, handle, tools)
}

// Tools returns the four thought-management tools as public component
// tools bound to the navi's queue — the collection form of Register for
// the navi composition root.
func Tools(q *thought.Queue) ([]tool.Tool, error) {
	if q == nil {
		return nil, errors.New("thoughts: queue is required")
	}
	handle := thought.QueueHandle{Queue: q}
	pairs := []thought.QueueTool{
		{Def: readDef(), Handler: handleRead},
		{Def: createDef(), Handler: handleCreate},
		{Def: updateDef(), Handler: handleUpdate},
		{Def: deleteDef(), Handler: handleDelete},
	}
	out := make([]tool.Tool, 0, len(pairs))
	for _, pair := range pairs {
		def, err := registry.ToDef(pair.Def)
		if err != nil {
			return nil, err
		}
		h := pair.Handler
		out = append(out, tool.Tool{
			Def: def,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return h(ctx, args, handle)
			},
		})
	}
	return out, nil
}

func readDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"status": { "type": "string", "enum": ["pending", "running", "paused", "complete", "failed", "cancelled"] },
			"limit": { "type": "integer", "minimum": 1 }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "thoughts_read",
		Description:   "Returns a snapshot of the navi's thought queue: id, status, priority, prompt summary, timestamps, and metadata per thought, ordered by priority then most recent activity. Optionally filter by lifecycle status and cap the result count.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"thoughts", "queue", "observability"},
		PromptSnippet: "thoughts_read — snapshot of the thought queue (filter by status, limit count)",
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

func createDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["prompt"],
		"properties": {
			"prompt": { "type": "string", "minLength": 1 },
			"context": { "type": "string" },
			"metadata": {
				"type": "object",
				"additionalProperties": { "type": "string" }
			},
			"priority": { "type": "integer" },
			"defer_until": { "type": "string", "format": "date-time" }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "thoughts_create",
		Description:   "Enqueues a new durable thought for the navi: prompt (required), optional context, metadata, and priority, plus an optional defer_until timestamp (RFC 3339) — deferred thoughts are queued immediately but not claimable until they are due, surviving restarts. Self-scheduled work is tagged origin:tool and its priority clamped to the conservative band (it never preempts external work). One-shot deferral only — recurring schedules belong to cron.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"thoughts", "queue", "scheduling"},
		PromptSnippet: "thoughts_create — enqueue a durable follow-up thought (optional defer_until, clamped priority)",
		PromptGuidelines: []string{
			"Priority is clamped to the conservative band for self-scheduled thoughts — urgent external work always wins.",
			"defer_until defers one-shot work; recurring schedules belong to the cron mechanism.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

func updateDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["id"],
		"properties": {
			"id": { "type": "string", "minLength": 1 },
			"priority": { "type": "integer" },
			"status": { "type": "string", "enum": ["pause", "resume", "cancel"] },
			"context_append": { "type": "string", "minLength": 1 }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "thoughts_update",
		Description:   "Applies partial updates to a queued thought by id: priority change (clamped for self-scheduled work, same reordering/preemption rules as the queue's BumpPriority), lifecycle transitions (pause a running thought — park-with-checkpoint; resume a paused one; cancel a non-terminal one), and context append. Omitted fields are left unchanged.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"thoughts", "queue", "lifecycle"},
		PromptSnippet: "thoughts_update — reshape a queued thought (priority, pause/resume/cancel, context append)",
		PromptGuidelines: []string{
			"Cancel requires the thought to be pending or paused — running thoughts must be paused first.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

func deleteDef() registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["id"],
		"properties": {
			"id": { "type": "string", "minLength": 1 }
		},
		"additionalProperties": false
	}`))
	return registry.ToolDefinition{
		Name:          "thoughts_delete",
		Description:   "Removes a queued thought record entirely by id (from the queue and its persistence). A running thought cannot be deleted in place — cancel it via thoughts_update first; terminal records are kept for inspection.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"thoughts", "queue", "lifecycle"},
		PromptSnippet: "thoughts_delete — remove a queued thought record by id (running thoughts protected)",
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginSDK},
	}
}

// ── handlers ─────────────────────────────────────────────────────────

// handleRead implements thoughts_read: queue snapshot,
// optional status filter, ordered by priority then most recent
// activity.
func handleRead(ctx context.Context, args map[string]any, handle thought.QueueHandle) (string, error) {
	status := thought.ThoughtStatus("")
	if raw, ok := args["status"]; ok {
		s, ok := raw.(string)
		if !ok || s == "" {
			return "", errors.New("thoughts_read: status must be a non-empty string")
		}
		status = thought.ThoughtStatus(s)
	}
	limit := 0
	if raw, ok := args["limit"]; ok {
		n, err := parseIntArg(raw)
		if err != nil {
			return "", fmt.Errorf("thoughts_read: invalid limit: %w", err)
		}
		limit = n
	}

	// Snapshot read — clones, safe under the queue's discipline.
	all := handle.Queue.GetThoughts()
	var out []thought.Thought
	for _, t := range all {
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return marshalThoughts(out)
}

// handleCreate implements thoughts_create: enqueue a durable
// thought tagged origin:tool — the queue clamps self-scheduled
// priority at Enqueue. Optional defer_until makes the
// thought durable-but-not-claimable until the timestamp passes.
func handleCreate(ctx context.Context, args map[string]any, handle thought.QueueHandle) (string, error) {
	prompt, err := stringField(args, "prompt")
	if err != nil {
		return "", fmt.Errorf("thoughts_create: %w", err)
	}
	t := &thought.Thought{
		ID:        newThoughtID(),
		Prompt:    prompt,
		Priority:  4, // normal band; Enqueue clamps origin-tagged work
		Status:    thought.ThoughtPending,
		Metadata:  map[string]string{"origin": thought.OriginTool},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if raw, ok := args["context"]; ok {
		s, ok := raw.(string)
		if !ok {
			return "", errors.New("thoughts_create: context must be a string")
		}
		t.Context = s
	}
	if raw, ok := args["metadata"]; ok {
		obj, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("thoughts_create: metadata must be an object")
		}
		for k, v := range obj {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("thoughts_create: metadata %q must be a string value", k)
			}
			if k == "origin" {
				continue // the safety rail's origin tag is not overridable
			}
			t.Metadata[k] = s
		}
	}
	if raw, ok := args["priority"]; ok {
		p, err := parseIntArg(raw)
		if err != nil {
			return "", fmt.Errorf("thoughts_create: invalid priority: %w", err)
		}
		t.Priority = p
	}
	if raw, ok := args["defer_until"]; ok {
		s, ok := raw.(string)
		if !ok || s == "" {
			return "", errors.New("thoughts_create: defer_until must be an RFC 3339 timestamp string")
		}
		due, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "", fmt.Errorf("thoughts_create: defer_until must be RFC 3339 (e.g. 2026-09-23T12:00:00Z): %w", err)
		}
		t.DeferUntil = due
	}
	if err := handle.Queue.Enqueue(t); err != nil {
		return "", fmt.Errorf("thoughts_create: enqueue: %w", err)
	}
	return marshalCreated(t)
}

// handleUpdate implements thoughts_update: partial updates
// by id — clamped priority, lifecycle transitions, context append.
// Mutations go through the queue's public API; snapshot reads provide
// the values the clamp needs (GetThought returns clones).
func handleUpdate(ctx context.Context, args map[string]any, handle thought.QueueHandle) (string, error) {
	id, err := stringField(args, "id")
	if err != nil {
		return "", fmt.Errorf("thoughts_update: %w", err)
	}
	q := handle.Queue

	updated := false
	if raw, ok := args["priority"]; ok {
		p, err := parseIntArg(raw)
		if err != nil {
			return "", fmt.Errorf("thoughts_update: invalid priority: %w", err)
		}
		// Clamp: self-scheduled work never outranks
		// external work. Apply the same band Enqueue applies.
		if snap, gerr := q.GetThought(id); gerr == nil {
			if origin, ok := snap.Metadata["origin"]; ok && (origin == thought.OriginSelf || origin == thought.OriginTool) {
				if p < thought.SelfScheduledHighBound {
					p = thought.SelfScheduledHighBound
				}
			}
		}
		if err := q.BumpPriority(id, p); err != nil {
			return "", fmt.Errorf("thoughts_update: priority: %w", err)
		}
		updated = true
	}
	if raw, ok := args["status"]; ok {
		s, ok := raw.(string)
		if !ok {
			return "", errors.New("thoughts_update: status must be a string")
		}
		switch s {
		case "pause":
			// Park-with-checkpoint semantics apply; the
			// checkpoint is supplied by the runner's cooperative
			// park path. Only running thoughts pause — the queue
			// rejects others with the lifecycle error.
			if err := q.PauseWithCheckpoint(id, nil); err != nil {
				return "", fmt.Errorf("thoughts_update: pause: %w", err)
			}
		case "resume":
			if err := q.Resume(id); err != nil {
				return "", fmt.Errorf("thoughts_update: resume: %w", err)
			}
		case "cancel":
			if err := q.Cancel(id); err != nil {
				return "", fmt.Errorf("thoughts_update: cancel: %w", err)
			}
		default:
			return "", fmt.Errorf("thoughts_update: status must be one of pause, resume, cancel (got %q)", s)
		}
		updated = true
	}
	if raw, ok := args["context_append"]; ok {
		s, ok := raw.(string)
		if !ok || s == "" {
			return "", errors.New("thoughts_update: context_append must be a non-empty string")
		}
		if err := q.AppendContext(id, s); err != nil {
			return "", fmt.Errorf("thoughts_update: context: %w", err)
		}
		updated = true
	}
	if !updated {
		return "", errors.New("thoughts_update: at least one of priority, status, or context_append is required")
	}

	snap, gerr := q.GetThought(id)
	if gerr != nil {
		return "", fmt.Errorf("thoughts_update: %w", gerr)
	}
	return marshalSnapshotted(snap)
}

// handleDelete implements thoughts_delete: record removal —
// running thoughts protected (cancel first via thoughts_update).
func handleDelete(ctx context.Context, args map[string]any, handle thought.QueueHandle) (string, error) {
	id, err := stringField(args, "id")
	if err != nil {
		return "", fmt.Errorf("thoughts_delete: %w", err)
	}
	if err := handle.Queue.Delete(id); err != nil {
		return "", fmt.Errorf("thoughts_delete: %w", err)
	}
	return `{"deleted":` + marshalString(id) + `}`, nil
}

// ── helpers ──────────────────────────────────────────────────────────

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
		return strconv.Atoi(v.String())
	default:
		return 0, fmt.Errorf("must be an integer (got %T)", raw)
	}
}

// newThoughtID generates a random 128-bit hex id for tool-created
// thoughts (the queue requires non-empty ids; no uuid dependency).
func newThoughtID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("thoughts: generate id: %v", err))
	}
	return "self-" + hex.EncodeToString(b[:])
}

func marshalString(s string) string {
	blob, _ := json.Marshal(s)
	return string(blob)
}

// marshalThoughts renders the read snapshot: prompt summarized
// timestamps, metadata, defer_until when set.
func marshalThoughts(items []thought.Thought) (string, error) {
	views := make([]thoughtView, 0, len(items))
	for _, t := range items {
		views = append(views, view(&t))
	}
	if views == nil {
		views = []thoughtView{}
	}
	blob, err := json.Marshal(views)
	if err != nil {
		return "", fmt.Errorf("thoughts: encode output: %w", err)
	}
	return string(blob), nil
}

type thoughtView struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	Priority   int               `json:"priority"`
	Prompt     string            `json:"prompt"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	DeferUntil string            `json:"defer_until,omitempty"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
}

func view(t *thought.Thought) thoughtView {
	return thoughtView{
		ID:         t.ID,
		Status:     string(t.Status),
		Priority:   t.Priority,
		Prompt:     summarize(t.Prompt, promptSummaryMax),
		Metadata:   t.Metadata,
		DeferUntil: deferStamp(t.DeferUntil),
		CreatedAt:  t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  t.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func marshalCreated(t *thought.Thought) (string, error) {
	v := view(t)
	blob, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("thoughts: encode output: %w", err)
	}
	return string(blob), nil
}

func marshalSnapshotted(t *thought.Thought) (string, error) {
	return marshalCreated(t)
}

func summarize(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// deferStamp renders the not-before timestamp (empty when not
// deferred — a real zero time means immediately claimable).
func deferStamp(due time.Time) string {
	if due.IsZero() {
		return ""
	}
	return due.UTC().Format(time.RFC3339)
}
