package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/principal"
)

// countingEmbedder counts calls, so a test can assert the bound is respected.
type countingEmbedder struct {
	dims  int
	calls int
	err   error
}

func (c *countingEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	v := make([]float64, c.dims)
	v[0] = 1
	return v, nil
}
func (c *countingEmbedder) Dimensions() int { return c.dims }
func (c *countingEmbedder) Name() string    { return "counting" }

// testSubject is the principal the archive is keyed to in these tests. Entries
// are written with it explicitly, because the archive filters by subject — an
// entry with no subject is not the navi's and is correctly invisible to it.
var testSubject = principal.MustParse("navi:x")

// writeRaw stores entries directly through the datastore, bypassing LongTerm,
// so the test can build an archive that predates the embedder — which is the
// situation backfill exists for.
func writeRaw(t *testing.T, m datastore.Memory, contents ...string) []datastore.MemoryRecord {
	t.Helper()
	out := make([]datastore.MemoryRecord, 0, len(contents))
	for _, c := range contents {
		rec, err := m.Remember(datastore.MemoryRecord{
			Scope: datastore.ScopeMemories, Subject: testSubject, Content: c,
		})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

// TestBackfillEmbedsEntriesWithNoVector is the whole point: an archive written
// before an embedder existed gains vectors.
func TestBackfillEmbedsEntriesWithNoVector(t *testing.T) {
	backend := inmem.New()
	recs := writeRaw(t, backend.Memory(), "one", "two", "three")

	emb := &countingEmbedder{dims: 3}
	vs := &memVectors{}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: emb, Vectors: vs,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := lt.Backfill(10)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(res.Embedded) != 3 {
		t.Fatalf("embedded %d, want 3", len(res.Embedded))
	}
	if res.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", res.Remaining)
	}
	for _, rec := range recs {
		if _, ok := vs.put[rec.ID]; !ok {
			t.Errorf("entry %s still has no vector", rec.ID)
		}
	}
}

// TestBackfillRespectsTheLimit: a backfill is bounded, because each entry is a
// network call. The remainder is reported so a caller can continue.
func TestBackfillRespectsTheLimit(t *testing.T) {
	backend := inmem.New()
	writeRaw(t, backend.Memory(), "one", "two", "three", "four", "five")

	emb := &countingEmbedder{dims: 3}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: emb, Vectors: &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := lt.Backfill(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embedded) != 2 {
		t.Fatalf("embedded %d, want 2 (the limit)", len(res.Embedded))
	}
	if emb.calls != 2 {
		t.Fatalf("embedder called %d times, want 2 — the limit must bound the work", emb.calls)
	}
	if res.Remaining != 3 {
		t.Fatalf("remaining = %d, want 3", res.Remaining)
	}
}

// TestBackfillSkipsEntriesThatAlreadyHaveAVector: a second call does no work.
func TestBackfillSkipsEntriesThatAlreadyHaveAVector(t *testing.T) {
	backend := inmem.New()
	writeRaw(t, backend.Memory(), "one", "two")

	emb := &countingEmbedder{dims: 3}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: emb, Vectors: &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := lt.Backfill(10); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 2 {
		t.Fatalf("first call embedded %d, want 2", emb.calls)
	}
	res, err := lt.Backfill(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embedded) != 0 {
		t.Fatalf("second call embedded %d, want 0 (nothing left to do)", len(res.Embedded))
	}
	if emb.calls != 2 {
		t.Fatalf("second call made %d extra embed calls, want 0", emb.calls-2)
	}
	if res.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", res.Remaining)
	}
}

// TestBackfillContinuesPastAFailure: one unreachable moment must not abandon
// the rest of the batch. Failures are collected and reported.
func TestBackfillContinuesPastAFailure(t *testing.T) {
	backend := inmem.New()
	recs := writeRaw(t, backend.Memory(), "one", "two", "three")

	// Fail on the second call only.
	emb := &failOnceEmbedder{dims: 3, failOn: 2}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: emb, Vectors: &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := lt.Backfill(10)
	if err != nil {
		t.Fatalf("a per-entry failure must not fail the batch: %v", err)
	}
	if len(res.Embedded) != 2 {
		t.Fatalf("embedded %d, want 2 (one entry failed)", len(res.Embedded))
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(res.Failed))
	}
	if _, ok := res.Failed[recs[1].ID]; !ok {
		t.Fatalf("the failure should name the entry that errored, got %v", res.Failed)
	}
}

// failOnceEmbedder errors on the nth call.
type failOnceEmbedder struct {
	dims   int
	calls  int
	failOn int
}

func (f *failOnceEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	f.calls++
	if f.calls == f.failOn {
		return nil, errors.New("transient failure")
	}
	v := make([]float64, f.dims)
	v[0] = 1
	return v, nil
}
func (f *failOnceEmbedder) Dimensions() int { return f.dims }
func (f *failOnceEmbedder) Name() string    { return "fail-once" }

// TestBackfillRequiresBothHalves: without an embedder or a vector index there
// is nothing a backfill could do, and it says so rather than silently doing
// nothing.
func TestBackfillRequiresBothHalves(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), testSubject, LongTermConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Backfill(10); err == nil {
		t.Fatal("backfill without an embedder or vector store must be an error")
	}
}

// TestBackfillRequiresAPositiveLimit: an unbounded backfill is refused, since
// each entry costs an embedding call.
func TestBackfillRequiresAPositiveLimit(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), testSubject, LongTermConfig{
		Embedder: &countingEmbedder{dims: 3}, Vectors: &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Backfill(0); err == nil {
		t.Fatal("a non-positive limit must be refused")
	}
}

// TestBackfillOnAnEmptyArchive: nothing to do is not an error.
func TestBackfillOnAnEmptyArchive(t *testing.T) {
	lt, err := NewLongTerm(inmem.New().Memory(), testSubject, LongTermConfig{
		Embedder: &countingEmbedder{dims: 3}, Vectors: &memVectors{},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := lt.Backfill(10)
	if err != nil {
		t.Fatalf("empty archive: %v", err)
	}
	if len(res.Embedded) != 0 || res.Remaining != 0 {
		t.Fatalf("res = %+v, want nothing embedded and nothing remaining", res)
	}
}

// TestBackfillMakesEntriesSemanticallySearchable is the end-to-end property: an
// entry that was keyword-only becomes reachable by the vector path.
func TestBackfillMakesEntriesSemanticallySearchable(t *testing.T) {
	backend := inmem.New()
	writeRaw(t, backend.Memory(), "an old note about bananas")

	// The embedder gives "bananas" a vector far from the query's, and the
	// query "oranges" a distinct one — so before the backfill the vector path
	// has no opinion, and after it, it does.
	emb := &mapEmbedder{dims: 3, vecs: map[string][]float64{
		"an old note about bananas": {0, 1, 0},
		"oranges":                   {1, 0, 0},
	}}
	vs := &memVectors{}
	lt, err := NewLongTerm(backend.Memory(), testSubject, LongTermConfig{
		Embedder: emb, Vectors: vs,
	})
	if err != nil {
		t.Fatal(err)
	}

	if res, err := lt.Backfill(10); err != nil || len(res.Embedded) != 1 {
		t.Fatalf("backfill = %+v, %v; want 1 embedded", res, err)
	}
	// The entry now has a vector, so the vector path can rank it.
	recs, err := lt.Search(SearchQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d entries, want 1", len(recs))
	}
	if _, ok := vs.put[recs[0].ID]; !ok {
		t.Fatal("the backfilled entry has no vector, so the vector path cannot see it")
	}
}
