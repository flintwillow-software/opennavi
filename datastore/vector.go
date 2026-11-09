package datastore

import (
	"errors"
	"fmt"
	"math"
)

// ErrNoVectorStore is returned when a caller asks a backend for vector search
// and the backend has no vector index.
var ErrNoVectorStore = errors.New("datastore: backend has no vector index")

// VectorStore is an OPTIONAL backend capability: storing an entry's embedding
// and finding the entries nearest a query vector (ADR 0040 decisions 6 and 7).
//
// It is a separate interface rather than methods on Memory because it is not
// part of the memory contract — it is a capability a backend may add. Adding
// it to Memory would force every backend to implement vector search, including
// the in-memory and filesystem backends that have no reason to, and would make
// "this backend cannot do vectors" a compile error rather than a runtime
// capability question. A caller asks with a type assertion:
//
//	if vs, ok := memory.(datastore.VectorStore); ok { ... }
//
// which is the same shape conformance.ReadReporter uses for its optional
// instrumentation. A backend without it is not broken; it is keyword-only,
// which ADR 0040 decision 2 makes a supported configuration.
//
// The contract is deliberately small: store a vector, delete one, and read the
// vectors back. **Ranking is not here.** Cosine similarity and the fusion of
// the two result lists are the caller's job (internal/memory), because they are
// policy — how many neighbours to consider, how to blend with the keyword
// path — and policy does not belong in a storage contract that three backends
// share. The store's only obligation is to return the vectors it holds for the
// requested entries, accurately, so the caller can rank them.
type VectorStore interface {
	// PutVector stores or replaces the embedding for a memory entry. The
	// entry need not exist yet at the caller's layer — the SQLite backend
	// relies on the foreign key, so an orphan vector is refused rather than
	// silently stored.
	PutVector(memoryID string, vec []float64) error
	// DeleteVector removes an entry's embedding. An entry with no vector is
	// not an error: an archive is heterogeneous by design (ADR 0040 decision
	// 5), since entries written before an embedder was declared have none.
	DeleteVector(memoryID string) error
	// Vectors returns the stored embedding for each id that has one, keyed by
	// id. Ids without a vector are simply absent from the map — that is the
	// normal case for a pre-embedder entry, not a failure. A vector whose
	// dimensions do not match dims is OMITTED rather than returned (ADR 0040
	// decision 8): cosine between two different embedding spaces is
	// meaningless, and omitting degrades that entry to keyword-only, which is
	// the same outcome as having no vector and is the honest one.
	Vectors(ids []string, dims int) (map[string][]float64, error)
}

// CosineSimilarity returns the cosine similarity of two vectors, in [-1, 1].
//
// It is here, in the contract package, because it is the definition of "how
// near" the vector path uses — the same reason the keyword path's matching rule
// lives in the store. An implementation of it elsewhere would let the ranking
// and any future caller disagree about what similarity means.
//
// The zero vector has no direction, so its similarity to anything is defined as
// 0 rather than NaN: a vector of all zeros is what a broken embedder would
// produce, and NaN would silently poison a sort. Mismatched lengths are also 0
// for the same reason (decision 8) — though a caller filtering by dimension
// first should never reach that case.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// EncodeVector packs a float64 vector into a little-endian byte slice for
// storage as a BLOB, and DecodeVector unpacks it.
//
// float64 rather than float32 because the embedder contract produces float64
// (model.Embedder) and a lossy narrowing at the storage boundary would make the
// stored vector a different vector from the one the caller ranked. The size
// cost is real and accepted: an archive entry's vector is 8 bytes per
// dimension, so a 768-dimension model is 6 KiB per entry, and ADR 0040
// decision 7 accepts brute force over exactly this.
func EncodeVector(vec []float64) []byte {
	out := make([]byte, 8*len(vec))
	for i, v := range vec {
		bits := math.Float64bits(v)
		out[i*8] = byte(bits)
		out[i*8+1] = byte(bits >> 8)
		out[i*8+2] = byte(bits >> 16)
		out[i*8+3] = byte(bits >> 24)
		out[i*8+4] = byte(bits >> 32)
		out[i*8+5] = byte(bits >> 40)
		out[i*8+6] = byte(bits >> 48)
		out[i*8+7] = byte(bits >> 56)
	}
	return out
}

// DecodeVector unpacks a vector stored by EncodeVector. dims is the expected
// dimension count, which must match the encoded length exactly — a blob of the
// wrong size is a corrupt or foreign row, and returning a short vector would
// make it comparable to nothing.
func DecodeVector(blob []byte, dims int) ([]float64, error) {
	if dims <= 0 {
		return nil, fmt.Errorf("datastore: decode vector: dims must be positive, got %d", dims)
	}
	if len(blob) != dims*8 {
		return nil, fmt.Errorf("datastore: decode vector: %d bytes is not %d float64 values", len(blob), dims)
	}
	out := make([]float64, dims)
	for i := range out {
		bits := uint64(blob[i*8]) |
			uint64(blob[i*8+1])<<8 |
			uint64(blob[i*8+2])<<16 |
			uint64(blob[i*8+3])<<24 |
			uint64(blob[i*8+4])<<32 |
			uint64(blob[i*8+5])<<40 |
			uint64(blob[i*8+6])<<48 |
			uint64(blob[i*8+7])<<56
		out[i] = math.Float64frombits(bits)
	}
	return out, nil
}
