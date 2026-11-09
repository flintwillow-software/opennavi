package datastore

import (
	"math"
	"testing"
)

// TestCosineSimilarityBasics: identical vectors are 1, orthogonal are 0,
// opposite are -1.
func TestCosineSimilarityBasics(t *testing.T) {
	a := []float64{1, 2, 3}
	if got := CosineSimilarity(a, a); math.Abs(got-1) > 1e-9 {
		t.Errorf("identical = %v, want 1", got)
	}
	if got := CosineSimilarity([]float64{1, 0}, []float64{0, 1}); math.Abs(got) > 1e-9 {
		t.Errorf("orthogonal = %v, want 0", got)
	}
	if got := CosineSimilarity([]float64{1, 0}, []float64{-1, 0}); math.Abs(got+1) > 1e-9 {
		t.Errorf("opposite = %v, want -1", got)
	}
}

// TestCosineSimilarityIsScaleInvariant: cosine measures direction, not
// magnitude, which is why it is the right measure for embeddings.
func TestCosineSimilarityIsScaleInvariant(t *testing.T) {
	a := []float64{1, 2, 3}
	b := []float64{2, 4, 6}
	if got := CosineSimilarity(a, b); math.Abs(got-1) > 1e-9 {
		t.Errorf("scaled copy = %v, want 1 (cosine is scale-invariant)", got)
	}
}

// TestCosineSimilarityZeroVectorIsZero: the zero vector has no direction, so
// its similarity is defined as 0 rather than NaN. NaN would silently poison a
// sort, and a zero vector is what a broken embedder produces.
func TestCosineSimilarityZeroVectorIsZero(t *testing.T) {
	zero := []float64{0, 0, 0}
	if got := CosineSimilarity(zero, []float64{1, 2, 3}); got != 0 {
		t.Errorf("zero vector = %v, want 0 (not NaN)", got)
	}
	if got := CosineSimilarity(zero, zero); got != 0 {
		t.Errorf("zero vs zero = %v, want 0", got)
	}
}

// TestCosineSimilarityMismatchedLengthsIsZero is ADR 0040 decision 8's rule at
// the arithmetic level: two different embedding spaces are not comparable.
func TestCosineSimilarityMismatchedLengthsIsZero(t *testing.T) {
	if got := CosineSimilarity([]float64{1, 2, 3}, []float64{1, 2}); got != 0 {
		t.Errorf("mismatched lengths = %v, want 0", got)
	}
	if got := CosineSimilarity(nil, []float64{1}); got != 0 {
		t.Errorf("nil vs vector = %v, want 0", got)
	}
}

// TestVectorEncodeDecodeRoundTrip: a stored vector must be the same vector that
// was ranked, so the encoding is lossless (float64, not narrowed to float32).
func TestVectorEncodeDecodeRoundTrip(t *testing.T) {
	vec := []float64{0, 1, -1, 0.5, math.Pi, -math.E, 1e-9, -1e9}
	blob := EncodeVector(vec)
	if len(blob) != len(vec)*8 {
		t.Fatalf("blob = %d bytes, want %d", len(blob), len(vec)*8)
	}
	got, err := DecodeVector(blob, len(vec))
	if err != nil {
		t.Fatalf("DecodeVector: %v", err)
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("element %d = %v, want %v (encoding must be lossless)", i, got[i], vec[i])
		}
	}
}

// TestVectorEncodeDecodeEmpty: an empty vector round-trips to empty.
func TestVectorEncodeDecodeEmpty(t *testing.T) {
	if blob := EncodeVector(nil); len(blob) != 0 {
		t.Fatalf("nil vector encoded to %d bytes, want 0", len(blob))
	}
	got, err := DecodeVector(nil, 0)
	if err == nil || got != nil {
		t.Fatalf("DecodeVector(nil, 0) = %v, %v; want an error (dims must be positive)", got, err)
	}
}

// TestDecodeVectorRejectsWrongSize: a blob of the wrong length is corrupt or
// foreign, and returning a short vector would make it comparable to nothing.
func TestDecodeVectorRejectsWrongSize(t *testing.T) {
	blob := EncodeVector([]float64{1, 2, 3})
	if _, err := DecodeVector(blob, 2); err == nil {
		t.Error("a blob that is not dims float64 values must be an error")
	}
	if _, err := DecodeVector(blob[:10], 3); err == nil {
		t.Error("a truncated blob must be an error")
	}
	if _, err := DecodeVector(blob, 0); err == nil {
		t.Error("dims=0 must be an error")
	}
}
