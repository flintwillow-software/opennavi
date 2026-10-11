package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// ThoughtStore is the thought/transcript aggregate view on SQLite
// (migration 3 for the rows, migration 16 for the transcripts). It
// implements both thought.Store (the queue's persistence) and
// datastore.Thoughts (the contract), because the aggregate is the same set
// of rows either consumer reaches.
type ThoughtStore struct {
	store *Store
}

// NewThoughtStore builds a thought store over an existing connection
// (migrations must already be applied via Open/OpenDB). It is the seam for
// callers that hold a *sql.DB directly, such as the `navi enqueue` mailbox;
// a datastore backend constructs the view through Store.Thoughts instead.
func NewThoughtStore(db *sql.DB) (*ThoughtStore, error) {
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &ThoughtStore{store: &Store{db: db, cipher: NoopCipher{}}}, nil
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
	var skills any
	if len(t.Skills) > 0 {
		blob, err := json.Marshal(t.Skills)
		if err != nil {
			return fmt.Errorf("sqlite: encode skills for thought %s: %w", t.ID, err)
		}
		skills = string(blob)
	}
	var checkpoint any
	if len(t.Checkpoint) > 0 {
		checkpoint = t.Checkpoint
	}
	var dependsOn any
	if len(t.DependsOn) > 0 {
		blob, err := json.Marshal(t.DependsOn)
		if err != nil {
			return fmt.Errorf("sqlite: encode depends_on for thought %s: %w", t.ID, err)
		}
		dependsOn = string(blob)
	}
	var deferUntil any
	if !t.DeferUntil.IsZero() {
		deferUntil = formatTime(t.DeferUntil)
	}
	_, err := ts.store.db.Exec(`INSERT INTO thoughts
		(id, prompt, context, metadata, priority, status, slot_id, worker_id, log_path, checkpoint, defer_until, provider, model, mode, result, assembled, skills, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			prompt=excluded.prompt, context=excluded.context, metadata=excluded.metadata,
			priority=excluded.priority, status=excluded.status,
			slot_id=excluded.slot_id, worker_id=excluded.worker_id, log_path=excluded.log_path,
			checkpoint=excluded.checkpoint, defer_until=excluded.defer_until, provider=excluded.provider, model=excluded.model, mode=excluded.mode, result=excluded.result, assembled=excluded.assembled, skills=excluded.skills, depends_on=excluded.depends_on, updated_at=excluded.updated_at`,
		t.ID, t.Prompt, t.Context, metadata, t.EffectivePriority(), string(t.Status),
		t.SlotID, t.WorkerID, t.LogPath, checkpoint, deferUntil,
		t.Provider, t.Model, t.Mode, t.Result, t.Assembled, skills, dependsOn, formatTime(t.CreatedAt), formatTime(t.UpdatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert thought %s: %w", t.ID, err)
	}
	return nil
}

// LoadThoughts returns all persisted thoughts.
func (ts *ThoughtStore) LoadThoughts() ([]*thought.Thought, error) {
	rows, err := ts.store.db.Query(`SELECT id, prompt, context, metadata, priority, status, slot_id, worker_id, log_path, checkpoint, defer_until, provider, model, mode, result, assembled, skills, depends_on, created_at, updated_at
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
	row := ts.store.db.QueryRow(`SELECT id, prompt, context, metadata, priority, status, slot_id, worker_id, log_path, checkpoint, defer_until, provider, model, mode, result, assembled, skills, depends_on, created_at, updated_at
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
	res, err := ts.store.db.Exec(`DELETE FROM thoughts WHERE id = ?`, id)
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

// ── transcripts ─────────────────────────────────────────────────────────

// SaveTranscript replaces thoughtID's transcript as a unit, in one
// transaction: the entries and the metadata projection land together, so a
// reader sees the previous transcript or the new one and never a mixture.
// Message bodies and tool input are encrypted when the store has a cipher
// (cipher.go); the projection stays plaintext, so metadata reads never
// decrypt a conversation.
func (ts *ThoughtStore) SaveTranscript(thoughtID string, entries []datastore.Entry) error {
	tx, err := ts.store.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: begin transcript save for %s: %w", thoughtID, err)
	}
	defer tx.Rollback()

	latest := time.Time{}
	for _, e := range entries {
		if e.Time.After(latest) {
			latest = e.Time
		}
	}
	var updated any
	if !latest.IsZero() {
		updated = formatTime(latest)
	}
	if _, err := tx.Exec(`INSERT INTO thought_transcript_meta (thought_id, message_count, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(thought_id) DO UPDATE SET message_count=excluded.message_count, updated_at=excluded.updated_at`,
		thoughtID, len(entries), updated); err != nil {
		return fmt.Errorf("sqlite: save transcript metadata for %s: %w", thoughtID, err)
	}
	if _, err := tx.Exec(`DELETE FROM thought_transcripts WHERE thought_id = ?`, thoughtID); err != nil {
		return fmt.Errorf("sqlite: replace transcript for %s: %w", thoughtID, err)
	}
	for i, e := range entries {
		message, err := ts.store.encryptSecret(e.Message)
		if err != nil {
			return fmt.Errorf("sqlite: transcript %s entry %d: %w", thoughtID, i, err)
		}
		var toolInput any
		if len(e.ToolInput) > 0 {
			ct, err := ts.store.encryptSecret(string(e.ToolInput))
			if err != nil {
				return fmt.Errorf("sqlite: transcript %s entry %d tool input: %w", thoughtID, i, err)
			}
			toolInput = ct
		}
		if _, err := tx.Exec(`INSERT INTO thought_transcripts
			(thought_id, entry_index, id, role, message, tool_call_id, tool_name, tool_input, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			thoughtID, i, e.ID, string(e.Role), message,
			nullText(e.ToolCallID), nullText(e.ToolName), toolInput, formatTime(e.Time)); err != nil {
			return fmt.Errorf("sqlite: insert transcript %s entry %d: %w", thoughtID, i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit transcript for %s: %w", thoughtID, err)
	}
	return nil
}

// LoadTranscript reads a conversation, ordered by entry index. This is the
// only method that reads (and decrypts) message bodies.
func (ts *ThoughtStore) LoadTranscript(thoughtID string) ([]datastore.Entry, error) {
	var count int
	err := ts.store.db.QueryRow(`SELECT message_count FROM thought_transcript_meta WHERE thought_id = ?`, thoughtID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, datastore.ErrTranscriptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: read transcript metadata for %s: %w", thoughtID, err)
	}

	rows, err := ts.store.db.Query(`SELECT id, role, message, tool_call_id, tool_name, tool_input, created_at
		FROM thought_transcripts WHERE thought_id = ? ORDER BY entry_index`, thoughtID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load transcript %s: %w", thoughtID, err)
	}
	defer rows.Close()

	out := make([]datastore.Entry, 0, count)
	for rows.Next() {
		var e datastore.Entry
		var role, message, createdAt string
		var toolCallID, toolName, toolInput sql.NullString
		if err := rows.Scan(&e.ID, &role, &message, &toolCallID, &toolName, &toolInput, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan transcript %s: %w", thoughtID, err)
		}
		e.Role = datastore.Role(role)
		if e.Message, err = ts.store.decryptSecret(message); err != nil {
			return nil, fmt.Errorf("sqlite: transcript %s entry %s: %w", thoughtID, e.ID, err)
		}
		e.ToolCallID = toolCallID.String
		e.ToolName = toolName.String
		if toolInput.Valid && toolInput.String != "" {
			plain, err := ts.store.decryptSecret(toolInput.String)
			if err != nil {
				return nil, fmt.Errorf("sqlite: transcript %s entry %s tool input: %w", thoughtID, e.ID, err)
			}
			e.ToolInput = json.RawMessage(plain)
		}
		if e.Time, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ts.store.noteBodyRead()
	return out, nil
}

// TranscriptInfos enumerates every persisted transcript's metadata in one
// query over the metadata projection: no message body is selected, so cold
// start is one pass independent of transcript size (ADR 0011 decision 5).
func (ts *ThoughtStore) TranscriptInfos() ([]datastore.TranscriptInfo, error) {
	rows, err := ts.store.db.Query(`SELECT thought_id, message_count, updated_at FROM thought_transcript_meta`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read transcript metadata: %w", err)
	}
	defer rows.Close()

	out := []datastore.TranscriptInfo{}
	for rows.Next() {
		var info datastore.TranscriptInfo
		var updated sql.NullString
		if err := rows.Scan(&info.ThoughtID, &info.MessageCount, &updated); err != nil {
			return nil, fmt.Errorf("sqlite: scan transcript metadata: %w", err)
		}
		if updated.Valid && updated.String != "" {
			if info.UpdatedAt, err = parseTime(updated.String); err != nil {
				return nil, err
			}
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: enumerate transcript metadata: %w", err)
	}
	return out, nil
}

// TranscriptInfo answers from the metadata projection alone: no message body
// is selected, so enumerating transcripts is independent of transcript size.
func (ts *ThoughtStore) TranscriptInfo(thoughtID string) (datastore.TranscriptInfo, error) {
	var count int
	var updated sql.NullString
	err := ts.store.db.QueryRow(`SELECT message_count, updated_at FROM thought_transcript_meta WHERE thought_id = ?`, thoughtID).
		Scan(&count, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return datastore.TranscriptInfo{}, datastore.ErrTranscriptNotFound
	}
	if err != nil {
		return datastore.TranscriptInfo{}, fmt.Errorf("sqlite: read transcript metadata for %s: %w", thoughtID, err)
	}
	info := datastore.TranscriptInfo{ThoughtID: thoughtID, MessageCount: count}
	if updated.Valid && updated.String != "" {
		if info.UpdatedAt, err = parseTime(updated.String); err != nil {
			return datastore.TranscriptInfo{}, err
		}
	}
	return info, nil
}

// DeleteTranscript removes a thought's transcript. Deleting an absent
// transcript is not an error, matching the contract.
func (ts *ThoughtStore) DeleteTranscript(thoughtID string) error {
	tx, err := ts.store.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: begin transcript delete for %s: %w", thoughtID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM thought_transcripts WHERE thought_id = ?`, thoughtID); err != nil {
		return fmt.Errorf("sqlite: delete transcript %s: %w", thoughtID, err)
	}
	if _, err := tx.Exec(`DELETE FROM thought_transcript_meta WHERE thought_id = ?`, thoughtID); err != nil {
		return fmt.Errorf("sqlite: delete transcript metadata for %s: %w", thoughtID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit transcript delete for %s: %w", thoughtID, err)
	}
	return nil
}

// nullText maps an empty optional column value to NULL.
func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

type rowScanner interface{ Scan(dest ...any) error }

func scanThought(row rowScanner) (*thought.Thought, error) {
	t := &thought.Thought{}
	var status, createdAt, updatedAt string
	var contextStr, metadata, slotID, workerID, logPath, deferUntil sql.NullString
	var providerStr, modelStr, modeStr, resultStr, assembledStr, skillsJSON, dependsOnJSON sql.NullString
	var checkpoint []byte
	if err := row.Scan(&t.ID, &t.Prompt, &contextStr, &metadata, &t.Priority, &status,
		&slotID, &workerID, &logPath, &checkpoint, &deferUntil,
		&providerStr, &modelStr, &modeStr, &resultStr, &assembledStr, &skillsJSON, &dependsOnJSON, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("sqlite: scan thought: %w", err)
	}
	t.Context = contextStr.String
	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &t.Metadata); err != nil {
			return nil, fmt.Errorf("sqlite: decode thought metadata: %w", err)
		}
	}
	t.Status = thought.ThoughtStatus(status)
	t.SlotID = slotID.String
	t.WorkerID = workerID.String
	t.LogPath = logPath.String
	t.Checkpoint = checkpoint
	t.Provider = providerStr.String
	t.Model = modelStr.String
	t.Mode = modeStr.String
	t.Result = resultStr.String
	t.Assembled = assembledStr.String
	if skillsJSON.Valid && skillsJSON.String != "" {
		if err := json.Unmarshal([]byte(skillsJSON.String), &t.Skills); err != nil {
			return nil, fmt.Errorf("sqlite: decode thought skills: %w", err)
		}
	}
	if dependsOnJSON.Valid && dependsOnJSON.String != "" {
		if err := json.Unmarshal([]byte(dependsOnJSON.String), &t.DependsOn); err != nil {
			return nil, fmt.Errorf("sqlite: decode thought depends_on: %w", err)
		}
	}
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
