package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/model"
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
	memory   datastore.Memory
	subject  principal.Ref
	embedder model.Embedder
	// vectors is the backend's optional vector capability, or nil when the
	// active backend has none. Nil is not an error: a backend without a vector
	// index is keyword-only, which ADR 0040 decision 2 makes a supported
	// configuration.
	vectors datastore.VectorStore
}

// LongTermConfig builds the archive's optional capabilities.
type LongTermConfig struct {
	// Embedder, when set, adds semantic recall to archive search (ADR 0040
	// decision 1). Nil means keyword-only search, which is a supported
	// configuration rather than a degraded one (decision 2) — a navi with no
	// embedder loses nothing it had, it simply gains no vector path.
	Embedder model.Embedder
	// Vectors is the backend's optional vector index (decision 7). It is
	// discovered from the datastore by the caller rather than asserted here,
	// so the memory package stays ignorant of which backend is active.
	Vectors datastore.VectorStore
}

// NewLongTerm builds the long-term tier over the datastore's memory view.
func NewLongTerm(m datastore.Memory, subject principal.Ref, cfg LongTermConfig) (*LongTerm, error) {
	if m == nil {
		return nil, fmt.Errorf("memory: long-term tier requires a datastore memory view")
	}
	if subject.IsZero() {
		return nil, fmt.Errorf("memory: long-term tier requires a subject")
	}
	return &LongTerm{
		memory:   m,
		subject:  subject,
		embedder: cfg.Embedder,
		vectors:  cfg.Vectors,
	}, nil
}

// Embedder reports the declared embedder, or nil when search is keyword-only.
// It is how a caller asks "is semantic search available" without probing.
func (l *LongTerm) Embedder() model.Embedder { return l.embedder }

// Semantic reports whether semantic search is available: an embedder AND a
// backend that can store vectors. Both are required — an embedder with nowhere
// to put a vector would otherwise report a capability it cannot deliver.
func (l *LongTerm) Semantic() bool { return l.embedder != nil && l.vectors != nil }

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
//
// When an embedder and a vector index are both available the entry's embedding
// is written alongside it (ADR 0040 decision 4). Embedding on write rather than
// on read is what makes "an embedded entry's vector describes its current
// content" true by construction: an edit re-embeds, so the vector cannot drift
// from the text. Embedding on read would put a network call on the search path
// and make a search fail when the model server is down.
//
// **The cost is deliberate and stated**: a navi with an embedder declared
// cannot write an archive entry while the embedder is unreachable. That is the
// same refusal-over-silent-degradation trade ADR 0039 decision 1 made for the
// short-term bound — an entry whose vector silently described nothing would be
// worse, because the failure would surface much later as unexplained bad recall.
//
// A navi with no embedder writes the entry as before; the archive is
// heterogeneous by design (decision 5).
func (l *LongTerm) Write(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	if !IsLongTermScope(rec.Scope) {
		return datastore.MemoryRecord{}, fmt.Errorf(
			"memory: scope %q is not long-term memory (want navi or memories) — use stm_write for short-term notes",
			rec.Scope)
	}
	rec.Subject = l.subject

	// Embed BEFORE storing the entry. If the embedder is down the write fails
	// and nothing is persisted, rather than an entry landing with no vector
	// and no record that it was meant to have one.
	var vec []float64
	if l.Semantic() {
		v, err := l.embedder.Embed(context.Background(), rec.Content)
		if err != nil {
			return datastore.MemoryRecord{}, fmt.Errorf("memory: embed archive entry: %w", err)
		}
		vec = v
	}

	stored, err := l.memory.Remember(rec)
	if err != nil {
		return datastore.MemoryRecord{}, err
	}
	if vec != nil {
		if err := l.vectors.PutVector(stored.ID, vec); err != nil {
			// The entry is stored but its vector is not. That is a real
			// inconsistency, and it is reported rather than swallowed: the
			// entry degrades to keyword-only, which is recoverable, but the
			// caller should know the vector path did not take.
			return stored, fmt.Errorf("memory: entry %s stored without its embedding: %w", stored.ID, err)
		}
	}
	return stored, nil
}

// Remove deletes an archive entry by id.
//
// The entry's vector is deleted explicitly rather than relying on the backend's
// foreign key: the SQLite row cascades, but the contract does not promise a
// cascade, so a backend without one would otherwise accumulate vectors for
// entries that no longer exist. A delete of a vector that was never stored is
// not an error (the archive is heterogeneous).
func (l *LongTerm) Remove(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("memory: an id is required")
	}
	if l.vectors != nil {
		if err := l.vectors.DeleteVector(id); err != nil {
			return fmt.Errorf("memory: remove entry %s: %w", id, err)
		}
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
//
// Two paths contribute when both are available, and their results are fused by
// reciprocal rank fusion (ADR 0040 decision 6):
//
//   - the KEYWORD path, which is the store's own recall — an FTS5 index lookup
//     ordered by importance then recency (decision 3);
//   - the VECTOR path, which embeds the query and ranks the candidates by
//     cosine similarity (decision 7).
//
// With no embedder, no vector index, or no query text the vector path
// contributes nothing and the fusion reduces to the keyword order — so a navi
// without semantic search gets exactly the ordering it had before, which is what
// makes one code path serve every configuration (decision 2).
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

	// The keyword path's contribution. It is the whole answer when the vector
	// path is unavailable, and one of two lists when it is not.
	keyword := rankByKeyword(out)
	if !l.Semantic() || strings.TrimSpace(q.Query) == "" {
		return out, nil
	}

	// The vector path: embed the query, then rank the candidates the keyword
	// path already selected. Ranking the KEYWORD CANDIDATES rather than the
	// whole archive is deliberate — it keeps the two paths' notions of "what
	// matches" consistent (the filters the caller asked for are honoured
	// identically) and it bounds the cosine work to entries already relevant
	// by the other measure.
	queryVec, err := l.embedder.Embed(context.Background(), q.Query)
	if err != nil {
		// A search must not fail because the embedder is down: unlike a write
		// (decision 4), a degraded read is recoverable and the keyword path
		// already has an answer. Falling back is the honest outcome, and it is
		// logged by the caller rather than silently hidden.
		return out, nil
	}
	vectors, err := l.vectors.Vectors(idsOf(out), l.embedder.Dimensions())
	if err != nil {
		return out, nil
	}
	if len(vectors) == 0 {
		// No stored vectors for these candidates — an archive written before
		// an embedder was declared (decision 5). The keyword order stands.
		return out, nil
	}

	byID := make(map[string]datastore.MemoryRecord, len(out))
	for _, r := range out {
		byID[r.ID] = r
	}
	return fuseRRF(byID, keyword, rankBySimilarity(queryVec, out, vectors)), nil
}

// idsOf is the id list for a record set, used to fetch the vectors for exactly
// the candidates under consideration.
func idsOf(recs []datastore.MemoryRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
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
