package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flintwillow-software/opennavi/internal/thought"
)

// ThoughtStore implements thought.Store on SQLite (migration v3),
// persisting queue state in the same database as sessions per the #151
// handoff (reuse the store layer; the spec's YAML files are superseded
// — crash recovery is a load-side running→pending transition).
type ThoughtStore struct {
	db *sql.DB
}

// NewThoughtStore builds a thought store over the session store's
// connection (migrations must already be applied via Open/OpenDB).
func NewThoughtStore(db *sql.DB) (*ThoughtStore, error) {
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &ThoughtStore{db: db}, nil
}

// SaveThought upserts a thought row.
func (ts *ThoughtStore) SaveThought(t *thought.Thought) error {
	var metadata any
	if len(t.Metadata) > 0 {
		blob, err := json.Marshal(t.Metadata)
		if err != nil {
			return fmt.Errorf("sqlite: encode metadata for thought %s: %w", t.ID, err)
		}
		metadata = string(blob)
	}
	var checkpoint any
	if len(t.Checkpoint) > 0 {
		checkpoint = t.Checkpoint
	}
	var deferUntil any
	if !t.DeferUntil.IsZero() {
		deferUntil = formatTime(t.DeferUntil)
	}
	_, err := ts.db.Exec(`INSERT INTO thoughts
		(id, prompt, context, metadata, priority, status, session_id, slot_id, worker_id, log_path, checkpoint, defer_until, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			prompt=excluded.prompt, context=excluded.context, metadata=excluded.metadata,
			priority=excluded.priority, status=excluded.status, session_id=excluded.session_id,
			slot_id=excluded.slot_id, worker_id=excluded.worker_id, log_path=excluded.log_path,
			checkpoint=excluded.checkpoint, defer_until=excluded.defer_until, updated_at=excluded.updated_at`,
		t.ID, t.Prompt, t.Context, metadata, t.EffectivePriority(), string(t.Status),
		t.SessionID, t.SlotID, t.WorkerID, t.LogPath, checkpoint, deferUntil,
		formatTime(t.CreatedAt), formatTime(t.UpdatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert thought %s: %w", t.ID, err)
	}
	return nil
}

// LoadThoughts returns all persisted thoughts.
func (ts *ThoughtStore) LoadThoughts() ([]*thought.Thought, error) {
	rows, err := ts.db.Query(`SELECT id, prompt, context, metadata, priority, status, session_id, slot_id, worker_id, log_path, checkpoint, defer_until, created_at, updated_at
		FROM thoughts ORDER BY priority, updated_at`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load thoughts: %w", err)
	}
	defer rows.Close()

	out := []*thought.Thought{}
	for rows.Next() {
		t, err := scanThought(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LoadThought returns one thought by ID.
func (ts *ThoughtStore) LoadThought(id string) (*thought.Thought, error) {
	row := ts.db.QueryRow(`SELECT id, prompt, context, metadata, priority, status, session_id, slot_id, worker_id, log_path, checkpoint, defer_until, created_at, updated_at
		FROM thoughts WHERE id = ?`, id)
	t, err := scanThought(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, thought.ErrThoughtNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteThought removes a thought row.
func (ts *ThoughtStore) DeleteThought(id string) error {
	res, err := ts.db.Exec(`DELETE FROM thoughts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete thought %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: delete thought %s: rows: %w", id, err)
	}
	if n == 0 {
		return thought.ErrThoughtNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanThought(row rowScanner) (*thought.Thought, error) {
	t := &thought.Thought{}
	var status, createdAt, updatedAt string
	var contextStr, metadata, sessionID, slotID, workerID, logPath, deferUntil sql.NullString
	var checkpoint []byte
	if err := row.Scan(&t.ID, &t.Prompt, &contextStr, &metadata, &t.Priority, &status,
		&sessionID, &slotID, &workerID, &logPath, &checkpoint, &deferUntil, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("sqlite: scan thought: %w", err)
	}
	t.Context = contextStr.String
	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &t.Metadata); err != nil {
			return nil, fmt.Errorf("sqlite: decode thought metadata: %w", err)
		}
	}
	t.Status = thought.ThoughtStatus(status)
	t.SessionID = sessionID.String
	t.SlotID = slotID.String
	t.WorkerID = workerID.String
	t.LogPath = logPath.String
	t.Checkpoint = checkpoint
	if deferUntil.Valid && deferUntil.String != "" {
		var derr error
		if t.DeferUntil, derr = parseTime(deferUntil.String); derr != nil {
			return nil, derr
		}
	}
	var err error
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return t, nil
}
