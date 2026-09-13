// Package sqlite is the public SQLite implementation of store.Store
// (S000800 §01): sessions, thoughts, and memory share one encrypted
// database. Open with Open/OpenDB; encryption is selected via
// CipherFromEnv or an explicit Cipher.
//
// The ThoughtStore seam (thought_store.go) is retained here for the
// internal daemon; its queue-facing surface moves behind the public
// agent boundary with the phase-2 promotion (NAVI-277).
package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/store"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Store implements store.Store backed by SQLite. SaveSession is a
// whole-row replace inside one transaction: upsert the session row,
// delete and reinsert all message rows and tags. Manager read-modify-
// write methods rely on this store-level atomicity.
//
// Public surface note: session-facing methods operate on the public
// store data model only. ThoughtStore methods intentionally reference
// internal queue types and are not part of the authoring contract.
type Store struct {
	db     *sql.DB
	cipher Cipher
}

// DB exposes the underlying connection for diagnostics and tooling.
func (s *Store) DB() *sql.DB { return s.db }

// Options configures the store.
type Options struct {
	// Cipher encrypts sensitive columns; nil means NoopCipher.
	Cipher Cipher
}

// Open opens (creating if needed) the database at path, applies
// migrations, and returns a store.
func Open(path string, opts Options) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// modernc/sqlite allows one writer; serialize writes and bound
	// connection use to avoid SQLITE_BUSY under concurrency.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	cipher := opts.Cipher
	if cipher == nil {
		cipher = NoopCipher{}
	}
	return &Store{db: db, cipher: cipher}, nil
}

// OpenDB wraps an existing *sql.DB (driver "sqlite") in a Store. The
// caller owns the DB's lifecycle; Close is a no-op.
func OpenDB(db *sql.DB, opts Options) (*Store, error) {
	if err := migrate(db); err != nil {
		return nil, err
	}
	cipher := opts.Cipher
	if cipher == nil {
		cipher = NoopCipher{}
	}
	return &Store{db: db, cipher: cipher}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── SaveSession: whole-row replace in one transaction ────────────────────

func (s *Store) SaveSession(sess *store.Session) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: begin save: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO sessions
		(id, name, agent_id, profile, work_dir, branch_id, parent_id, label, status, current, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, agent_id=excluded.agent_id, profile=excluded.profile,
			work_dir=excluded.work_dir, branch_id=excluded.branch_id, parent_id=excluded.parent_id,
			label=excluded.label, status=excluded.status, current=excluded.current,
			updated_at=excluded.updated_at`,
		sess.ID, sess.Name, sess.AgentID, sess.Profile, sess.WorkDir,
		sess.BranchID, sess.ParentID, sess.Label, string(sess.Status),
		nilIfNegative(sess.Current),
		formatTime(sess.CreatedAt), formatTime(sess.UpdatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert session %s: %w", sess.ID, err)
	}

	// Whole-row replace for messages and tags.
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sess.ID); err != nil {
		return fmt.Errorf("sqlite: clear messages: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_tags WHERE session_id = ?`, sess.ID); err != nil {
		return fmt.Errorf("sqlite: clear tags: %w", err)
	}

	for i, e := range sess.Entries {
		content, err := s.encodeText([]byte(e.Message))
		if err != nil {
			return fmt.Errorf("sqlite: encrypt entry %s: %w", e.ID, err)
		}
		var toolInput any
		if len(e.ToolInput) > 0 {
			enc, err := s.encodeText(e.ToolInput)
			if err != nil {
				return fmt.Errorf("sqlite: encrypt tool input %s: %w", e.ID, err)
			}
			toolInput = string(enc)
		}
		compacted := 0
		if e.IsCompacted {
			compacted = 1
		}
		if _, err := tx.Exec(`INSERT INTO messages
			(id, session_id, entry_index, role, content, tool_call_id, tool_name, tool_input, is_compacted, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, sess.ID, i, string(e.Role), string(content),
			e.ToolCallID, e.ToolName, toolInput, compacted, formatTime(e.Time)); err != nil {
			return fmt.Errorf("sqlite: insert message %s: %w", e.ID, err)
		}
	}

	for _, tag := range sess.Tags {
		if _, err := tx.Exec(`INSERT INTO session_tags (session_id, tag) VALUES (?, ?)`, sess.ID, tag); err != nil {
			return fmt.Errorf("sqlite: insert tag %q: %w", tag, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit save %s: %w", sess.ID, err)
	}
	return nil
}

// ── LoadSession ──────────────────────────────────────────────────────────

func (s *Store) LoadSession(id string) (*store.Session, error) {
	sess := &store.Session{}
	var status string
	var createdAt, updatedAt string
	var storedCurrent sql.NullInt64
	err := s.db.QueryRow(`SELECT id, name, agent_id, profile, work_dir, branch_id, parent_id, label, status, current, created_at, updated_at
		FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.Name, &sess.AgentID, &sess.Profile, &sess.WorkDir,
			&sess.BranchID, &sess.ParentID, &sess.Label, &status, &storedCurrent, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, store.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: load session %s: %w", id, err)
	}
	sess.Status = store.Status(status)
	if sess.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if sess.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT id, entry_index, role, content, tool_call_id, tool_name, tool_input, is_compacted, created_at
		FROM messages WHERE session_id = ? ORDER BY entry_index`, id)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load messages for %s: %w", id, err)
	}
	defer rows.Close()

	sess.Entries = []store.SessionEntry{}
	for rows.Next() {
		var e store.SessionEntry
		var role, content string
		var entryIndex int
		var toolInput sql.NullString
		var compacted int
		var created string
		if err := rows.Scan(&e.ID, &entryIndex, &role, &content, &e.ToolCallID, &e.ToolName, &toolInput, &compacted, &created); err != nil {
			return nil, fmt.Errorf("sqlite: scan message in %s: %w", id, err)
		}
		e.Role = store.Role(role)
		plain, err := s.decodeText([]byte(content))
		if err != nil {
			return nil, err
		}
		e.Message = string(plain)
		if toolInput.Valid && toolInput.String != "" {
			plain, err := s.decodeText([]byte(toolInput.String))
			if err != nil {
				return nil, err
			}
			e.ToolInput = json.RawMessage(plain)
		}
		e.IsCompacted = compacted == 1
		if e.Time, err = parseTime(created); err != nil {
			return nil, err
		}
		sess.Entries = append(sess.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate messages in %s: %w", id, err)
	}
	// Derive the fallback position, then prefer the stored Current when
	// present and in range (clamped otherwise — entries may have changed
	// since the position was saved).
	if len(sess.Entries) > 0 {
		sess.Current = len(sess.Entries) - 1
	} else {
		sess.Current = -1
	}
	if storedCurrent.Valid {
		if c := int(storedCurrent.Int64); c >= 0 && c < len(sess.Entries) {
			sess.Current = c
		}
	}

	tags, err := s.loadTags(id)
	if err != nil {
		return nil, err
	}
	sess.Tags = tags
	return sess, nil
}

func (s *Store) loadTags(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT tag FROM session_tags WHERE session_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load tags for %s: %w", id, err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("sqlite: scan tag in %s: %w", id, err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

// ── DeleteSession ────────────────────────────────────────────────────────

func (s *Store) DeleteSession(id string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete session %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: delete session %s: rows: %w", id, err)
	}
	if n == 0 {
		return store.ErrSessionNotFound
	}
	return nil
}

// ── ListSessions ─────────────────────────────────────────────────────────

func (s *Store) ListSessions(opts store.ListOptions) ([]store.SessionInfo, error) {
	where := []string{"1=1"}
	args := []any{}
	if opts.Profile != "" {
		where = append(where, "s.profile = ?")
		args = append(args, opts.Profile)
	}
	if opts.AgentID != "" {
		where = append(where, "s.agent_id = ?")
		args = append(args, opts.AgentID)
	}
	if opts.Status != "" {
		where = append(where, "s.status = ?")
		args = append(args, string(opts.Status))
	}
	if !opts.Since.IsZero() {
		where = append(where, "s.updated_at >= ?")
		args = append(args, formatTime(opts.Since))
	}
	for _, tag := range opts.Tags {
		where = append(where, "EXISTS (SELECT 1 FROM session_tags t WHERE t.session_id = s.id AND t.tag = ?)")
		args = append(args, tag)
	}

	query := `SELECT s.id, s.name, s.agent_id, s.created_at, s.updated_at, s.status,
		(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS message_count,
		(SELECT json_group_array(t.tag) FROM session_tags t WHERE t.session_id = s.id) AS tags
		FROM sessions s WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY s.updated_at DESC`

	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	} else if opts.Offset > 0 {
		// SQLite only applies OFFSET together with a LIMIT clause.
		query += " LIMIT -1"
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list sessions: %w", err)
	}
	defer rows.Close()

	out := []store.SessionInfo{}
	for rows.Next() {
		info, err := scanSessionInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// scanSessionInfo scans one row of the shared session-list projection
// (metadata + message count + JSON tag array).
func scanSessionInfo(rows *sql.Rows) (store.SessionInfo, error) {
	var info store.SessionInfo
	var status, createdAt, updatedAt string
	var tags sql.NullString
	if err := rows.Scan(&info.ID, &info.Name, &info.AgentID, &createdAt, &updatedAt, &status, &info.MessageCount, &tags); err != nil {
		return info, fmt.Errorf("sqlite: scan session list: %w", err)
	}
	info.Status = store.Status(status)
	var err error
	if info.CreatedAt, err = parseTime(createdAt); err != nil {
		return info, err
	}
	if info.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return info, err
	}
	info.Tags = []string{}
	if tags.Valid && tags.String != "" {
		// json_group_array produces a JSON array; decode so tags
		// containing commas survive.
		if err := json.Unmarshal([]byte(tags.String), &info.Tags); err != nil {
			return info, fmt.Errorf("sqlite: decode tags %q: %w", tags.String, err)
		}
	}
	return info, nil
}

// ── codec helpers ────────────────────────────────────────────────────────

// encodeText encrypts via the cipher; the ciphertext (or plaintext for
// Noop) is stored with the enc1: prefix only when actually encrypted.
func (s *Store) encodeText(plain []byte) ([]byte, error) {
	ct, err := s.cipher.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	if _, isNoop := s.cipher.(NoopCipher); isNoop {
		return ct, nil
	}
	if strings.HasPrefix(string(ct), encPrefix) {
		return ct, nil // cipher already self-describing (AESGCM)
	}
	return append([]byte(encPrefix), ct...), nil
}

func (s *Store) decodeText(stored []byte) ([]byte, error) {
	if !strings.HasPrefix(string(stored), encPrefix) {
		return stored, nil
	}
	return s.cipher.Decrypt(stored)
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// nilIfNegative maps a negative Current (empty session) to SQL NULL.
func nilIfNegative(i int) any {
	if i < 0 {
		return nil
	}
	return i
}

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: parse time %q: %w", v, err)
	}
	return t, nil
}

func now() string { return formatTime(time.Now()) }
