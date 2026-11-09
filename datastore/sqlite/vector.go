package sqlite

import (
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
)

// VectorStore is the sqlite backend's optional vector capability (ADR 0040
// decisions 7 and 8), over migration 5's memory_embeddings table.
//
// Vectors are stored as BLOBs in the same database and compared by brute-force
// cosine in Go, which is migration 5's own design note rather than a new
// decision: sqlite-vec/vec0 is a C extension and is unshippable under
// CGO_ENABLED=0, which this backend requires (modernc.org/sqlite is the pure-Go
// driver). Brute force is linear in the archive size — the same shape as the
// scan the FTS5 change removed — so the vector path does not make search
// asymptotically worse.
//
// This type implements only the storage half of datastore.VectorStore: put,
// delete, and read back. Ranking is the caller's (internal/memory), because
// cosine and the fusion of the two result lists are policy, and policy does not
// belong in a storage contract three backends share.
type VectorStore struct{ store *Store }

// Vectors implements datastore.VectorStore.
//
// A missing vector is simply absent from the result — an archive is
// heterogeneous by design (ADR 0040 decision 5), since entries written before
// an embedder was declared have none. A vector whose stored dimension count
// does not match dims is omitted rather than returned (decision 8): cosine
// between two embedding spaces is meaningless, and omitting degrades that entry
// to keyword-only, which is the honest outcome.
func (v *VectorStore) Vectors(ids []string, dims int) (map[string][]float64, error) {
	out := map[string][]float64{}
	if len(ids) == 0 {
		return out, nil
	}
	if dims <= 0 {
		return nil, fmt.Errorf("sqlite: vectors: dims must be positive, got %d", dims)
	}

	// Chunked so a large id list cannot exceed SQLite's variable limit
	// (default 32766 in modernc.org/sqlite). The archive is searched rather
	// than injected, so a caller may legitimately ask about many ids.
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		for _, id := range batch {
			args = append(args, id)
		}
		args = append(args, dims)

		rows, err := v.store.db.Query(
			`SELECT memory_id, embedding FROM memory_embeddings WHERE memory_id IN (`+placeholders+`) AND dims = ?`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("sqlite: vectors: %w", err)
		}
		for rows.Next() {
			var id string
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sqlite: vectors: scan: %w", err)
			}
			vec, err := datastore.DecodeVector(blob, dims)
			if err != nil {
				// A row whose blob is the wrong size is corrupt or was
				// written by another encoding. Skip it rather than failing
				// the search: one bad row should not make the archive
				// unsearchable, and the entry degrades to keyword-only.
				continue
			}
			out[id] = vec
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlite: vectors: rows: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// PutVector implements datastore.VectorStore.
//
// The row references memories(id) with ON DELETE CASCADE, so an entry's vector
// dies with the entry (which is what keeps Forget honest without a second
// delete) and a vector for a non-existent entry is refused by the foreign key
// rather than stored as an orphan.
func (v *VectorStore) PutVector(memoryID string, vec []float64) error {
	if strings.TrimSpace(memoryID) == "" {
		return fmt.Errorf("sqlite: put vector: an entry id is required")
	}
	if len(vec) == 0 {
		return fmt.Errorf("sqlite: put vector: the vector is empty")
	}
	_, err := v.store.db.Exec(`INSERT INTO memory_embeddings (memory_id, embedding, dims, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET
			embedding=excluded.embedding, dims=excluded.dims, created_at=excluded.created_at`,
		memoryID, datastore.EncodeVector(vec), len(vec), formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("sqlite: put vector for %s: %w", memoryID, err)
	}
	return nil
}

// DeleteVector implements datastore.VectorStore. A missing vector is not an
// error: an entry that was never embedded has nothing to delete, and a caller
// pruning a heterogeneous archive should not have to know which.
func (v *VectorStore) DeleteVector(memoryID string) error {
	if strings.TrimSpace(memoryID) == "" {
		return fmt.Errorf("sqlite: delete vector: an entry id is required")
	}
	if _, err := v.store.db.Exec(`DELETE FROM memory_embeddings WHERE memory_id = ?`, memoryID); err != nil {
		return fmt.Errorf("sqlite: delete vector for %s: %w", memoryID, err)
	}
	return nil
}

// VectorDims reports the distinct dimensions stored, and how many rows hold
// each. It is diagnostic: a navi that changed embedding models has two
// populations (ADR 0040 decision 8), and this is how an operator sees that
// rather than inferring it from worsening recall.
func (v *VectorStore) VectorDims() (map[int]int, error) {
	rows, err := v.store.db.Query(`SELECT dims, COUNT(*) FROM memory_embeddings GROUP BY dims ORDER BY dims`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: vector dims: %w", err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var dims, n int
		if err := rows.Scan(&dims, &n); err != nil {
			return nil, fmt.Errorf("sqlite: vector dims: scan: %w", err)
		}
		out[dims] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: vector dims: rows: %w", err)
	}
	return out, nil
}

// Ensure VectorStore satisfies the optional contract. It is asserted here so a
// signature drift is a build failure rather than a silent loss of the
// capability — a backend that stops satisfying the interface would otherwise
// just quietly become keyword-only.
var _ datastore.VectorStore = (*VectorStore)(nil)
