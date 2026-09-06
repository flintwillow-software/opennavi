package memory

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{1, 0}, 1},   // identical direction
		{[]float64{1, 0}, []float64{0, 1}, 0},   // orthogonal
		{[]float64{1, 0}, []float64{-1, 0}, -1}, // opposite
		{[]float64{2, 0}, []float64{1, 0}, 1},   // magnitude-invariant
		{[]float64{1, 1}, []float64{1, 0}, math.Sqrt(0.5)},
	}
	for i, c := range cases {
		got := cosine(c.a, c.b)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("case %d: cosine = %v, want %v", i, got, c.want)
		}
	}
	// Mismatched lengths: 0.
	if got := cosine([]float64{1}, []float64{1, 2}); got != 0 {
		t.Errorf("mismatched lengths: %v", got)
	}
	// Zero vectors: 0 (no NaN).
	if got := cosine([]float64{0, 0}, []float64{1, 1}); got != 0 || math.IsNaN(got) {
		t.Errorf("zero vector: %v", got)
	}
}

func TestVectorEncodeDecodeRoundTrip(t *testing.T) {
	vec := []float64{0.5, -1.25, 3.75, 0, -0.001}
	blob, err := encodeVector(vec)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 4*len(vec) {
		t.Fatalf("blob len = %d", len(blob))
	}
	got, err := decodeVector(blob)
	if err != nil {
		t.Fatal(err)
	}
	for i := range vec {
		if math.Abs(got[i]-vec[i]) > 1e-6 { // float32 precision
			t.Fatalf("elem %d: %v vs %v", i, got[i], vec[i])
		}
	}
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("corrupted blob should error")
	}
}

// scriptableEmbedder returns deterministic vectors keyed by substring
// match — enough to drive index/search tests without a live Ollama.
type scriptableEmbedder struct {
	dims  int
	vocab map[string][]float64
}

func (e *scriptableEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	vec := make([]float64, e.dims)
	for substr, v := range e.vocab {
		if strings.Contains(strings.ToLower(text), substr) {
			for i := range vec {
				vec[i] += v[i%len(v)]
			}
		}
	}
	// Normalize so cosine behaves like a real embedder.
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}
func (e *scriptableEmbedder) Dimensions() int { return e.dims }
func (e *scriptableEmbedder) Name() string    { return "scriptable" }

func TestVectorSearchTopK(t *testing.T) {
	s := testStore(t)
	ix, err := NewIndex(s, &scriptableEmbedder{dims: 4, vocab: map[string][]float64{
		"dog":  {1, 0, 0, 0},
		"cat":  {0, 1, 0, 0},
		"fish": {0, 0, 1, 0},
	}})
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range []struct{ id, text string }{
		{"m1", "the dog barked all night"},
		{"m2", "the cat slept on the keyboard"},
		{"m3", "the fish swam in circles"},
	} {
		if err := ix.Upsert(context.Background(), e.id, e.text); err != nil {
			t.Fatal(err)
		}
	}

	vec, _ := (&scriptableEmbedder{dims: 4, vocab: map[string][]float64{
		"dog": {1, 0, 0, 0},
	}}).Embed(context.Background(), "dog")
	hits, err := ix.VectorSearch(context.Background(), vec, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (top-k)", len(hits))
	}
	if hits[0].MemoryID != "m1" {
		t.Fatalf("top hit = %s, want m1", hits[0].MemoryID)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("not ranked: %+v", hits)
	}
}

func TestSemanticSearchHydratesEntries(t *testing.T) {
	s := testStore(t)
	ix, err := NewIndex(s, &scriptableEmbedder{dims: 4, vocab: map[string][]float64{
		"dog": {1, 0, 0, 0}, "cat": {0, 1, 0, 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "dog-facts", Content: "the dog barked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "cat-facts", Content: "the cat purred"}); err != nil {
		t.Fatal(err)
	}
	// Index both.
	for _, id := range []string{"dog-facts", "cat-facts"} {
		e, _ := s.Get(ScopeMemories, id)
		if err := ix.Upsert(context.Background(), id, e.Content); err != nil {
			t.Fatal(err)
		}
	}

	results, err := ix.SemanticSearch(context.Background(), "dog", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Entry.Subject != "dog-facts" {
		t.Fatalf("semantic search = %+v", results)
	}
	if results[0].Entry.Content != "the dog barked" {
		t.Fatalf("hydrated entry = %+v", results[0].Entry)
	}
}

func TestSemanticSearchSkipsOrphans(t *testing.T) {
	// Prune/Delete can leave orphan vectors when the index isn't told;
	// SemanticSearch must skip them instead of erroring.
	s := testStore(t)
	ix, err := NewIndex(s, &scriptableEmbedder{dims: 2, vocab: map[string][]float64{
		"x": {1, 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "orphan", Content: "x marks the spot"}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Upsert(context.Background(), "orphan", "x marks the spot"); err != nil {
		t.Fatal(err)
	}
	// Entry deleted without telling the index.
	if err := s.Delete(ScopeMemories, "orphan"); err != nil {
		t.Fatal(err)
	}
	// Reconcile removes the orphan vector.
	n, err := ix.Reconcile()
	if err != nil || n != 1 {
		t.Fatalf("reconcile = %d, %v", n, err)
	}
	results, err := ix.SemanticSearch(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("orphan surfaced: %+v", results)
	}
}

func TestIndexRequiresEmbedder(t *testing.T) {
	if _, err := NewIndex(testStore(t), nil); err == nil {
		t.Fatal("nil embedder should error")
	}
}

func TestUpsertReindexes(t *testing.T) {
	s := testStore(t)
	ix, err := NewIndex(s, &scriptableEmbedder{dims: 2, vocab: map[string][]float64{
		"alpha-topic": {1, 0}, "beta-topic": {0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Upsert(context.Background(), "t", "the alpha-topic discussion"); err != nil {
		t.Fatal(err)
	}
	// Re-index with different content: upsert overwrites, no duplicate.
	if err := ix.Upsert(context.Background(), "t", "the beta-topic discussion"); err != nil {
		t.Fatal(err)
	}
	vec, _ := (&scriptableEmbedder{dims: 2, vocab: map[string][]float64{
		"beta-topic": {0, 1},
	}}).Embed(context.Background(), "beta-topic")
	hits, err := ix.VectorSearch(context.Background(), vec, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MemoryID != "t" || hits[0].Score < 0.99 {
		t.Fatalf("reindex failed: %+v", hits)
	}
}
