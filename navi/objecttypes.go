package navi

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/thought"
)

// Names of the navi's built-in object types (ADR 0017).
const (
	// TypeThought is the thought/transcript aggregate.
	TypeThought = "thought"
	// TypeEntry is one transcript entry, reached through the thought type's
	// `transcript` detail collection.
	TypeEntry = "entry"
	// TypeMemory is the memory aggregate.
	TypeMemory = "memory"
	// TypeTrustEvent is the trust ledger: the trust decisions the navi made
	// (ADR 0027). It is read-only and separate from the navi's own state.
	TypeTrustEvent = "trust_event"
)

// DetailTranscript is the thought type's transcript detail collection.
const DetailTranscript = "transcript"

// Page bounds for the built-in providers.
const (
	defaultPage = 100
	maxPage     = 500
)

// builtinTypes returns the registrations for the navi's own aggregates. They
// are exposed by default — the navi owns them, so a portal renders them
// without the author declaring anything — and read through the navi's own
// views, RAM-first (ADR 0017 decisions 3 and 9).
func (n *Navi) builtinTypes() []TypeRegistration {
	thoughts := &thoughtProvider{n: n}
	return []TypeRegistration{
		{Type: thoughtType(), Provider: thoughts, Act: thoughts.act},
		{Type: entryType(), Provider: entryProvider{}},
		{Type: memoryType(), Provider: &memoryProvider{n: n}},
		{Type: trustEventType(), Provider: trustEventProvider{n: n}},
	}
}

// ── thought ─────────────────────────────────────────────────────────────

func thoughtType() object.Type {
	return object.Type{
		Name:  TypeThought,
		Label: "Thought",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString, Section: "identity"},
			{Name: "prompt", Label: "Prompt", Kind: object.KindText, Section: "core"},
			{Name: "context", Label: "Context", Kind: object.KindText, Section: "core"},
			{Name: "status", Label: "Status", Kind: object.KindEnum, Section: "state",
				Enum: []string{"pending", "running", "paused", "complete", "failed", "cancelled"}},
			{Name: "priority", Label: "Priority", Kind: object.KindInt, Section: "state"},
			{Name: "result", Label: "Result", Kind: object.KindText, Section: "state"},
			{Name: "provider", Label: "Provider", Kind: object.KindString, Section: "run"},
			{Name: "model", Label: "Model", Kind: object.KindString, Section: "run"},
			{Name: "mode", Label: "Mode", Kind: object.KindString, Section: "run"},
			{Name: "foreground", Label: "Foreground", Kind: object.KindBool, Section: "run"},
			{Name: "skills", Label: "Skills", Kind: object.KindList, ItemKind: object.KindString, Section: "run"},
			{Name: "depends_on", Label: "Depends on", Kind: object.KindList, ItemKind: object.KindRef,
				RefType: TypeThought, Section: "relations"},
			// Attribution (ADR 0022 decision 2, ADR 0027): who caused this
			// thought and the chain that scheduled it. Read-only and NOT
			// sensitive — the interaction record is what a portal reads, not
			// working state.
			{Name: "origin", Label: "Origin", Kind: object.KindString, Section: "attribution", ReadOnly: true},
			{Name: "lineage", Label: "Lineage", Kind: object.KindList, ItemKind: object.KindRef,
				RefType: TypeThought, Section: "attribution", ReadOnly: true},
			// Derived from the thought's metadata role (the map itself is
			// sensitive): a plan's verification is a different kind of work,
			// and a portal distinguishes it without reading working state.
			{Name: "verification", Label: "Verification", Kind: object.KindBool,
				Section: "run", ReadOnly: true},
			{Name: "defer_until", Label: "Defer until", Kind: object.KindTime, Section: "schedule"},
			{Name: "created_at", Label: "Created", Kind: object.KindTime, Section: "timestamps"},
			{Name: "updated_at", Label: "Updated", Kind: object.KindTime, Section: "timestamps"},
			// Not exposed by default: agent working state, a free-form map,
			// and host paths. The surface omits sensitive fields (ADR 0006/0008).
			{Name: "assembled", Label: "Assembled context", Kind: object.KindText, Section: "internal",
				Sensitive: true, ReadOnly: true},
			{Name: "checkpoint", Label: "Checkpoint", Kind: object.KindJSON, Section: "internal",
				Sensitive: true, ReadOnly: true},
			{Name: "metadata", Label: "Metadata", Kind: object.KindJSON, Section: "internal",
				Sensitive: true, ReadOnly: true},
			{Name: "log_path", Label: "Log path", Kind: object.KindString, Section: "internal",
				Sensitive: true, ReadOnly: true},
		},
		Relations: []object.Relation{
			{Name: "depends_on", Label: "Depends on", Target: TypeThought, Kind: object.RelationMany},
		},
		Details: []object.Detail{
			{Name: DetailTranscript, Label: "Transcript", Type: TypeEntry, Key: "thought_id"},
		},
		Actions: []object.Action{
			{Name: "steer", Label: "Steer", Args: rawSchema(`{
				"type": "object",
				"properties": {"message": {"type": "string", "description": "Message to deliver to the running thought"}},
				"required": ["message"]
			}`)},
			{Name: "pause", Label: "Pause"},
			{Name: "resume", Label: "Resume"},
			{Name: "cancel", Label: "Cancel", Confirm: true},
			{Name: "bump", Label: "Bump priority", Args: rawSchema(`{
				"type": "object",
				"properties": {"priority": {"type": "integer", "minimum": 1, "maximum": 10}},
				"required": ["priority"]
			}`)},
			{Name: "retry", Label: "Retry"},
		},
		Views: []object.View{
			{Kind: object.ViewList, Label: "Thoughts", Columns: []string{"id", "prompt", "status", "priority", "model", "updated_at"}},
			{Kind: object.ViewDetail, Label: "Thought"},
			{Kind: object.ViewChat, Label: "Conversation", Detail: DetailTranscript},
		},
	}
}

// thoughtProvider serves thought objects from the queue: RAM is the source of
// truth (ADR 0011), so a navi with no datastore still serves them. The
// transcript detail falls through to the bound datastore, which is the only
// place history outlives the process.
type thoughtProvider struct{ n *Navi }

func (p *thoughtProvider) List(q object.Query) ([]object.Object, string, error) {
	if p.n.Thoughts == nil {
		return []object.Object{}, "", nil
	}
	all := p.n.Thoughts.GetThoughts()
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	start, end := pageWindow(len(all), q)
	out := make([]object.Object, 0, end-start)
	for _, t := range all[start:end] {
		out = append(out, thoughtObject(t))
	}
	return out, nextCursor(end, len(all)), nil
}

func (p *thoughtProvider) Get(id string) (object.Object, error) {
	if p.n.Thoughts == nil {
		return object.Object{}, object.ErrNotFound
	}
	t, err := p.n.Thoughts.GetThought(id)
	if err != nil {
		return object.Object{}, err
	}
	return thoughtObject(t), nil
}

func (p *thoughtProvider) Detail(id, detail string, q object.Query) ([]object.Object, string, error) {
	if detail != DetailTranscript {
		return nil, "", object.ErrUnknownDetail
	}
	view := p.n.transcripts
	if view == nil {
		return []object.Object{}, "", nil // no datastore: no persisted history
	}
	entries, err := view.LoadTranscript(id)
	if errors.Is(err, datastore.ErrTranscriptNotFound) {
		return []object.Object{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	start, end := pageWindow(len(entries), q)
	out := make([]object.Object, 0, end-start)
	for _, e := range entries[start:end] {
		out = append(out, entryObject(e, id))
	}
	return out, nextCursor(end, len(entries)), nil
}

func thoughtObject(t *thought.Thought) object.Object {
	return object.Object{
		Type:    TypeThought,
		ID:      t.ID,
		Version: t.UpdatedAt.UnixNano(),
		Fields: map[string]any{
			"id": t.ID, "prompt": t.Prompt, "context": t.Context, "result": t.Result,
			"status": string(t.Status), "priority": t.Priority,
			"provider": t.Provider, "model": t.Model, "mode": t.Mode,
			"foreground": t.Foreground, "skills": t.Skills, "depends_on": t.DependsOn,
			// Attribution: the principal that caused the thought and the chain
			// that scheduled it (ADR 0022 decision 2, ADR 0027).
			"origin": t.Origin.String(), "lineage": t.Lineage,
			// Derived, non-sensitive state a portal must not lose: a plan's
			// verification is semantically distinct from a normal thought, and
			// the free-form metadata it is recorded in stays omitted.
			"verification": t.IsVerification(),
			"defer_until":  t.DeferUntil, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
			// Sensitive: present here, stripped by the surface's projection.
			"assembled": t.Assembled, "checkpoint": string(t.Checkpoint),
			"metadata": t.Metadata, "log_path": t.LogPath,
		},
	}
}

// ── entry (detail-only) ─────────────────────────────────────────────────

func entryType() object.Type {
	return object.Type{
		Name:  TypeEntry,
		Label: "Transcript entry",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString},
			{Name: "thought_id", Label: "Thought", Kind: object.KindRef, RefType: TypeThought},
			{Name: "role", Label: "Role", Kind: object.KindEnum,
				Enum: []string{"user", "assistant", "tool", "system"}},
			{Name: "message", Label: "Message", Kind: object.KindText},
			{Name: "tool_call_id", Label: "Tool call", Kind: object.KindString},
			{Name: "tool_name", Label: "Tool", Kind: object.KindString},
			{Name: "tool_input", Label: "Tool input", Kind: object.KindJSON},
			{Name: "time", Label: "Time", Kind: object.KindTime},
		},
	}
}

// entryProvider exists only so the entry descriptor is registered; entries are
// reached through a thought's `transcript` detail, never listed on their own.
type entryProvider struct{}

func (entryProvider) List(object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnsupported
}

func (entryProvider) Get(string) (object.Object, error) {
	return object.Object{}, object.ErrUnsupported
}

func (entryProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnsupported
}

func entryObject(e datastore.Entry, thoughtID string) object.Object {
	fields := map[string]any{
		"id": e.ID, "thought_id": thoughtID, "role": string(e.Role), "message": e.Message,
		"tool_call_id": e.ToolCallID, "tool_name": e.ToolName, "time": e.Time,
	}
	if len(e.ToolInput) > 0 {
		fields["tool_input"] = string(e.ToolInput)
	}
	return object.Object{Type: TypeEntry, ID: e.ID, Fields: fields}
}

// ── memory ──────────────────────────────────────────────────────────────

func memoryType() object.Type {
	return object.Type{
		Name:  TypeMemory,
		Label: "Memory",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString},
			{Name: "scope", Label: "Scope", Kind: object.KindEnum,
				Enum: []string{"session", "user", "navi", "memories"}},
			{Name: "subject", Label: "Subject", Kind: object.KindString},
			{Name: "content", Label: "Content", Kind: object.KindText},
			{Name: "source", Label: "Source", Kind: object.KindString},
			{Name: "tags", Label: "Tags", Kind: object.KindList, ItemKind: object.KindString},
			{Name: "importance", Label: "Importance", Kind: object.KindFloat},
			{Name: "expires_at", Label: "Expires", Kind: object.KindTime},
			{Name: "created_at", Label: "Created", Kind: object.KindTime},
			{Name: "updated_at", Label: "Updated", Kind: object.KindTime},
		},
		Views: []object.View{
			{Kind: object.ViewList, Columns: []string{"scope", "subject", "content", "importance"}},
			{Kind: object.ViewDetail},
		},
	}
}

// memoryProvider serves the memory aggregate through the navi's memory view.
// The view is unbound when no datastore is declared, in which case the type
// is simply empty rather than failing a portal's read.
type memoryProvider struct{ n *Navi }

func (p *memoryProvider) List(q object.Query) ([]object.Object, string, error) {
	recs, err := p.recall()
	if err != nil {
		return nil, "", err
	}
	start, end := pageWindow(len(recs), q)
	out := make([]object.Object, 0, end-start)
	for _, r := range recs[start:end] {
		out = append(out, memoryObject(r))
	}
	return out, nextCursor(end, len(recs)), nil
}

func (p *memoryProvider) Get(id string) (object.Object, error) {
	recs, err := p.recall()
	if err != nil {
		return object.Object{}, err
	}
	for _, r := range recs {
		if r.ID == id {
			return memoryObject(r), nil
		}
	}
	return object.Object{}, object.ErrNotFound
}

func (p *memoryProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

func (p *memoryProvider) recall() ([]datastore.MemoryRecord, error) {
	recs, err := p.n.Datastores.Handle().Memory().Recall(datastore.RecallQuery{})
	if errors.Is(err, datastore.ErrUnbound) {
		return nil, nil
	}
	return recs, err
}

func memoryObject(r datastore.MemoryRecord) object.Object {
	return object.Object{
		Type:    TypeMemory,
		ID:      r.ID,
		Version: r.UpdatedAt.UnixNano(),
		Fields: map[string]any{
			"id": r.ID, "scope": string(r.Scope), "subject": r.Subject.String(), "content": r.Content,
			"source": r.Source, "tags": r.Tags, "importance": r.Importance,
			"expires_at": r.ExpiresAt, "created_at": r.CreatedAt, "updated_at": r.UpdatedAt,
		},
	}
}

// ── pagination ──────────────────────────────────────────────────────────

// pageWindow resolves a Query's opaque cursor and limit against a total.
func pageWindow(total int, q object.Query) (start, end int) {
	if n, err := strconv.Atoi(q.Cursor); err == nil && n > 0 {
		start = n
	}
	if start > total {
		start = total
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultPage
	}
	if limit > maxPage {
		limit = maxPage
	}
	end = start + limit
	if end > total {
		end = total
	}
	return start, end
}

// nextCursor returns the cursor for the page after end, or "" at the end.
func nextCursor(end, total int) string {
	if end >= total {
		return ""
	}
	return strconv.Itoa(end)
}

// rawSchema parses a JSON Schema literal for an action's arguments.
func rawSchema(s string) json.RawMessage { return json.RawMessage(strings.TrimSpace(s)) }

// ── trust_event ────────────────────────────────────────────────────────

// trustEventType is the trust ledger as a read-only type (ADR 0027): the trust
// decisions the navi made — who was authenticated, who was refused — kept
// separate from the navi-authored interaction record (the thought and its
// lineage). Reading it is a capability (`read:trust_event`), so a deployment
// decides who may audit trust, and an operator portal renders it like any other
// type rather than through a bespoke endpoint.
func trustEventType() object.Type {
	return object.Type{
		Name:  TypeTrustEvent,
		Label: "Trust event",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Label: "ID", Kind: object.KindString, Section: "identity"},
			{Name: "at", Label: "At", Kind: object.KindTime, Section: "decision"},
			{Name: "kind", Label: "Kind", Kind: object.KindEnum, Section: "decision",
				Enum: []string{string(TrustAuthenticated), string(TrustRefused)}},
			{Name: "principal", Label: "Principal", Kind: object.KindString, Section: "decision"},
			{Name: "scheme", Label: "Scheme", Kind: object.KindString, Section: "decision"},
			{Name: "detail", Label: "Detail", Kind: object.KindText, Section: "decision"},
		},
		Views: []object.View{{Kind: object.ViewList, Name: "trust-events", Label: "Trust events",
			Columns: []string{"at", "kind", "principal", "scheme"}}},
	}
}

// trustEventProvider serves the trust ledger, newest first. A ledger entry has
// no stable identity of its own — the ring has no durable key — so Get is
// unsupported and the ledger is read as a bounded list; a durable ledger is a
// follow-up (ADR 0027 open question 1).
type trustEventProvider struct{ n *Navi }

func (p trustEventProvider) List(q object.Query) ([]object.Object, string, error) {
	var events []TrustEvent
	if p.n != nil && p.n.trust != nil {
		events = p.n.trust.Ledger().Events()
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	start, end := pageWindow(len(events), q)
	out := make([]object.Object, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, trustEventObject(events[i], i))
	}
	return out, nextCursor(end, len(events)), nil
}

func (p trustEventProvider) Get(string) (object.Object, error) {
	return object.Object{}, object.ErrNotFound
}

func (p trustEventProvider) Detail(string, string, object.Query) ([]object.Object, string, error) {
	return nil, "", object.ErrUnknownDetail
}

// trustEventObject projects one ledger entry. index is a display handle within
// the page, not a durable identity: the ledger is a ring.
func trustEventObject(ev TrustEvent, index int) object.Object {
	id := strconv.Itoa(index)
	return object.Object{
		Type: TypeTrustEvent,
		ID:   id,
		Fields: map[string]any{
			"id": id, "at": ev.At, "kind": string(ev.Kind),
			"principal": ev.Principal.String(), "scheme": ev.Scheme, "detail": ev.Detail,
		},
	}
}
