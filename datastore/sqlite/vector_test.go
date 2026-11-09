package sqlite_test

import (
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
)

// TestVectorStoreRoundTrip: a stored vector reads back identical.
func TestVectorStoreRoundTrip(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "a note")

	vs := store.VectorStore()
	if vs == nil {
		t.Fatal("sqlite must expose a vector store")
	}
	vec := []float64{0.1, -0.2, 0.3, 0.4}
	if err := vs.PutVector(rec.ID, vec); err != nil {
		t.Fatalf("PutVector: %v", err)
	}
	got, err := vs.Vectors([]string{rec.ID}, len(vec))
	if err != nil {
		t.Fatalf("Vectors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Vectors returned %d entries, want 1", len(got))
	}
	for i := range vec {
		if got[rec.ID][i] != vec[i] {
			t.Errorf("element %d = %v, want %v", i, got[rec.ID][i], vec[i])
		}
	}
}

// TestVectorStoreReplacesOnRewrite: an entry edited and re-embedded keeps ONE
// vector, describing the new text. A second row would make the ranking depend
// on which one the query happened to return.
func TestVectorStoreReplacesOnRewrite(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "first text")
	vs := store.VectorStore()

	if err := vs.PutVector(rec.ID, []float64{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := vs.PutVector(rec.ID, []float64{0, 1}); err != nil {
		t.Fatal(err)
	}
	got, err := vs.Vectors([]string{rec.ID}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d vectors, want 1 (a rewrite must replace)", len(got))
	}
	if got[rec.ID][0] != 0 || got[rec.ID][1] != 1 {
		t.Fatalf("vector = %v, want the replacement", got[rec.ID])
	}
}

// TestVectorStoreOmitsMismatchedDimensions is ADR 0040 decision 8: a vector
// stored under a different model is omitted, not compared.
func TestVectorStoreOmitsMismatchedDimensions(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "a note")
	vs := store.VectorStore()

	if err := vs.PutVector(rec.ID, []float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// Ask for 2 dimensions: the stored vector is 3, so it must be omitted.
	got, err := vs.Vectors([]string{rec.ID}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d vectors, want 0 (a dimension mismatch is omitted, not compared)", len(got))
	}
}

// TestVectorStoreMissingIsAbsentNotError: an entry with no vector is the normal
// case for one written before an embedder existed (decision 5).
func TestVectorStoreMissingIsAbsentNotError(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "never embedded")
	vs := store.VectorStore()

	got, err := vs.Vectors([]string{rec.ID, "no-such-id"}, 4)
	if err != nil {
		t.Fatalf("a missing vector must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d vectors, want 0", len(got))
	}
}

// TestVectorStoreCascadesOnForget: the foreign key deletes an entry's vector
// with the entry, so a pruned entry cannot be resurrected by the vector path.
func TestVectorStoreCascadesOnForget(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "an ephemeral note")
	vs := store.VectorStore()

	if err := vs.PutVector(rec.ID, []float64{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.Memory().Forget(rec.ID); err != nil {
		t.Fatal(err)
	}
	got, err := vs.Vectors([]string{rec.ID}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d vectors after Forget, want 0 (the row must cascade)", len(got))
	}
}

// TestVectorStoreDeleteIsIdempotent: deleting a vector that was never stored is
// not an error, so a caller pruning a heterogeneous archive need not know which
// entries were embedded.
func TestVectorStoreDeleteIsIdempotent(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "a note")
	vs := store.VectorStore()

	if err := vs.DeleteVector(rec.ID); err != nil {
		t.Fatalf("deleting an absent vector must not error: %v", err)
	}
	if err := vs.PutVector(rec.ID, []float64{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := vs.DeleteVector(rec.ID); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	got, _ := vs.Vectors([]string{rec.ID}, 2)
	if len(got) != 0 {
		t.Fatalf("vector survived deletion")
	}
}

// TestVectorStoreRefusesOrphan: the foreign key refuses a vector for an entry
// that does not exist, so an orphan cannot accumulate.
func TestVectorStoreRefusesOrphan(t *testing.T) {
	store := openMem(t)
	if err := store.VectorStore().PutVector("no-such-entry", []float64{1, 0}); err == nil {
		t.Fatal("a vector for a non-existent entry must be refused by the foreign key")
	}
}

// TestVectorStoreRefusesEmptyVector: an empty vector is a caller error, not a
// zero-length row.
func TestVectorStoreRefusesEmptyVector(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "a note")
	if err := store.VectorStore().PutVector(rec.ID, nil); err == nil {
		t.Fatal("an empty vector must be refused")
	}
	if err := store.VectorStore().PutVector("", []float64{1}); err == nil {
		t.Fatal("an empty id must be refused")
	}
}

// TestVectorStoreEmptyIdList: asking about nothing returns nothing, without
// building an empty IN () clause (which would be a SQL error).
func TestVectorStoreEmptyIdList(t *testing.T) {
	store := openMem(t)
	got, err := store.VectorStore().Vectors(nil, 4)
	if err != nil {
		t.Fatalf("empty id list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

// TestVectorStoreManyIds: the id list is chunked, so a large candidate set does
// not exceed SQLite's variable limit.
func TestVectorStoreManyIds(t *testing.T) {
	store := openMem(t)
	vs := store.VectorStore()
	ids := make([]string, 0, 1200)
	for i := 0; i < 1200; i++ {
		rec := remember(t, store, datastore.ScopeMemories, "note")
		if err := vs.PutVector(rec.ID, []float64{1, 0}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.ID)
	}
	got, err := vs.Vectors(ids, 2)
	if err != nil {
		t.Fatalf("Vectors over %d ids: %v", len(ids), err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d vectors, want %d", len(got), len(ids))
	}
}

// TestVectorDimsReportsPopulations: a navi that changed embedding models has
// two populations of vectors, and this is how an operator sees that rather than
// inferring it from worsening recall (decision 8).
func TestVectorDimsReportsPopulations(t *testing.T) {
	store := openMem(t)
	// VectorDims is a backend-specific diagnostic, not part of the contract
	// interface, so this reaches for the concrete type.
	vs, ok := store.VectorStore().(*sqlite.VectorStore)
	if !ok {
		t.Fatalf("VectorStore() = %T, want *sqlite.VectorStore", store.VectorStore())
	}
	a := remember(t, store, datastore.ScopeMemories, "one")
	b := remember(t, store, datastore.ScopeMemories, "two")
	c := remember(t, store, datastore.ScopeMemories, "three")

	if err := vs.PutVector(a.ID, []float64{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := vs.PutVector(b.ID, []float64{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := vs.PutVector(c.ID, []float64{1, 0, 0}); err != nil {
		t.Fatal(err)
	}

	dims, err := vs.VectorDims()
	if err != nil {
		t.Fatal(err)
	}
	if dims[2] != 2 || dims[3] != 1 {
		t.Fatalf("dims = %v, want {2:2, 3:1}", dims)
	}
}
