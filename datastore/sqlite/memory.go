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

// Recall returns records matching the query, most relevant first.
//
// Scope, subject, and — when a query is given — the content match are filtered
// in SQL; tags are applied in Go, because "every tag required" over a JSON
// array is exactly the contract's and has no SQL spelling worth having.
//
// The content match uses the memories_fts index (migration 4) rather than an
// in-Go substring scan. That index has always been maintained on every write
// (memories_ai/ad/au) and was never queried, so this is using an index the
// store was already paying for rather than adding one. See recallSQL for the
// one behavioural difference this introduces, which is the point of an index.
func (m *MemoryStore) Recall(q datastore.RecallQuery) ([]datastore.MemoryRecord, error) {
	query, args := recallSQL(q)

	rows, err := m.store.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recall: %w", err)
	}
	defer rows.Close()

	out := []datastore.MemoryRecord{}
	for rows.Next() {
		rec, err := scanMemory(rows)
		if err != nil {
			return nil, err
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

// recallSQL builds the recall statement and its arguments.
//
// When a query is present the statement JOINs memories_fts and filters with
// MATCH, which makes the search an index lookup instead of a full scan. The
// match is a TOKEN match (unicode61), where the previous in-Go scan was a
// SUBSTRING match — the documented behaviour change of ADR 0040 decision 3:
// "config" now finds "configuration", and a mid-word fragment like "onfig" no
// longer finds it. This is the point of using an index; the contract's wording
// ("a literal substring match unless a backend supports more") already allows
// it, and ltm_search's description states which matching applies.
//
// The ORDER BY is unchanged: importance, then recency. The FTS rank is
// deliberately not used here — this is the keyword path, and its ordering is
// the archive's own. Blending a rank into it is the vector path's job (ADR
// 0040 decision 6).
func recallSQL(q datastore.RecallQuery) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT m.id, m.scope, m.subject, m.content, m.source, m.tags, m.importance, `)
	b.WriteString(`m.expires_at, m.created_at, m.updated_at FROM memories m`)
	args := []any{}

	// A query of only whitespace has no terms, so it is not a constraint —
	// the same reading the contract gives an empty query. Adding the JOIN for
	// it would match nothing and silently turn "no filter" into "no results".
	match := ftsQuery(q.Query)
	hasMatch := len(strings.Fields(q.Query)) > 0

	if hasMatch {
		b.WriteString(` JOIN memories_fts f ON f.rowid = m.rowid`)
	}

	b.WriteString(` WHERE 1=1`)
	if q.ID != "" {
		b.WriteString(` AND m.id = ?`)
		args = append(args, q.ID)
	}
	if q.Scope != "" {
		b.WriteString(` AND m.scope = ?`)
		args = append(args, string(q.Scope))
	}
	if !q.Subject.IsZero() {
		// Migration: a pre-principal row carries the navi's bare name, so a
		// navi-kind subject also matches its unqualified legacy form.
		if q.Subject.Kind() == principal.KindNavi {
			b.WriteString(` AND (m.subject = ? OR m.subject = ?)`)
			args = append(args, q.Subject.String(), q.Subject.Name())
		} else {
			b.WriteString(` AND m.subject = ?`)
			args = append(args, q.Subject.String())
		}
	}
	if hasMatch {
		b.WriteString(` AND memories_fts MATCH ?`)
		args = append(args, match)
	}

	b.WriteString(` ORDER BY m.importance DESC, m.updated_at DESC`)
	return b.String(), args
}

// ftsQuery turns a user's search text into an FTS5 MATCH expression that
// cannot fail to parse.
//
// FTS5's query language is not free text: a bare string is a sequence of terms
// combined implicitly with AND, and the characters `" * ( ) : ^ -` and the
// keywords AND/OR/NOT are syntax. A navi searching for "what did I do about
// the -foo thing" would otherwise hand FTS5 a malformed expression and get a
// parse error instead of a search — a tool that fails on ordinary prose is
// worse than one that searches less cleverly.
//
// So each whitespace-separated term is quoted as a PHRASE and the terms are
// joined with AND. Quoting makes every term literal, so no operator can leak
// through from a navi's prose. Embedded double quotes are doubled, which is
// FTS5's own escape.
//
// Each term also gets a trailing `*` (a prefix query) and that is load-bearing
// rather than decorative: FTS5 matches whole TOKENS by default, so a bare
// `config` does NOT match `configuration`. Since ADR 0040 decision 3 promises
// that a topic word finds its inflected and compound forms — the behaviour an
// index exists to provide, and the one the ADR records as a change from
// substring scanning — the `*` is what delivers it. It is appended OUTSIDE the
// quotes, where FTS5 requires it; inside would search for a literal asterisk.
//
// A term that is only punctuation (`--dry-run`, `(parentheses)`) quotes to a
// phrase that matches nothing, which is correct: the tokenizer never produced
// it. The alternative — stripping punctuation — would silently search for
// something the caller did not write.
func ftsQuery(text string) string {
	terms := strings.Fields(text)
	if len(terms) == 0 {
		// Recall does not call this for an empty query, but a bare empty
		// MATCH is a parse error while a quoted empty phrase is a valid
		// expression matching nothing, so this is the safe shape either way.
		return `""`
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"*`)
	}
	return strings.Join(quoted, " AND ")
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
