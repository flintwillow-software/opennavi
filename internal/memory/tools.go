package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/tool"
)

// The memory tools are split BY TIER, not by operation (NAVI-578).
//
// The tier is the load-timing boundary (ADR 0037 decision 1), and it is the
// thing that decides whether a write costs context on every future thought or
// only when searched. Putting it in the tool NAME rather than in a `scope`
// argument makes that choice legible at the call site and makes the wrong one
// unrepresentable: a caller cannot accidentally write an archive entry into the
// always-injected set, or a working-state note into an archive nothing reads.
//
//   stm_write   — short-term: session, user. Always injected. Small, deliberate.
//   ltm_write   — long-term: navi, memories. Searched. Park it and find it later.
//   ltm_search  — the bounded, ranked archive query.
//   ltm_read    — one archive entry in full, by id.
//   ltm_prune   — deliberate retention (ADR 0037 decision 7).
//
// Within a tier the OWNERSHIP is still an argument (`scope`), because both
// scopes in a tier load at the same time: `session` vs `user` is whose notes
// they are, and `navi` vs `memories` is whose archive they are. The tier is
// not an argument because it is not a choice the caller makes per call — it is
// which tool they reached for.

// Bounds on a long-term search (ADR 0037 decision 3). The bound is part of the
// contract: a tool that can return an unbounded result into a thought with a
// fixed context budget is a way to fail a thought rather than inform it.
const (
	ltmSearchDefaultLimit = 10
	ltmSearchMaxLimit     = 50
	// ltmExcerptMax caps each entry in a search result. A search returns
	// enough of each entry to JUDGE relevance; the full text is ltm_read's
	// job, which is why the two are separate tools.
	ltmExcerptMax = 240
)

// Tools returns the built-in memory tools for both tiers. stm is the
// always-injected tier; lt is the searched archive. Either may be nil when the
// navi does not declare that tier, and only the declared tier's tools are
// returned — a navi with no archive gets no ltm_* surface rather than a tool
// that fails at call time.
func Tools(stm *Service, lt *LongTerm) ([]tool.Tool, error) {
	var out []tool.Tool
	if stm != nil {
		t, err := stmTools(stm)
		if err != nil {
			return nil, err
		}
		out = append(out, t...)
	}
	if lt != nil {
		t, err := ltmTools(lt)
		if err != nil {
			return nil, err
		}
		out = append(out, t...)
	}
	return out, nil
}

// ── short-term ──────────────────────────────────────────────────────────

func stmTools(s *Service) ([]tool.Tool, error) {
	schema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The note to remember, written as a short self-contained statement."
			},
			"scope": {
				"type": "string",
				"enum": ["user", "session"],
				"description": "user = notes about the human (preferences, style, lessons); session = working state for the current task. Defaults to session."
			},
			"id": {
				"type": "string",
				"description": "Replace an existing entry by id, or name the entry to remove."
			},
			"remove": {
				"type": "boolean",
				"description": "Delete the entry with this id instead of writing."
			}
		},
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	return []tool.Tool{{
		Def: tool.Def{
			Name: "stm_write",
			Description: "Add, replace, or remove one SHORT-TERM memory note. Short-term memory is injected into " +
				"every thought's context, so a note here is a context cost paid by all your future work — write " +
				"deliberately, and consolidate rather than accumulating near-duplicates. Use user scope for notes " +
				"about the human, session scope for working state. Pass an id to replace an existing entry, or " +
				"remove=true with an id to delete it. For anything too large or too situational to carry in every " +
				"thought, use ltm_write instead and leave a short-term pointer to it.",
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        tool.AccessWrite,
			Timeout:       5 * time.Second,
			Recovery:      "none",
			Mode:          tool.ToolModeSequential,
			Tags:          []string{"memory", "short-term", "context", "session"},
			Version:       "0.1.0",
			PromptSnippet: "stm_write — add, replace, or remove a short-term memory note (user or session scope)",
			PromptGuidelines: []string{
				"Short-term memory is already in your context; write only what is worth carrying to a later thought, and consolidate rather than accumulating near-duplicates.",
				"Write as you go rather than saving it all for a wrap-up: a more urgent thought can stop this one without giving it a chance to write a final note.",
			},
			SourceInfo: tool.SourceInfo{Origin: tool.OriginBuiltin},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleSTMWrite(ctx, s, args)
		},
	}}, nil
}

func handleSTMWrite(ctx context.Context, s *Service, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	id, _ := args["id"].(string)
	if remove, _ := args["remove"].(bool); remove {
		if id == "" {
			return "", fmt.Errorf("stm_write: remove requires an id")
		}
		if err := s.Remove(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed %s", id), nil
	}

	content, _ := args["content"].(string)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("stm_write: content is required")
	}

	scope := datastore.ScopeSession
	if raw, ok := args["scope"].(string); ok && raw != "" {
		scope = datastore.Scope(raw)
	}

	rec, err := s.Write(datastore.MemoryRecord{ID: id, Scope: scope, Content: content})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("remembered %s (%s)", rec.ID, rec.Scope), nil
}

// ── long-term ───────────────────────────────────────────────────────────

func ltmTools(lt *LongTerm) ([]tool.Tool, error) {
	writeSchema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The entry to archive, written as a self-contained statement."
			},
			"scope": {
				"type": "string",
				"enum": ["memories", "navi"],
				"description": "memories = the knowledge archive (facts and learned information); navi = notes about another navi (how to communicate, delegate, collaborate). Defaults to memories."
			},
			"id": { "type": "string", "description": "Replace an existing entry by id, or name the entry to remove." },
			"tags": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Free-form labels; ltm_search can filter on them exactly."
			},
			"importance": {
				"type": "number",
				"minimum": 0,
				"maximum": 1,
				"description": "Ranks the entry for recall (0..1, higher first). Defaults to the store's."
			},
			"source": { "type": "string", "description": "Where this came from, when known." },
			"remove": { "type": "boolean", "description": "Delete the entry with this id instead of writing." }
		},
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	searchSchema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": { "type": "string", "description": "Case-insensitive substring match on the entry's content." },
			"scope": { "type": "string", "enum": ["memories", "navi"], "description": "Narrow to one archive scope. Omit for both." },
			"tags": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Keep only entries carrying every listed tag."
			},
			"limit": { "type": "integer", "minimum": 1, "description": "Maximum entries to return (default 10, hard cap 50)." }
		},
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	readSchema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["id"],
		"properties": { "id": { "type": "string", "minLength": 1 } },
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	pruneSchema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"ids": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Remove exactly these entries."
			},
			"query": { "type": "string", "description": "Remove entries whose content matches this substring." },
			"tags": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Remove entries carrying every listed tag."
			},
			"scope": { "type": "string", "enum": ["memories", "navi"], "description": "Narrow to one archive scope. Omit for both." }
		},
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	return []tool.Tool{
		{
			Def: tool.Def{
				Name: "ltm_write",
				Description: "Add, replace, or remove one LONG-TERM memory entry — the searched archive, which is " +
					"NOT injected into your context and so may grow. This is where a conclusion you cannot act on " +
					"now belongs: park it here with a tag you will recognise, and find it later with ltm_search. " +
					"Use memories scope for facts and learned information, navi scope for notes about another navi. " +
					"Pass an id to replace an existing entry, or remove=true with an id to delete it. For something " +
					"you need in front of you at every thought, use stm_write instead.",
				InputSchema:   writeSchema,
				OutputFormat:  "text",
				Access:        tool.AccessWrite,
				Timeout:       5 * time.Second,
				Recovery:      "none",
				Mode:          tool.ToolModeSequential,
				Tags:          []string{"memory", "long-term", "archive", "knowledge"},
				Version:       "0.1.0",
				PromptSnippet: "ltm_write — add, replace, or remove a long-term memory entry (memories or navi scope)",
				PromptGuidelines: []string{
					"Long-term memory is searched, never injected: nothing you write here reaches your context unless you look for it. Give it tags you would search by.",
					"If a fact is worth having in front of you at every thought, it belongs in stm_write; if it is worth finding later, it belongs here.",
				},
				SourceInfo: tool.SourceInfo{Origin: tool.OriginBuiltin},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return handleLTMWrite(ctx, lt, args)
			},
		},
		{
			Def: tool.Def{
				Name: "ltm_search",
				Description: "Search the long-term memory archive, returning a BOUNDED, ranked set of entries " +
					"(default 10, at most 50) with enough of each to judge relevance. This is the tool for " +
					"\"what do I already know about X\" — filter by content, scope, or tags. Read a specific hit in " +
					"full with ltm_read. Nothing from the archive is ever in your context unless you search for it.",
				InputSchema:   searchSchema,
				OutputFormat:  "json",
				Access:        tool.AccessReadOnly,
				Timeout:       5 * time.Second,
				Recovery:      "none",
				Mode:          tool.ToolModeParallel,
				Tags:          []string{"memory", "long-term", "archive", "search"},
				Version:       "0.1.0",
				PromptSnippet: "ltm_search — bounded, ranked search of the long-term memory archive",
				PromptGuidelines: []string{
					"The result is bounded (at most 50). If it is truncated, narrow the query rather than paging.",
				},
				SourceInfo: tool.SourceInfo{Origin: tool.OriginBuiltin},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return handleLTMSearch(ctx, lt, args)
			},
		},
		{
			Def: tool.Def{
				Name: "ltm_read",
				Description: "Read one long-term memory entry in full, by id. A search returns an excerpt of each " +
					"hit so you can judge relevance; this returns the whole entry when one is worth reading properly.",
				InputSchema:   readSchema,
				OutputFormat:  "json",
				Access:        tool.AccessReadOnly,
				Timeout:       5 * time.Second,
				Recovery:      "none",
				Mode:          tool.ToolModeParallel,
				Tags:          []string{"memory", "long-term", "archive"},
				Version:       "0.1.0",
				PromptSnippet: "ltm_read — read one long-term memory entry in full by id",
				SourceInfo:    tool.SourceInfo{Origin: tool.OriginBuiltin},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return handleLTMRead(ctx, lt, args)
			},
		},
		{
			Def: tool.Def{
				Name: "ltm_prune",
				Description: "Deliberately remove entries from the long-term memory archive, and report what was " +
					"removed. Retention is an explicit act, not a background sweep: nothing prunes the archive on " +
					"its own. Give ids to remove exactly those, or a query and/or tags to remove a set. At least one " +
					"constraint is required — an unconstrained prune would empty the archive and is refused.",
				InputSchema:   pruneSchema,
				OutputFormat:  "json",
				Access:        tool.AccessWrite,
				Timeout:       10 * time.Second,
				Recovery:      "none",
				Mode:          tool.ToolModeSequential,
				Tags:          []string{"memory", "long-term", "archive", "retention"},
				Version:       "0.1.0",
				PromptSnippet: "ltm_prune — deliberately remove long-term memory entries (ids, query, or tags)",
				PromptGuidelines: []string{
					"Pruning is permanent. Search first if you are not certain what a query or tag selects.",
				},
				SourceInfo: tool.SourceInfo{Origin: tool.OriginBuiltin},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return handleLTMPrune(ctx, lt, args)
			},
		},
	}, nil
}

func handleLTMWrite(ctx context.Context, lt *LongTerm, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	id, _ := args["id"].(string)
	if remove, _ := args["remove"].(bool); remove {
		if id == "" {
			return "", fmt.Errorf("ltm_write: remove requires an id")
		}
		if err := lt.Remove(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed %s", id), nil
	}

	content, _ := args["content"].(string)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("ltm_write: content is required")
	}

	scope := datastore.ScopeMemories
	if raw, ok := args["scope"].(string); ok && raw != "" {
		scope = datastore.Scope(raw)
	}

	rec := datastore.MemoryRecord{ID: id, Scope: scope, Content: content}
	if raw, ok := args["tags"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("ltm_write: tags must be an array of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return "", fmt.Errorf("ltm_write: tags must be an array of non-empty strings")
			}
			rec.Tags = append(rec.Tags, s)
		}
	}
	if raw, ok := args["importance"]; ok {
		f, ok := raw.(float64)
		if !ok || f < 0 || f > 1 {
			return "", fmt.Errorf("ltm_write: importance must be a number in 0..1")
		}
		rec.Importance = f
	}
	if raw, ok := args["source"].(string); ok {
		rec.Source = raw
	}

	stored, err := lt.Write(rec)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("archived %s (%s)", stored.ID, stored.Scope), nil
}

func handleLTMSearch(ctx context.Context, lt *LongTerm, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	q := SearchQuery{}
	limit := ltmSearchDefaultLimit
	if raw, ok := args["query"].(string); ok {
		q.Query = raw
	}
	if raw, ok := args["scope"].(string); ok && raw != "" {
		q.Scope = datastore.Scope(raw)
	}
	if raw, ok := args["tags"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("ltm_search: tags must be an array of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return "", fmt.Errorf("ltm_search: tags must be an array of non-empty strings")
			}
			q.Tags = append(q.Tags, s)
		}
	}
	if raw, ok := args["limit"]; ok {
		n, ok := raw.(float64)
		if !ok {
			return "", fmt.Errorf("ltm_search: limit must be an integer")
		}
		limit = int(n)
	}
	// The bound is enforced here, not trusted from the caller: a request for
	// more than the cap is honoured at the cap. It is applied AFTER the
	// ranking, so the envelope's `total` reports what matched rather than
	// restating the limit.
	if limit <= 0 {
		limit = ltmSearchDefaultLimit
	}
	if limit > ltmSearchMaxLimit {
		limit = ltmSearchMaxLimit
	}

	recs, err := lt.Search(q)
	if err != nil {
		return "", err
	}
	total := len(recs)
	if len(recs) > limit {
		recs = recs[:limit]
	}
	return marshalEntries(recs, total), nil
}

func handleLTMRead(ctx context.Context, lt *LongTerm, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("ltm_read: id is required")
	}
	rec, err := lt.Read(id)
	if err != nil {
		return "", err
	}
	blob, err := json.Marshal(fullEntry{
		ID:         rec.ID,
		Scope:      string(rec.Scope),
		Content:    rec.Content,
		Tags:       rec.Tags,
		Importance: rec.Importance,
		Source:     rec.Source,
		CreatedAt:  rec.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  rec.UpdatedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

func handleLTMPrune(ctx context.Context, lt *LongTerm, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var f PruneFilter
	if raw, ok := args["ids"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("ltm_prune: ids must be an array of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return "", fmt.Errorf("ltm_prune: ids must be an array of non-empty strings")
			}
			f.IDs = append(f.IDs, s)
		}
	}
	if raw, ok := args["query"].(string); ok {
		f.Query = raw
	}
	if raw, ok := args["scope"].(string); ok && raw != "" {
		f.Scope = datastore.Scope(raw)
	}
	if raw, ok := args["tags"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("ltm_prune: tags must be an array of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return "", fmt.Errorf("ltm_prune: tags must be an array of non-empty strings")
			}
			f.Tags = append(f.Tags, s)
		}
	}

	removed, err := lt.Prune(f)
	if err != nil {
		return "", err
	}
	type pruned struct {
		ID      string `json:"id"`
		Scope   string `json:"scope"`
		Excerpt string `json:"excerpt"`
	}
	out := struct {
		Removed int      `json:"removed"`
		Entries []pruned `json:"entries"`
	}{Removed: len(removed)}
	for _, rec := range removed {
		out.Entries = append(out.Entries, pruned{ID: rec.ID, Scope: string(rec.Scope), Excerpt: excerpt(rec.Content)})
	}
	if out.Entries == nil {
		out.Entries = []pruned{}
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

// ── rendering ───────────────────────────────────────────────────────────

// entryView is what a SEARCH returns: enough of each entry to judge relevance,
// never the whole text. The full text is ltm_read's job (ADR 0037 decision 3).
type entryView struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	Excerpt    string   `json:"excerpt"`
	Tags       []string `json:"tags,omitempty"`
	Importance float64  `json:"importance"`
	Source     string   `json:"source,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
}

// fullEntry is what ltm_read returns: the whole entry.
type fullEntry struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags,omitempty"`
	Importance float64  `json:"importance"`
	Source     string   `json:"source,omitempty"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

func marshalEntries(recs []datastore.MemoryRecord, total int) string {
	views := make([]entryView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, entryView{
			ID:         rec.ID,
			Scope:      string(rec.Scope),
			Excerpt:    excerpt(rec.Content),
			Tags:       rec.Tags,
			Importance: rec.Importance,
			Source:     rec.Source,
			UpdatedAt:  rec.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	// The envelope reports what matched and whether the bound cut it, so a
	// caller can tell "that is all of it" from "there is more" without a
	// second call — the same contract thoughts_search uses.
	env := struct {
		Total     int         `json:"total"`
		Returned  int         `json:"returned"`
		Truncated bool        `json:"truncated"`
		Entries   []entryView `json:"entries"`
	}{Total: total, Returned: len(views), Truncated: total > len(views), Entries: views}
	blob, _ := json.Marshal(env)
	return string(blob)
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= ltmExcerptMax {
		return s
	}
	return s[:ltmExcerptMax] + "…"
}
