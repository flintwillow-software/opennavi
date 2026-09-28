package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/flintwillow-software/opennavi/llm"
)

// Index is the vector index over the memories tier (Tier 5):
// embeddings stored as BLOBs in the shared SQLite DB (migration v5),
// searched by brute-force cosine similarity in pure Go. Design note:
// sqlite-vec/vec0 is a C extension (unshippable under CGO_ENABLED=0);
// brute-force cosine is single-digit milliseconds at single-navi scale
// (thousands of entries × 768 dims), and chromem-go was rejected as a
// second storage mechanism. FTS5 (keyword) and vectors (semantic) are
// complementary.
type Index struct {
	store    *Store
	db       *sql.DB
	embedder llm.Embedder
}

// NewIndex builds a vector index bound to the memory store's database
// and an embedder. Applies migration v5 (idempotent).
func NewIndex(s *Store, embedder llm.Embedder) (*Index, error) {
	if embedder == nil {
		return nil, fmt.Errorf("memory: index requires an embedder")
	}
	if err := s.migrateEmbeddings(); err != nil {
		return nil, err
	}
	return &Index{store: s, db: s.db, embedder: embedder}, nil
}

// migrateEmbeddings applies migration v5 (idempotent).
func (s *Store) migrateEmbeddings() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS memory_embeddings (
		memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
		embedding BLOB NOT NULL,
		dims INTEGER NOT NULL,
		created_at DATETIME NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("memory: create memory_embeddings: %w", err)
	}
	return nil
}

// Upsert indexes a memory entry: embeds its content and stores the
// vector keyed by the entry ID.
func (ix *Index) Upsert(ctx context.Context, memoryID, text string) error {
	vec, err := ix.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("memory: index %s: %w", memoryID, err)
	}
	blob, err := encodeVector(vec)
	if err != nil {
		return err
	}
	_, err = ix.db.Exec(`INSERT INTO memory_embeddings (memory_id, embedding, dims, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET
			embedding=excluded.embedding, dims=excluded.dims, created_at=excluded.created_at`,
		memoryID, blob, len(vec), formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("memory: index %s: %w", memoryID, err)
	}
	return nil
}

// Delete removes a memory's embedding (the FK cascade requires
// foreign_keys pragma; explicit removal keeps the index consistent).
func (ix *Index) Delete(memoryID string) error {
	_, err := ix.db.Exec(`DELETE FROM memory_embeddings WHERE memory_id = ?`, memoryID)
	if err != nil {
		return fmt.Errorf("memory: delete embedding %s: %w", memoryID, err)
	}
	return nil
}

// Reconcile removes embeddings whose memory entry no longer exists
// (orphans left by Delete/Prune when the index wasn't told). Returns
// the number removed.
func (ix *Index) Reconcile() (int64, error) {
	res, err := ix.db.Exec(`DELETE FROM memory_embeddings
		WHERE memory_id NOT IN (SELECT id FROM memories)`)
	if err != nil {
		return 0, fmt.Errorf("memory: reconcile embeddings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("memory: reconcile embeddings: rows: %w", err)
	}
	return n, nil
}

// VectorSearch returns the k most similar memory IDs to the given
// query vector, with cosine similarity scores in [-1, 1] (1 = identical
// direction), highest first. Vectors of a different dimensionality are
// skipped.
func (ix *Index) VectorSearch(ctx context.Context, query []float64, k int) ([]Scored, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("memory: empty query vector")
	}
	rows, err := ix.db.Query(`SELECT memory_id, embedding, dims FROM memory_embeddings WHERE dims = ?`, len(query))
	if err != nil {
		return nil, fmt.Errorf("memory: vector scan: %w", err)
	}
	defer rows.Close()

	type hit struct {
		id   string
		rank float64
	}
	var hits []hit
	for rows.Next() {
		var id string
		var blob []byte
		var dims int
		if err := rows.Scan(&id, &blob, &dims); err != nil {
			return nil, fmt.Errorf("memory: scan embedding: %w", err)
		}
		vec, err := decodeVector(blob)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit{id: id, rank: cosine(query, vec)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: iterate embeddings: %w", err)
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].rank > hits[j].rank })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Scored, len(hits))
	for i, h := range hits {
		out[i] = Scored{MemoryID: h.id, Score: h.rank}
	}
	return out, nil
}

// Scored is one vector-search hit.
type Scored struct {
	MemoryID string
	Score    float64
}

// ScoredEntry pairs a memory entry with its similarity score.
type ScoredEntry struct {
	Entry Entry
	Score float64
}

// SemanticSearch embeds the query via the embedder, finds the top-k
// similar memories, and hydrates them from the store
// (results are reference material, injected on demand). Orphaned
// vectors (entry pruned without index cleanup) are skipped.
func (ix *Index) SemanticSearch(ctx context.Context, query string, k int) ([]ScoredEntry, error) {
	vec, err := ix.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("memory: embed query: %w", err)
	}
	hits, err := ix.VectorSearch(ctx, vec, k)
	if err != nil {
		return nil, err
	}
	out := make([]ScoredEntry, 0, len(hits))
	for _, h := range hits {
		entry, err := ix.store.Get(ScopeMemories, h.MemoryID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // vector outlived its entry (pruned); skip
			}
			return nil, err
		}
		out = append(out, ScoredEntry{Entry: entry, Score: h.Score})
	}
	return out, nil
}

// ── vector math (pure Go) ────────────────────────────────────────────────

// encodeVector packs floats as little-endian float32 bytes (compact
// BLOB storage; float32 precision is ample for similarity ranking).
func encodeVector(vec []float64) ([]byte, error) {
	out := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(v)))
	}
	return out, nil
}

func decodeVector(blob []byte) ([]float64, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("memory: corrupted embedding blob (len %d)", len(blob))
	}
	out := make([]float64, len(blob)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:])))
	}
	return out, nil
}

// cosine returns the cosine similarity of two vectors (0-length or
// zero-magnitude vectors yield 0).
func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
