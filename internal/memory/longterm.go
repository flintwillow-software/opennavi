package memory

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/principal"
)

// LongTerm is the long-term memory tier: the searched knowledge archive
// (ADR 0037 decision 2). It is the tier boundary in code — reading it is a tool
// call, NEVER a runtime load — which is why it is a separate type from Service
// (the always-injected short-term tier) rather than a scope argument on one
// service.
//
// It deliberately holds no cache. Short-term memory is hydrated into RAM once
// and injected per thought, so its writes must land in RAM to be visible to the
// next thought; long-term memory is never injected, so caching it would buy
// nothing and would risk a long-term entry leaking into the always-loaded set —
// the exact context cost the tier boundary exists to avoid.
type LongTerm struct {
	memory  datastore.Memory
	subject principal.Ref
}

// NewLongTerm builds the long-term tier over the datastore's memory view.
func NewLongTerm(m datastore.Memory, subject principal.Ref) (*LongTerm, error) {
	if m == nil {
		return nil, fmt.Errorf("memory: long-term tier requires a datastore memory view")
	}
	if subject.IsZero() {
		return nil, fmt.Errorf("memory: long-term tier requires a subject")
	}
	return &LongTerm{memory: m, subject: subject}, nil
}

// longTermScopes are the scopes that belong to the searched archive. The `navi`
// scope is long-term rather than always-injected because its load timing is a
// *condition* ("when interacting with a specific navi"), and a navi cannot know
// in advance which peers it will meet (ADR 0037 decision 5).
var longTermScopes = []datastore.Scope{datastore.ScopeNavi, datastore.ScopeMemories}

// IsLongTermScope reports whether a scope belongs to the searched archive.
func IsLongTermScope(scope datastore.Scope) bool {
	for _, s := range longTermScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// LongTermScopes returns the archive's scopes (for a tool description or a
// skill that has to name them).
func LongTermScopes() []datastore.Scope {
	out := make([]datastore.Scope, len(longTermScopes))
	copy(out, longTermScopes)
	return out
}

// Write stores an entry in the archive. The subject is forced to the navi's own
// principal: an archive entry is the navi's knowledge, and letting a caller
// name another subject would let one navi's thought write into another's notes.
func (l *LongTerm) Write(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	if !IsLongTermScope(rec.Scope) {
		return datastore.MemoryRecord{}, fmt.Errorf(
			"memory: scope %q is not long-term memory (want navi or memories) — use stm_write for short-term notes",
			rec.Scope)
	}
	rec.Subject = l.subject
	return l.memory.Remember(rec)
}

// Remove deletes an archive entry by id.
func (l *LongTerm) Remove(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("memory: an id is required")
	}
	return l.memory.Forget(id)
}

// Read returns one entry by id, so a search hit can be read in full without
// re-deriving the query (ADR 0037 decision 3: a search returns enough of each
// entry to judge relevance, and the full text is a second, deliberate call).
func (l *LongTerm) Read(id string) (datastore.MemoryRecord, error) {
	if strings.TrimSpace(id) == "" {
		return datastore.MemoryRecord{}, fmt.Errorf("memory: an id is required")
	}
	recs, err := l.memory.Recall(datastore.RecallQuery{ID: id, Subject: l.subject})
	if err != nil {
		return datastore.MemoryRecord{}, err
	}
	for _, r := range recs {
		// The subject filter already scopes this to the navi's own archive;
		// the scope check keeps a short-term row out of a long-term read even
		// if a caller passes its id.
		if r.ID == id && IsLongTermScope(r.Scope) {
			return r, nil
		}
	}
	return datastore.MemoryRecord{}, fmt.Errorf("memory: %w (id %s)", datastore.ErrMemoryNotFound, id)
}

// SearchQuery is a bounded, ranked archive query (ADR 0037 decision 3). The
// bound is applied by the caller (the tool layer enforces the cap), but it is
// carried here so the store can do the ranking before truncation rather than
// after — a truncated-then-ranked result would be arbitrary.
type SearchQuery struct {
	// Query is a literal substring match on content (the keyword path). The
	// store's own recall ordering ranks the result.
	Query string
	// Scope narrows to one archive scope (empty = both).
	Scope datastore.Scope
	// Tags require every listed tag.
	Tags []string
}

// Search returns archive entries matching the query, most relevant first.
// "Most relevant" is the store's ranking (importance, then recency).
//
// It deliberately does NOT pass the caller's limit to the store. The tool layer
// has to report how many entries MATCHED as well as how many it returned, so a
// caller can tell "that is all of it" from "there is more" — and a store-side
// limit would truncate before that count could be taken, making the total a
// restatement of the limit rather than a fact about the archive. The archive is
// searched rather than injected, so paying the full scan here is the tier's own
// trade (ADR 0037 decision 6); the BOUND the caller sees is applied by the tool
// layer, after the ranking.
func (l *LongTerm) Search(q SearchQuery) ([]datastore.MemoryRecord, error) {
	if q.Scope != "" && !IsLongTermScope(q.Scope) {
		return nil, fmt.Errorf("memory: scope %q is not long-term memory (want navi or memories)", q.Scope)
	}
	recs, err := l.memory.Recall(datastore.RecallQuery{
		Scope:   q.Scope,
		Subject: l.subject,
		Query:   q.Query,
		Tags:    q.Tags,
	})
	if err != nil {
		return nil, err
	}
	// The store's recall is not scope-limited when Scope is empty, so filter
	// the short-term scopes out: an archive search must never return an
	// always-injected note, or the tier boundary would blur at the read path
	// too.
	out := make([]datastore.MemoryRecord, 0, len(recs))
	for _, r := range recs {
		if IsLongTermScope(r.Scope) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Prune removes archive entries matching the filter, and returns what it
// removed so the caller can report it (ADR 0037 decision 7: retention is a
// deliberate act, never a background heuristic — so this is a tool, and the
// caller sees exactly what it dropped).
type PruneFilter struct {
	// Scope narrows to one archive scope (empty = both).
	Scope datastore.Scope
	// IDs removes exactly these entries (mutually exclusive with the others).
	IDs []string
	// Query removes entries whose content matches (the same literal substring
	// match Search uses).
	Query string
	// Tags removes entries carrying every listed tag.
	Tags []string
}

// Prune deletes the entries the filter selects. It requires at least one
// constraint: an unconstrained prune would empty the archive, which is never
// what a deliberate act means, so it is refused rather than honoured.
func (l *LongTerm) Prune(f PruneFilter) ([]datastore.MemoryRecord, error) {
	if f.Scope != "" && !IsLongTermScope(f.Scope) {
		return nil, fmt.Errorf("memory: scope %q is not long-term memory (want navi or memories)", f.Scope)
	}
	if len(f.IDs) == 0 && f.Query == "" && len(f.Tags) == 0 {
		return nil, fmt.Errorf("memory: prune requires at least one of ids, query, or tags — an unconstrained prune would empty the archive")
	}

	var victims []datastore.MemoryRecord
	if len(f.IDs) > 0 {
		for _, id := range f.IDs {
			rec, err := l.Read(id)
			if err != nil {
				continue // already gone: prune is idempotent
			}
			if f.Scope != "" && rec.Scope != f.Scope {
				continue
			}
			victims = append(victims, rec)
		}
	} else {
		recs, err := l.Search(SearchQuery{Scope: f.Scope, Query: f.Query, Tags: f.Tags})
		if err != nil {
			return nil, err
		}
		victims = recs
	}

	removed := make([]datastore.MemoryRecord, 0, len(victims))
	for _, rec := range victims {
		if err := l.memory.Forget(rec.ID); err != nil {
			return removed, err
		}
		removed = append(removed, rec)
	}
	return removed, nil
}
