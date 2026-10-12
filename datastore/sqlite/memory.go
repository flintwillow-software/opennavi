package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/principal"
)

// MemoryStore is the memory aggregate view on SQLite (migration 4's memories
// table). Content stays plaintext: recall matches it with a literal substring,
// and the cipher's job is the transcript body it was designed for
// (cipher.go). Expiry is carried, never interpreted — retention is a separate
// mechanism (NAVI-363).
type MemoryStore struct{ store *Store }

// Remember stores a record, assigning an id when empty and preserving the
// creation time of one that already exists. A record is identified by its id
// alone, so a scope change updates the row rather than leaving a second copy.
func (m *MemoryStore) Remember(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = newID()
	}

	var created string
	err := m.store.db.QueryRow(`SELECT created_at FROM memories WHERE id = ?`, rec.ID).Scan(&created)
	switch {
	case err == nil:
		t, perr := parseTime(created)
		if perr != nil {
			return datastore.MemoryRecord{}, perr
		}
		rec.CreatedAt = t
	case errors.Is(err, sql.ErrNoRows):
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = now
		}
	default:
		return datastore.MemoryRecord{}, fmt.Errorf("sqlite: read memory %s: %w", rec.ID, err)
	}
	rec.UpdatedAt = now

	tags, err := encodeTags(rec.Tags)
	if err != nil {
		return datastore.MemoryRecord{}, err
	}
	var expires, source any
	if !rec.ExpiresAt.IsZero() {
		expires = formatTime(rec.ExpiresAt)
	}
	if rec.Source != "" {
		source = rec.Source
	}
	_, err = m.store.db.Exec(`INSERT INTO memories
		(id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			scope=excluded.scope, subject=excluded.subject, content=excluded.content,
			source=excluded.source, tags=excluded.tags, importance=excluded.importance,
			expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
		rec.ID, string(rec.Scope), rec.Subject.String(), rec.Content, source, tags, rec.Importance,
		expires, formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt))
	if err != nil {
		return datastore.MemoryRecord{}, fmt.Errorf("sqlite: upsert memory %s: %w", rec.ID, err)
	}
	return rec, nil
}

// Recall returns records matching the query, most relevant first. Scope and
// subject are filtered in SQL; content substring and tags are applied in Go so
// the matching (case-insensitive, every tag required) is exactly the
// contract's, independent of SQLite's collation or FTS tokenizer.
func (m *MemoryStore) Recall(q datastore.RecallQuery) ([]datastore.MemoryRecord, error) {
	query := `SELECT id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at
		FROM memories WHERE 1=1`
	args := []any{}
	if q.Scope != "" {
		query += ` AND scope = ?`
		args = append(args, string(q.Scope))
	}
	if !q.Subject.IsZero() {
		// Migration: a pre-principal row carries the navi's bare name, so a
		// navi-kind subject also matches its unqualified legacy form.
		if q.Subject.Kind() == principal.KindNavi {
			query += ` AND (subject = ? OR subject = ?)`
			args = append(args, q.Subject.String(), q.Subject.Name())
		} else {
			query += ` AND subject = ?`
			args = append(args, q.Subject.String())
		}
	}
	query += ` ORDER BY importance DESC, updated_at DESC`

	rows, err := m.store.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recall: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(q.Query)
	out := []datastore.MemoryRecord{}
	for rows.Next() {
		rec, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		if needle != "" && !strings.Contains(strings.ToLower(rec.Content), needle) {
			continue
		}
		if !hasAllTag(rec.Tags, q.Tags) {
			continue
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

// Forget deletes a record; an unknown id is a sentinel, not a silent no-op.
func (m *MemoryStore) Forget(id string) error {
	res, err := m.store.db.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: forget memory %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: forget memory %s: rows: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: memory %s: %w", id, datastore.ErrMemoryNotFound)
	}
	return nil
}

func scanMemory(row rowScanner) (datastore.MemoryRecord, error) {
	var rec datastore.MemoryRecord
	var scope, subject, content, createdAt, updatedAt string
	var source, tags, expiresAt sql.NullString
	if err := row.Scan(&rec.ID, &scope, &subject, &content, &source, &tags,
		&rec.Importance, &expiresAt, &createdAt, &updatedAt); err != nil {
		return datastore.MemoryRecord{}, fmt.Errorf("sqlite: scan memory: %w", err)
	}
	rec.Scope = datastore.Scope(scope)
	if strings.TrimSpace(subject) != "" {
		// Migration: read a bare legacy subject as navi:<name>.
		ref, err := principal.ParseLegacy(subject)
		if err != nil {
			return datastore.MemoryRecord{}, fmt.Errorf("sqlite: scan memory subject: %w", err)
		}
		rec.Subject = ref
	}
	rec.Content = content
	rec.Source = source.String
	if tags.Valid && tags.String != "" {
		if err := json.Unmarshal([]byte(tags.String), &rec.Tags); err != nil {
			return datastore.MemoryRecord{}, fmt.Errorf("sqlite: decode memory tags: %w", err)
		}
	}
	var err error
	if rec.CreatedAt, err = parseTime(createdAt); err != nil {
		return datastore.MemoryRecord{}, err
	}
	if rec.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return datastore.MemoryRecord{}, err
	}
	if expiresAt.Valid && expiresAt.String != "" {
		if rec.ExpiresAt, err = parseTime(expiresAt.String); err != nil {
			return datastore.MemoryRecord{}, err
		}
	}
	return rec, nil
}

// hasAllTag reports whether have contains every wanted tag.
func hasAllTag(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
