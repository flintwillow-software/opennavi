package memory

import (
	"sort"

	"github.com/flintwillow-software/opennavi/datastore"
)

// ── hybrid ranking (ADR 0040 decision 6) ────────────────────────────────
//
// The archive has two ways to answer a search: the keyword path (FTS5, which
// ranks by the store's own importance-then-recency) and the vector path (cosine
// over stored embeddings). When both produce candidates they are combined by
// RECIPROCAL RANK FUSION rather than by blending a distance with a rank.
//
// RRF is `score = Σ 1/(k + rank)` over the lists an entry appears in, with
// `k = 60`. The reason to prefer it over a weighted score is that it needs no
// tuning and no normalisation: a cosine similarity and a position in an
// importance-ordered list have no common unit, so any weighted blend requires a
// constant with no correct value, revisited for every embedder. RRF is
// rank-based, so it is indifferent to what the two scorers measure.
//
// It also degrades gracefully, which is what makes one code path serve every
// configuration: with only the keyword list non-empty (no embedder, or no
// vectors stored yet) the fusion reduces to that list's order, so a navi
// without semantic search gets exactly the ordering it had before this existed.

// rrfK is the RRF constant. 60 is the value from the original RRF paper
// (Cormack et al., 2009), used here as a stated default rather than a tuned
// one. ADR 0040 open question 1 records that whether it should be a knob is
// undecided; it is deliberately not one yet, because a knob with no evidence
// for its value is worse than a stated default.
const rrfK = 60

// fuseRRF combines ranked result lists into one order.
//
// Each list is a sequence of entry ids, best first. An entry appearing in
// several lists accumulates a contribution from each, so agreement between the
// two paths ranks an entry above one that only a single path liked.
//
// Ordering is deterministic and total: by fused score descending, then by the
// archive's own ranking (importance, then recency) as the tiebreak, then by id.
// The tiebreak is what ADR 0040 decision 6 promises — an entry RRF cannot
// separate still lands where it would have before the fusion existed — and the
// final id comparison is only there so two entries that are genuinely equal on
// every other axis still order stably rather than by map iteration.
func fuseRRF(byID map[string]datastore.MemoryRecord, lists ...[]string) []datastore.MemoryRecord {
	scores := map[string]float64{}
	seen := map[string]bool{}
	for _, list := range lists {
		for rank, id := range list {
			// Rank is 1-based: the first result is the best, and 1/(k+0)
			// would let a single list's top entry outscore a genuine
			// agreement between both lists.
			scores[id] += 1.0 / float64(rrfK+rank+1)
			seen[id] = true
		}
	}

	out := make([]datastore.MemoryRecord, 0, len(seen))
	for id := range seen {
		if rec, ok := byID[id]; ok {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := scores[out[i].ID], scores[out[j].ID]
		if si != sj {
			return si > sj
		}
		// The archive's own ranking, preserved as the tiebreak.
		if out[i].Importance != out[j].Importance {
			return out[i].Importance > out[j].Importance
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// rankBySimilarity returns the ids in `candidates`, best cosine first.
//
// A candidate with no stored vector (or one whose dimensions did not match, so
// the store omitted it — ADR 0040 decision 8) is simply absent from the
// ranking. It is not an error and not a zero score: it means the vector path
// has no opinion about that entry, so the entry is ranked by the keyword path
// alone, which is the honest outcome for an entry written before an embedder
// existed (decision 5).
func rankBySimilarity(query []float64, candidates []datastore.MemoryRecord, vectors map[string][]float64) []string {
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	for _, rec := range candidates {
		vec, ok := vectors[rec.ID]
		if !ok {
			continue
		}
		ranked = append(ranked, scored{id: rec.ID, score: datastore.CosineSimilarity(query, vec)})
	}
	// Descending similarity; id as a stable tiebreak so equal scores do not
	// order by map iteration.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	out := make([]string, 0, len(ranked))
	for _, s := range ranked {
		out = append(out, s.id)
	}
	return out
}

// rankByKeyword is the keyword list's order: the store's own ranking, which is
// importance then recency (and, on the FTS5 path, whatever order the query
// returned — the ORDER BY is unchanged by the index, ADR 0040 decision 3).
//
// It is extracted rather than inlined so the fusion's two inputs are visibly
// the same shape.
func rankByKeyword(candidates []datastore.MemoryRecord) []string {
	out := make([]string, 0, len(candidates))
	for _, rec := range candidates {
		out = append(out, rec.ID)
	}
	return out
}
