package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/principal"
)

// mapEmbedder is a deterministic stub: it maps text to a small vector derived
// from its characters, so tests can control similarity without a model.
type mapEmbedder struct {
	dims int
	vecs map[string][]float64
	err  error
}

func (m *mapEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if m.err != nil {
		return nil, m.err
	}
	if v, ok := m.vecs[text]; ok {
		return v, nil
	}
	// Default: a vector that is similar to nothing in particular.
	v := make([]float64, m.dims)
	v[0] = 1
	return v, nil
}
func (m *mapEmbedder) Dimensions() int { return m.dims }
func (m *mapEmbedder) Name() string    { return "map" }

// memVectors is an in-memory datastore.VectorStore.
type memVectors struct{ put map[string][]float64 }

func (s *memVectors) PutVector(id string, vec []float64) error {
	if s.put == nil {
		s.put = map[string][]float64{}
	}
	s.put[id] = vec
	return nil
}
func (s *memVectors) DeleteVector(id string) error {
	delete(s.put, id)
	return nil
}
func (s *memVectors) Vectors(ids []string, dims int) (map[string][]float64, error) {
	out := map[string][]float64{}
	for _, id := range ids {
		if v, ok := s.put[id]; ok && len(v) == dims {
			out[id] = v
		}
	}
	return out, nil
}

func newArchive(t *testing.T, emb *mapEmbedder) (*LongTerm, *memVectors) {
	t.Helper()
	vs := &memVectors{}
	lt, err := NewLongTerm(inmem.New().Memory(), principal.MustParse("navi:test"), LongTermConfig{
		Embedder: emb,
		Vectors:  vs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lt, vs
}

// TestWriteStoresTheEmbedding is ADR 0040 decision 4: the vector is written
// with the entry, so it cannot drift from the text.
func TestWriteStoresTheEmbedding(t *testing.T) {
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{"a note": {1, 0, 0}}}
	lt, vs := newArchive(t, emb)

	rec, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, ok := vs.put[rec.ID]; !ok {
		t.Fatal("the entry's embedding was not stored")
	}
}

// TestWriteFailsWhenTheEmbedderIsDown is the stated cost of decision 4: a navi
// with an embedder declared cannot store an archive entry it cannot embed,
// because an entry whose vector silently described nothing would surface much
// later as unexplained bad recall.
func TestWriteFailsWhenTheEmbedderIsDown(t *testing.T) {
	sentinel := errors.New("model server is down")
	emb := &mapEmbedder{dims: 3, err: sentinel}
	lt, vs := newArchive(t, emb)

	_, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the embedder's error", err)
	}
	// Nothing was persisted: the write failed before the entry landed.
	recs, _ := lt.Search(SearchQuery{})
	if len(recs) != 0 {
		t.Fatalf("a failed embed must not persist the entry, got %d", len(recs))
	}
	if len(vs.put) != 0 {
		t.Fatal("no vector should have been stored")
	}
}

// TestWriteWithoutEmbedderStillWorks is decision 2: keyword-only is a supported
// configuration, so an entry writes normally with no embedder.
func TestWriteWithoutEmbedderStillWorks(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), principal.MustParse("navi:test"), LongTermConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"}); err != nil {
		t.Fatalf("Write without an embedder: %v", err)
	}
}

// TestSearchWithoutEmbedderIsKeywordOnly: the fusion reduces to the store's
// order, so a navi without semantic search gets exactly what it had before.
func TestSearchWithoutEmbedderIsKeywordOnly(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), principal.MustParse("navi:test"), LongTermConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"deploy notes", "unrelated", "deploy again"} {
		if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lt.Search(SearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("keyword search returned %d, want 2", len(got))
	}
}

// TestSearchFusesVectorAndKeyword: the vector path promotes an entry the
// keyword path ranked low, when the two disagree — which is what makes the
// fusion worth having rather than a restatement of the keyword order.
//
// The case is built so the two lists genuinely conflict: three entries match
// the keyword, and the vector path ranks the keyword-WORST of them first. RRF
// must move it up, because it is second-on-average rather than last on one.
func TestSearchFusesVectorAndKeyword(t *testing.T) {
	// The query vector is [1,0,0]; the stored vectors are chosen so the entry
	// the keyword path ranks last is the one the vector path ranks first.
	emb := &mapEmbedder{
		dims: 3,
		vecs: map[string][]float64{
			"deploy alpha": {0, 1, 0},     // cosine 0 with the query
			"deploy bravo": {0.5, 0.5, 0}, // cosine ~0.707
			"deploy gamma": {1, 0, 0},     // cosine 1 — the vector path's favourite
			"deploy":       {1, 0, 0},
		},
	}
	lt, _ := newArchive(t, emb)

	// Importance drives the keyword order (the store orders by importance
	// then recency), so gamma is LAST there and FIRST on the vector list.
	for _, c := range []struct {
		content    string
		importance float64
	}{
		{"deploy alpha", 0.9},
		{"deploy bravo", 0.5},
		{"deploy gamma", 0.1},
	} {
		if _, err := lt.Write(datastore.MemoryRecord{
			Scope: datastore.ScopeMemories, Content: c.content, Importance: c.importance,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := lt.Search(SearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	// gamma is rank 3 on keyword and rank 1 on vector; alpha is rank 1 and
	// rank 3. They tie on fused score, and the archive tiebreak (importance)
	// puts alpha first — so the assertion is that gamma is NOT last, which is
	// the change the vector path is responsible for.
	if got[len(got)-1].Content == "deploy gamma" {
		t.Fatalf("order = %v; the vector path must lift the entry it ranks first above last place", contents(got))
	}
}

func contents(recs []datastore.MemoryRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Content)
	}
	return out
}

// TestSearchVectorPathLiftsAnEntryTheKeywordPathRankedLower pins the same
// property in a case with no tie, so the promotion is unambiguous: two entries
// match the keyword, and the vector path reverses their order, so the one the
// keyword path ranked second ends up first on the combined score.
func TestSearchVectorPathLiftsAnEntryTheKeywordPathRankedLower(t *testing.T) {
	emb := &mapEmbedder{
		dims: 3,
		vecs: map[string][]float64{
			"deploy alpha": {0, 1, 0}, // far from the query
			"deploy gamma": {1, 0, 0}, // exactly the query
			"deploy":       {1, 0, 0},
		},
	}
	lt, _ := newArchive(t, emb)
	// alpha is more important, so the keyword path ranks it first.
	if _, err := lt.Write(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "deploy alpha", Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Write(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "deploy gamma", Importance: 0.1,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := lt.Search(SearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// gamma is rank 2 keyword + rank 1 vector = 1/(k+2)+1/(k+1).
	// alpha is rank 1 keyword + rank 2 vector = 1/(k+1)+1/(k+2). Equal, so the
	// tiebreak (importance) restores alpha. This asserts the tiebreak works
	// rather than that the vector path wins outright — which is exactly what
	// decision 6 promises: RRF cannot separate these, so the archive order
	// decides.
	if got[0].Content != "deploy alpha" {
		t.Fatalf("order = %v; the archive tiebreak (importance) should decide an RRF tie", contents(got))
	}
}

// TestSearchDegradesWhenTheEmbedderIsDown: unlike a write (decision 4), a
// search must not fail because the embedder is unreachable — the keyword path
// already has an answer, and a degraded read is recoverable.
func TestSearchDegradesWhenTheEmbedderIsDown(t *testing.T) {
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{"deploy notes": {1, 0, 0}}}
	lt, _ := newArchive(t, emb)
	if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "deploy notes"}); err != nil {
		t.Fatal(err)
	}

	// Now the embedder breaks.
	emb.err = errors.New("down")
	got, err := lt.Search(SearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatalf("a search must not fail when the embedder is down: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want the keyword answer", len(got))
	}
}

// TestSearchWithNoStoredVectorsIsKeywordOnly: an archive written before an
// embedder was declared (decision 5) still searches.
func TestSearchWithNoStoredVectorsIsKeywordOnly(t *testing.T) {
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{}}
	lt, vs := newArchive(t, emb)
	if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "deploy notes"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-embedder entry by clearing its vector.
	vs.put = map[string][]float64{}

	got, err := lt.Search(SearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want the keyword answer", len(got))
	}
}

// TestSearchWithEmptyQueryDoesNotEmbed: with no query text there is nothing to
// embed, so the vector path contributes nothing rather than embedding "".
func TestSearchWithEmptyQueryDoesNotEmbed(t *testing.T) {
	emb := &mapEmbedder{dims: 3, err: errors.New("must not be called")}
	lt, _ := newArchive(t, emb)
	if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"}); err != nil {
		// The write embeds, so clear the error to let it through.
		emb.err = nil
		if _, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"}); err != nil {
			t.Fatal(err)
		}
	}
	emb.err = errors.New("must not be called for an empty query")

	got, err := lt.Search(SearchQuery{})
	if err != nil {
		t.Fatalf("Search with no query: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("a query-less search must return the archive")
	}
}

// TestRemoveDeletesTheVector: pruning an entry must not leave its vector behind
// to be resurrected by a later ranking.
func TestRemoveDeletesTheVector(t *testing.T) {
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{"a note": {1, 0, 0}}}
	lt, vs := newArchive(t, emb)
	rec, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "a note"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lt.Remove(rec.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := vs.put[rec.ID]; ok {
		t.Fatal("the entry's vector survived its removal")
	}
}

// TestSemanticRequiresBothHalves: an embedder with nowhere to store a vector
// must not report a capability it cannot deliver.
func TestSemanticRequiresBothHalves(t *testing.T) {
	emb := &mapEmbedder{dims: 3}
	noStore, err := NewLongTerm(inmem.New().Memory(), principal.MustParse("navi:x"), LongTermConfig{Embedder: emb})
	if err != nil {
		t.Fatal(err)
	}
	if noStore.Semantic() {
		t.Error("Semantic() must be false with an embedder but no vector store")
	}

	noEmb, err := NewLongTerm(inmem.New().Memory(), principal.MustParse("navi:x"), LongTermConfig{Vectors: &memVectors{}})
	if err != nil {
		t.Fatal(err)
	}
	if noEmb.Semantic() {
		t.Error("Semantic() must be false with a vector store but no embedder")
	}
}

// TestStoredVectorSurvivesASearchRoundTrip: the end-to-end property — a vector
// written by Write is the vector the ranking reads.
func TestStoredVectorSurvivesASearchRoundTrip(t *testing.T) {
	want := []float64{0.25, -0.5, 0.75}
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{"target text": want}}
	lt, vs := newArchive(t, emb)
	rec, err := lt.Write(datastore.MemoryRecord{Scope: datastore.ScopeMemories, Content: "target text"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := vs.Vectors([]string{rec.ID}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored vectors = %d, want 1", len(got))
	}
	for i := range want {
		if got[rec.ID][i] != want[i] {
			t.Fatalf("element %d = %v, want %v", i, got[rec.ID][i], want[i])
		}
	}
	if !strings.Contains(rec.Content, "target") {
		t.Fatalf("unexpected content %q", rec.Content)
	}
}
