// Package note implements the note tools: each
// navi's durable verbatim scratchpad, deliberately distinct from
// semantic memory. `note_read` is a pure read — by id, by
// tag, or by literal substring on title/body (never semantic); its
// output bodies are truncated at the note size cap with a visible
// notice. `note_write` stores a note verbatim, assigning a new id when
// omitted, updating the existing note when the id matches, or removing
// it when `remove` is set — rejecting bodies over the per-note cap
// and writes that would push the navi past its total storage cap,
// leaving existing notes untouched on rejection.
//
// Both tools register through `registry.RegisterStateful`,
// so handlers receive a per-navi `StateContext` on every
// dispatch and state persists durably, scoped per navi identity.
package note

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// DefaultMaxBodyBytes is the per-note body cap: 64 KiB.
// note_write rejects larger bodies; note_read truncates any stored
// body over the cap and marks the result truncated.
const DefaultMaxBodyBytes = 64 * 1024

// DefaultMaxNaviBytes is the per-navi total body-storage cap: 10 MiB.
// Writes that would push the navi's stored note bytes over the cap are
// rejected and nothing is written.
const DefaultMaxNaviBytes = 10 * 1024 * 1024

// TruncationNotice is appended to a body cut at the size cap so the
// caller can see the returned body was shortened.
const TruncationNotice = "\n…[truncated at note size cap]"

// Options tunes the note tools at registration. Both caps are the
// config hook this package exposes: the orchestrator converges them
// with runtime configuration (internal/config) when that surface
// lands. A zero field keeps the default cap.
type Options struct {
	// MaxBodyBytes caps one note body; larger bodies are rejected by
	// note_write. Zero = DefaultMaxBodyBytes.
	MaxBodyBytes int
	// MaxNaviBytes caps the navi's total stored note body bytes.
	// Zero = DefaultMaxNaviBytes.
	MaxNaviBytes int
}

// ReadSchema / WriteSchema are the input schemas:
// note_read takes at most one of id/tag/query plus an optional limit;
// note_write takes id/tags/remove optionally and title/body for
// create/update — but the REMOVE scenario ("calls note_write with that
// id and remove: true") must not require title/body, so their presence
// is enforced by the handler instead of the schema. Both schemas are
// strict: unknown keys are rejected.
var ReadSchemaJSON = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"tag":{"type":"string"},"query":{"type":"string"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)
var WriteSchemaJSON = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"remove":{"type":"boolean"}},"additionalProperties":false}`)

// mergeOptions folds variadic Options into one effective value: a
// zero field is skipped and taken from the default caps.
func mergeOptions(opts ...Options) Options {
	o := Options{}
	for _, x := range opts {
		if x.MaxBodyBytes > 0 {
			o.MaxBodyBytes = x.MaxBodyBytes
		}
		if x.MaxNaviBytes > 0 {
			o.MaxNaviBytes = x.MaxNaviBytes
		}
	}
	if o.MaxBodyBytes == 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.MaxNaviBytes == 0 {
		o.MaxNaviBytes = DefaultMaxNaviBytes
	}
	return o
}

// FormatTime is the JSON-facing representation of store timestamps
// (RFC 3339 nano in UTC — the format the SQLite layer persists).
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Register adds note_read and note_write to reg, bound to naviID
// through the stateful store state. opts
// override the note caps (last one wins). Registration fails cleanly
// when the registry, navi identity, or store handle is missing.
func Register(reg *registry.Registry, naviID string, state datastore.ToolState, opts ...Options) error {
	if reg == nil {
		return fmt.Errorf("note register: registry is required")
	}
	if naviID == "" {
		return fmt.Errorf("note register: navi identity is required")
	}
	if state == nil {
		return fmt.Errorf("note register: stateful store handle is required")
	}
	o := mergeOptions(opts...)

	readDef, writeDef, err := definitions()
	if err != nil {
		return err
	}
	if err := reg.RegisterStateful(readDef, naviID, state, noteReadHandler(o.MaxBodyBytes)); err != nil {
		return err
	}
	return reg.RegisterStateful(writeDef, naviID, state, noteWriteHandler(o.MaxBodyBytes, o.MaxNaviBytes))
}

// definitions builds the note_read and note_write descriptors.
func definitions() (registry.ToolDefinition, registry.ToolDefinition, error) {
	readSchema, err := registry.ParseSchema(ReadSchemaJSON)
	if err != nil {
		return registry.ToolDefinition{}, registry.ToolDefinition{}, fmt.Errorf("note: note_read schema: %w", err)
	}
	writeSchema, err := registry.ParseSchema(WriteSchemaJSON)
	if err != nil {
		return registry.ToolDefinition{}, registry.ToolDefinition{}, fmt.Errorf("note: note_write schema: %w", err)
	}
	read := registry.ToolDefinition{
		Name:          "note_read",
		Description:   "Reads notes from the navi's durable verbatim scratchpad: get one by id, list by tag, or search title/body by literal substring. Not semantic — notes are verbatim, semantic recall belongs to memory. Bodies over the note size cap are truncated with a notice.",
		InputSchema:   readSchema,
		OutputFormat:  "json",
		Access:        registry.AccessReadOnly,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"note", "utility"},
		PromptSnippet: "note_read: read notes from the scratchpad (by id, tag, or literal substring)",
		PromptGuidelines: []string{
			"Provide at most one of id, tag, query per call.",
			"Searches are literal substrings, not semantic; use memory for recall.",
			"Bodies may be truncated at the note size cap (truncated: true).",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK, Path: "tool/note/note.go"},
	}
	write := registry.ToolDefinition{
		Name:          "note_write",
		Description:   "Stores a note in the navi's durable verbatim scratchpad: omit id to create (a new id is assigned), pass an existing id to update it, or set remove: true to delete the note with that id. Bodies over the per-note cap and writes past the per-navi total cap are rejected; existing notes stay unchanged on rejection.",
		InputSchema:   writeSchema,
		OutputFormat:  "json",
		Access:        registry.AccessWrite,
		Timeout:       10 * time.Second,
		Recovery:      "atomic_transaction",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"note", "utility"},
		PromptSnippet: "note_write: create, update, or remove notes in the scratchpad",
		PromptGuidelines: []string{
			"Store verbatim text, not summaries — notes are a scratchpad, memory does recall.",
			"Use remove: true with an existing id to delete a note.",
			"Bodies are capped at 64 KiB per note and 10 MiB total per navi.",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK, Path: "tool/note/note.go"},
	}
	return read, write, nil
}

// Tools returns note_read and note_write as public component tools bound
// to naviID and state — the collection form of Register for the navi
// composition root.
func Tools(naviID string, state datastore.ToolState, opts ...Options) ([]tool.Tool, error) {
	if naviID == "" {
		return nil, fmt.Errorf("note: navi identity is required")
	}
	if state == nil {
		return nil, fmt.Errorf("note: stateful store handle is required")
	}
	o := mergeOptions(opts...)
	readDef, writeDef, err := definitions()
	if err != nil {
		return nil, err
	}
	stateCtx := registry.StateContext{NaviID: naviID, Store: state}
	pairs := []struct {
		def     registry.ToolDefinition
		handler registry.StatefulHandler
	}{
		{readDef, noteReadHandler(o.MaxBodyBytes)},
		{writeDef, noteWriteHandler(o.MaxBodyBytes, o.MaxNaviBytes)},
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

// truncateBody cuts body at maxBytes (bytes) and appends
// TruncationNotice; the original body is returned untouched when it
// fits. Splitting is byte-level — the appended notice marks the cut.
func truncateBody(body string, maxBytes int) string {
	if maxBytes <= 0 || len(body) <= maxBytes {
		return body
	}
	return body[:maxBytes] + TruncationNotice
}

// noteReadHandler returns the note_read handler bound to the read cap.
func noteReadHandler(maxBody int) registry.StatefulHandler {
	return func(ctx context.Context, args map[string]any, state registry.StateContext) (string, error) {
		id, tag, query := "", "", ""
		if v, ok := args["id"].(string); ok {
			id = v
		}
		if v, ok := args["tag"].(string); ok {
			tag = v
		}
		if v, ok := args["query"].(string); ok {
			query = v
		}
		limit := 0
		if v, ok := args["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}

		selectors := 0
		if id != "" {
			selectors++
		}
		if tag != "" {
			selectors++
		}
		if query != "" {
			selectors++
		}
		if selectors > 1 {
			return "", fmt.Errorf("note_read: provide at most one of id, tag, query")
		}

		notes, err := state.Store.ReadNotes(state.NaviID, datastore.NoteQuery{ID: id, Tag: tag, Query: query, Limit: limit})
		if err != nil {
			return "", fmt.Errorf("note_read: %w", err)
		}

		out := []map[string]any{}
		for _, n := range notes {
			body := n.Body
			truncated := false
			if len(body) > maxBody {
				body = truncateBody(body, maxBody)
				truncated = true
			}
			out = append(out, map[string]any{
				"id":         n.ID,
				"title":      n.Title,
				"body":       body,
				"tags":       n.Tags,
				"created_at": FormatTime(n.CreatedAt),
				"updated_at": FormatTime(n.UpdatedAt),
				"truncated":  truncated,
			})
		}

		blob, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("note_read: encode output: %w", err)
		}
		return string(blob), nil
	}
}

// noteWriteHandler returns the note_write handler bound to the caps.
func noteWriteHandler(maxBody, maxNavi int) registry.StatefulHandler {
	return func(ctx context.Context, args map[string]any, state registry.StateContext) (string, error) {
		id := ""
		if v, ok := args["id"].(string); ok {
			id = v
		}
		remove := false
		if v, ok := args["remove"].(bool); ok {
			remove = v
		}

		if remove {
			if id == "" {
				return "", fmt.Errorf("note_write: remove requires an id")
			}
			err := state.Store.RemoveNote(state.NaviID, id)
			if errors.Is(err, datastore.ErrNoteNotFound) {
				return "", fmt.Errorf("note_write: note %q not found", id)
			}
			if err != nil {
				return "", fmt.Errorf("note_write: %w", err)
			}
			blob, err := json.Marshal(map[string]any{"removed": true, "id": id})
			if err != nil {
				return "", fmt.Errorf("note_write: encode output: %w", err)
			}
			return string(blob), nil
		}

		title, body := "", ""
		if v, ok := args["title"].(string); ok {
			title = v
		}
		if v, ok := args["body"].(string); ok {
			body = v
		}
		tags := []string{}
		if v, ok := args["tags"].([]any); ok {
			for _, tag := range v {
				s, ok := tag.(string)
				if ok {
					tags = append(tags, s)
				}
			}
		}

		if !remove && (title == "" || body == "") {
			return "", fmt.Errorf("note_write: title and body are required unless remove is set")
		}
		if remove && id == "" {
			return "", fmt.Errorf("note_write: remove requires an id")
		}
		if maxBody > 0 && len(body) > maxBody {
			return "", fmt.Errorf("note_write: body is %d bytes; the per-note cap is %d bytes", len(body), maxBody)
		}

		// Per-navi total cap: account for the note being replaced so
		// updates do not double-count. The check is read-then-write;
		// each store call is atomic, so a rejected write leaves every
		// existing note unchanged (body-cap scenario).
		current, err := state.Store.ReadNotes(state.NaviID, datastore.NoteQuery{Limit: 0})
		if err != nil {
			return "", fmt.Errorf("note_write: %w", err)
		}
		total := 0
		for _, n := range current {
			if n.ID == id {
				continue
			}
			total += len(n.Body)
		}
		if maxNavi > 0 && total+len(body) > maxNavi {
			return "", fmt.Errorf("note_write: note storage would reach %d bytes; the per-navi cap is %d bytes", total+len(body), maxNavi)
		}

		persisted, err := state.Store.UpsertNote(state.NaviID, datastore.Note{ID: id, NaviID: state.NaviID, Title: title, Body: body, Tags: tags})
		if err != nil {
			return "", fmt.Errorf("note_write: %w", err)
		}

		blob, err := json.Marshal(map[string]any{
			"id":         persisted.ID,
			"title":      persisted.Title,
			"body":       persisted.Body,
			"tags":       persisted.Tags,
			"created_at": FormatTime(persisted.CreatedAt),
			"updated_at": FormatTime(persisted.UpdatedAt),
		})
		if err != nil {
			return "", fmt.Errorf("note_write: encode output: %w", err)
		}
		return string(blob), nil
	}
}
