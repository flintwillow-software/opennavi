package memory

import (
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
)

func rec(id string, importance float64, updated time.Time) datastore.MemoryRecord {
	return datastore.MemoryRecord{ID: id, Importance: importance, UpdatedAt: updated}
}

func ids(recs []datastore.MemoryRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

func eq(t *testing.T, got []datastore.MemoryRecord, want ...string) {
	t.Helper()
	gotIDs := ids(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("got %v, want %v", gotIDs, want)
		}
	}
}

// TestFuseRRFAgreementWins: an entry both paths rank highly beats one only a
// single path liked, which is the whole point of fusion.
func TestFuseRRFAgreementWins(t *testing.T) {
	now := time.Now()
	byID := map[string]datastore.MemoryRecord{
		"both":    rec("both", 0.1, now),
		"kwonly":  rec("kwonly", 0.9, now),
		"veconly": rec("veconly", 0.9, now),
	}
	// "both" is second on each list; the others top one list and are absent
	// from the other. Agreement must win.
	got := fuseRRF(byID,
		[]string{"kwonly", "both"},
		[]string{"veconly", "both"},
	)
	eq(t, got, "both", "kwonly", "veconly")
}

// TestFuseRRFSingleListDegradesToThatOrder is ADR 0040 decision 6's graceful
// degradation: with one list, fusion is that list's order, so a navi without
// semantic search gets exactly the ordering it had before.
func TestFuseRRFSingleListDegradesToThatOrder(t *testing.T) {
	now := time.Now()
	byID := map[string]datastore.MemoryRecord{
		"a": rec("a", 0.1, now),
		"b": rec("b", 0.5, now),
		"c": rec("c", 0.9, now),
	}
	got := fuseRRF(byID, []string{"c", "a", "b"})
	eq(t, got, "c", "a", "b")
}

// TestFuseRRFEmptyLists: nothing in, nothing out — not a panic on an empty map.
func TestFuseRRFEmptyLists(t *testing.T) {
	got := fuseRRF(map[string]datastore.MemoryRecord{}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", ids(got))
	}
}

// TestFuseRRFTiebreakIsTheArchiveOrdering is ADR 0040 decision 6's promise: an
// entry RRF cannot separate lands where it would have before the fusion
// existed — importance, then recency.
func TestFuseRRFTiebreakIsTheArchiveOrdering(t *testing.T) {
	base := time.Now()
	byID := map[string]datastore.MemoryRecord{
		"low":  rec("low", 0.1, base),
		"high": rec("high", 0.9, base),
		"mid":  rec("mid", 0.5, base),
	}
	// All three are first on their own list, so they tie on fused score.
	got := fuseRRF(byID,
		[]string{"low"},
		[]string{"high"},
		[]string{"mid"},
	)
	eq(t, got, "high", "mid", "low")
}

// TestFuseRRFRecencyBreaksEqualImportance: with importance equal, the more
// recently updated entry wins, matching the store's own ORDER BY.
func TestFuseRRFRecencyBreaksEqualImportance(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	byID := map[string]datastore.MemoryRecord{
		"old": rec("old", 0.5, older),
		"new": rec("new", 0.5, newer),
	}
	got := fuseRRF(byID, []string{"old"}, []string{"new"})
	eq(t, got, "new", "old")
}

// TestFuseRRFIsDeterministic: two entries equal on score, importance, and
// recency still order stably, rather than by map iteration.
func TestFuseRRFIsDeterministic(t *testing.T) {
	now := time.Now()
	byID := map[string]datastore.MemoryRecord{
		"b": rec("b", 0.5, now),
		"a": rec("a", 0.5, now),
	}
	first := ids(fuseRRF(byID, []string{"a"}, []string{"b"}))
	for i := 0; i < 20; i++ {
		if got := ids(fuseRRF(byID, []string{"a"}, []string{"b"})); got[0] != first[0] {
			t.Fatalf("order is not deterministic: %v then %v", first, got)
		}
	}
}

// TestFuseRRFIgnoresUnknownIds: an id with no record is skipped rather than
// producing a zero-valued record.
func TestFuseRRFIgnoresUnknownIds(t *testing.T) {
	now := time.Now()
	byID := map[string]datastore.MemoryRecord{"a": rec("a", 0.5, now)}
	got := fuseRRF(byID, []string{"a", "ghost"}, []string{"phantom"})
	eq(t, got, "a")
}

// TestRankBySimilarityOrdersByCosine.
func TestRankBySimilarityOrdersByCosine(t *testing.T) {
	now := time.Now()
	candidates := []datastore.MemoryRecord{
		rec("far", 0.9, now),
		rec("near", 0.1, now),
		rec("mid", 0.5, now),
	}
	query := []float64{1, 0}
	vectors := map[string][]float64{
		"near": {1, 0}, // cosine 1
		"mid":  {1, 1}, // cosine ~0.707
		"far":  {0, 1}, // cosine 0
	}
	got := rankBySimilarity(query, candidates, vectors)
	if len(got) != 3 || got[0] != "near" || got[1] != "mid" || got[2] != "far" {
		t.Fatalf("got %v, want [near mid far]", got)
	}
}

// TestRankBySimilaritySkipsEntriesWithNoVector: an entry the vector path has no
// opinion about is absent from the ranking rather than scored 0 — so it is
// ranked by the keyword path alone, which is the honest outcome for an entry
// written before an embedder existed (decision 5).
func TestRankBySimilaritySkipsEntriesWithNoVector(t *testing.T) {
	now := time.Now()
	candidates := []datastore.MemoryRecord{
		rec("has", 0.5, now),
		rec("hasnot", 0.9, now),
	}
	vectors := map[string][]float64{"has": {1, 0}}
	got := rankBySimilarity([]float64{1, 0}, candidates, vectors)
	if len(got) != 1 || got[0] != "has" {
		t.Fatalf("got %v, want [has] (an unembedded entry is skipped, not scored 0)", got)
	}
}

// TestRankBySimilarityIsDeterministic: equal similarities order by id, not by
// map iteration.
func TestRankBySimilarityIsDeterministic(t *testing.T) {
	now := time.Now()
	candidates := []datastore.MemoryRecord{rec("b", 0.5, now), rec("a", 0.5, now)}
	vectors := map[string][]float64{"a": {1, 0}, "b": {1, 0}}
	got := rankBySimilarity([]float64{1, 0}, candidates, vectors)
	if len(got) != 2 || got[0] != "a" {
		t.Fatalf("got %v, want [a b] (id tiebreak)", got)
	}
}

// TestRankByKeywordPreservesStoreOrder: the keyword list is the store's order
// verbatim — fusion must not re-sort it.
func TestRankByKeywordPreservesStoreOrder(t *testing.T) {
	now := time.Now()
	got := rankByKeyword([]datastore.MemoryRecord{rec("x", 0.1, now), rec("y", 0.9, now)})
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("got %v, want the input order preserved", got)
	}
}
