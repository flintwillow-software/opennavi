package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/store"
)

// StatefulStore implements store.StatefulToolStore on SQLite
// (migration 6): durable todo and note state for
// stateful catalog tools, in the same database as sessions/thoughts.
// Per-navi scoping is enforced here — every query is filtered on
// navi_id, so two navis sharing one DB never see each other's state.
type StatefulStore struct {
	db *sql.DB
}

// NewStatefulStore builds a stateful tool store over the session
// store's connection (migrations must already be applied via
// Open/OpenDB).
func NewStatefulStore(db *sql.DB) (*StatefulStore, error) {
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &StatefulStore{db: db}, nil
}

// ListTodos returns the navi's todos ordered by priority, then most
// recent activity, optionally filtered by status ("" = all).
func (s *StatefulStore) ListTodos(naviID string, status store.TodoStatus, limit int) ([]store.Todo, error) {
	q := `SELECT id, navi_id, text, status, priority, created_at, updated_at
		FROM todos WHERE navi_id = ?`
	args := []any{naviID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY priority, updated_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list todos for %s: %w", naviID, err)
	}
	defer rows.Close()

	out := []store.Todo{}
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceTodos atomically replaces the navi's entire todo list inside
// one transaction; on failure the previous list is intact.
func (s *StatefulStore) ReplaceTodos(naviID string, items []store.Todo) ([]store.Todo, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin todo replace for %s: %w", naviID, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM todos WHERE navi_id = ?`, naviID); err != nil {
		return nil, fmt.Errorf("sqlite: clear todos for %s: %w", naviID, err)
	}

	now := formatTime(time.Now())
	out := make([]store.Todo, 0, len(items))
	for i, item := range items {
		if item.Status == "" {
			item.Status = store.TodoPending
		}
		if !item.Status.Valid() {
			return nil, fmt.Errorf("sqlite: replace todos for %s: item %d has invalid status %q", naviID, i, item.Status)
		}
		id := item.ID
		if id == "" {
			id = newID()
		}
		ts := now
		_, err := tx.Exec(`INSERT INTO todos (id, navi_id, text, status, priority, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, naviID, item.Text, string(item.Status), item.Priority, ts, ts)
		if err != nil {
			return nil, fmt.Errorf("sqlite: insert todo for %s: %w", naviID, err)
		}
		stored, err := scanTodo(tx.QueryRow(`SELECT id, navi_id, text, status, priority, created_at, updated_at
			FROM todos WHERE navi_id = ? AND id = ?`, naviID, id))
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite: commit todo replace for %s: %w", naviID, err)
	}
	return out, nil
}

// UpdateTodos applies partial updates atomically: an unknown id
// rejects the whole batch and leaves prior state intact.
func (s *StatefulStore) UpdateTodos(naviID string, updates []store.TodoUpdate) ([]store.Todo, error) {
	if len(updates) == 0 {
		return []store.Todo{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin todo update for %s: %w", naviID, err)
	}
	defer tx.Rollback()

	now := formatTime(time.Now())
	for i, u := range updates {
		if u.Status != nil && !u.Status.Valid() {
			return nil, fmt.Errorf("sqlite: update todos for %s: update %d has invalid status %q", naviID, i, *u.Status)
		}
		if u.Remove {
			res, err := tx.Exec(`DELETE FROM todos WHERE navi_id = ? AND id = ?`, naviID, u.ID)
			if err != nil {
				return nil, fmt.Errorf("sqlite: delete todo %s for %s: %w", u.ID, naviID, err)
			}
			if n, err := res.RowsAffected(); err != nil {
				return nil, fmt.Errorf("sqlite: delete todo %s rows: %w", u.ID, err)
			} else if n == 0 {
				return nil, fmt.Errorf("sqlite: update todos for %s: %w (id %s)", naviID, store.ErrTodoNotFound, u.ID)
			}
			continue
		}
		sets, args := []string{"updated_at = ?"}, []any{now}
		if u.Text != nil {
			sets = append(sets, "text = ?")
			args = append(args, *u.Text)
		}
		if u.Status != nil {
			sets = append(sets, "status = ?")
			args = append(args, string(*u.Status))
		}
		if u.Priority != nil {
			sets = append(sets, "priority = ?")
			args = append(args, *u.Priority)
		}
		args = append(args, naviID, u.ID)
		res, err := tx.Exec(`UPDATE todos SET `+joinComma(sets)+` WHERE navi_id = ? AND id = ?`, args...)
		if err != nil {
			return nil, fmt.Errorf("sqlite: update todo %s for %s: %w", u.ID, naviID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("sqlite: update todo %s rows: %w", u.ID, err)
		} else if n == 0 {
			return nil, fmt.Errorf("sqlite: update todos for %s: %w (id %s)", naviID, store.ErrTodoNotFound, u.ID)
		}
	}

	out, err := listTodosTx(tx, naviID, "")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite: commit todo update for %s: %w", naviID, err)
	}
	return out, nil
}

// ReadNotes returns notes matching the query (id, tag, or literal
// substring on title/body), newest activity first.
func (s *StatefulStore) ReadNotes(naviID string, q store.NoteQuery) ([]store.Note, error) {
	where, args := `navi_id = ?`, []any{naviID}
	if q.ID != "" {
		where += ` AND id = ?`
		args = append(args, q.ID)
	}
	if q.Tag != "" {
		where += ` AND EXISTS (SELECT 1 FROM json_each(notes.tags) WHERE json_each.value = ?)`
		args = append(args, q.Tag)
	}
	if q.Query != "" {
		where += ` AND (title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\')`
		pat := likeEscape(q.Query)
		args = append(args, pat, pat)
	}
	query := `SELECT id, navi_id, title, body, tags, created_at, updated_at
		FROM notes WHERE ` + where + ` ORDER BY updated_at DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read notes for %s: %w", naviID, err)
	}
	defer rows.Close()

	out := []store.Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpsertNote stores a note verbatim; new id assigned when n.ID is
// empty, existing note replaced when it matches. Unknown ids on
// explicit update return ErrNoteNotFound.
func (s *StatefulStore) UpsertNote(naviID string, n store.Note) (store.Note, error) {
	now := formatTime(time.Now())
	if n.ID == "" {
		n.ID = newID()
		n.CreatedAt = time.Now()
	} else {
		err := s.db.QueryRow(`SELECT created_at FROM notes WHERE navi_id = ? AND id = ?`, naviID, n.ID).Scan(&n.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			n.CreatedAt = time.Now()
		} else if err != nil {
			return store.Note{}, fmt.Errorf("sqlite: upsert note %s for %s: %w", n.ID, naviID, err)
		}
	}
	tags, err := encodeTags(n.Tags)
	if err != nil {
		return store.Note{}, fmt.Errorf("sqlite: upsert note %s for %s: %w", n.ID, naviID, err)
	}
	_, err = s.db.Exec(`INSERT INTO notes (id, navi_id, title, body, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(navi_id, id) DO UPDATE SET
			title=excluded.title, body=excluded.body, tags=excluded.tags, updated_at=excluded.updated_at`,
		n.ID, naviID, n.Title, n.Body, tags, formatTime(n.CreatedAt), now)
	if err != nil {
		return store.Note{}, fmt.Errorf("sqlite: upsert note %s for %s: %w", n.ID, naviID, err)
	}
	n.NaviID = naviID
	n.UpdatedAt = time.Now()
	return n, nil
}

// RemoveNote deletes the note; unknown ids return ErrNoteNotFound.
func (s *StatefulStore) RemoveNote(naviID, id string) error {
	res, err := s.db.Exec(`DELETE FROM notes WHERE navi_id = ? AND id = ?`, naviID, id)
	if err != nil {
		return fmt.Errorf("sqlite: remove note %s for %s: %w", id, naviID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: remove note %s rows: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: remove note for %s: %w (id %s)", naviID, store.ErrNoteNotFound, id)
	}
	return nil
}

func listTodosTx(tx *sql.Tx, naviID string, status store.TodoStatus) ([]store.Todo, error) {
	q := `SELECT id, navi_id, text, status, priority, created_at, updated_at
		FROM todos WHERE navi_id = ?`
	args := []any{naviID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY priority, updated_at DESC`
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list todos for %s: %w", naviID, err)
	}
	defer rows.Close()
	out := []store.Todo{}
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTodo(row rowScanner) (store.Todo, error) {
	t := store.Todo{}
	var status, createdAt, updatedAt string
	if err := row.Scan(&t.ID, &t.NaviID, &t.Text, &status, &t.Priority, &createdAt, &updatedAt); err != nil {
		return store.Todo{}, fmt.Errorf("sqlite: scan todo: %w", err)
	}
	t.Status = store.TodoStatus(status)
	var err error
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.Todo{}, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return store.Todo{}, err
	}
	return t, nil
}

func scanNote(row rowScanner) (store.Note, error) {
	n := store.Note{}
	var tags sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&n.ID, &n.NaviID, &n.Title, &n.Body, &tags, &createdAt, &updatedAt); err != nil {
		return store.Note{}, fmt.Errorf("sqlite: scan note: %w", err)
	}
	if tags.Valid && tags.String != "" {
		if err := json.Unmarshal([]byte(tags.String), &n.Tags); err != nil {
			return store.Note{}, fmt.Errorf("sqlite: decode note tags: %w", err)
		}
	}
	var err error
	if n.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.Note{}, err
	}
	if n.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return store.Note{}, err
	}
	return n, nil
}

// encodeTags serializes note tags to the JSON TEXT column (nil/empty
// stores as NULL).
func encodeTags(tags []string) (any, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	blob, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("encode tags: %w", err)
	}
	return string(blob), nil
}

// likeEscape escapes SQL LIKE metacharacters so the note substring
// query matches literally.
func likeEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return "%" + string(out) + "%"
}

// newID generates a random 128-bit hex id for new todos/notes
// (crypto/rand; no uuid dependency needed).
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("sqlite: generate id: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// joinComma joins SQL SET fragments with ", ".
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
