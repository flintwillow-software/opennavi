package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Put stores (or replaces) a document for a subject in a user/navi
// scope — whole-document semantics per spec §02: the caller owns
// consolidation; the store never auto-compacts. For the memories scope
// use AddEntry instead.
func (s *Store) Put(e Entry) error {
	if !e.Scope.Valid() || e.Scope == ScopeMemories {
		return fmt.Errorf("%w: Put is for user/navi scopes only (use AddEntry for memories)", ErrInvalidScope)
	}
	if e.Subject == "" {
		return fmt.Errorf("memory: subject is required")
	}
	now := time.Now().UTC()
	if e.ID == "" {
		// Deterministic document ID: one document per (scope, subject).
		e.ID = string(e.Scope) + "/" + e.Subject
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	tags, _ := json.Marshal(e.Tags)

	_, err := s.db.Exec(`INSERT INTO memories
		(id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, subject, id) DO UPDATE SET
			content=excluded.content, source=excluded.source, tags=excluded.tags,
			importance=excluded.importance, expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
		e.ID, string(e.Scope), e.Subject, e.Content, e.Source, string(tags),
		e.Importance, nullableTime(e.ExpiresAt), formatTime(e.CreatedAt), formatTime(e.UpdatedAt))
	if err != nil {
		return fmt.Errorf("memory: put %s/%s: %w", e.Scope, e.Subject, err)
	}
	return nil
}

// AddEntry stores a new memories-scope archive entry with metadata
// (spec §04: date, source, tags, importance, expiration).
func (s *Store) AddEntry(e Entry) (Entry, error) {
	if e.Scope != ScopeMemories {
		return Entry{}, fmt.Errorf("%w: AddEntry is for the memories scope only (use Put for user/navi)", ErrInvalidScope)
	}
	if strings.TrimSpace(e.Content) == "" {
		return Entry{}, fmt.Errorf("memory: entry content is required")
	}
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.Importance <= 0 {
		e.Importance = 0.5
	}
	tags, _ := json.Marshal(e.Tags)
	// Archive entries get a timestamp-unique ID when unset (direct
	// inserts via SaveThought-style paths share the PK).
	if e.ID == "" {
		e.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	_, err := s.db.Exec(`INSERT INTO memories
		(id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, string(ScopeMemories), e.Subject, e.Content, e.Source, string(tags),
		e.Importance, nullableTime(e.ExpiresAt), formatTime(e.CreatedAt), formatTime(e.UpdatedAt))
	if err != nil {
		return Entry{}, fmt.Errorf("memory: add entry: %w", err)
	}
	return e, nil
}

// Get returns a copy of the document (user/navi by subject) or entry
// (memories by ID).
func (s *Store) Get(scope Scope, key string) (Entry, error) {
	if !scope.Valid() {
		return Entry{}, fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
	var (
		e                Entry
		tags             sql.NullString
		expires          sql.NullString
		created, updated sql.NullString
	)
	row := s.db.QueryRow(`SELECT id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at
		FROM memories WHERE scope = ? AND (subject = ? OR id = ?) LIMIT 1`,
		string(scope), key, key)
	if err := scanEntry(row, &e, &tags, &expires, &created, &updated); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// ListSubjects returns the subjects that have documents in a user/navi
// scope.
func (s *Store) ListSubjects(scope Scope) ([]string, error) {
	if scope != ScopeUser && scope != ScopeNavi {
		return nil, fmt.Errorf("%w: ListSubjects is for user/navi scopes", ErrInvalidScope)
	}
	rows, err := s.db.Query(`SELECT DISTINCT subject FROM memories WHERE scope = ? ORDER BY subject`, string(scope))
	if err != nil {
		return nil, fmt.Errorf("memory: list subjects: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var subj string
		if err := rows.Scan(&subj); err != nil {
			return nil, fmt.Errorf("memory: scan subject: %w", err)
		}
		out = append(out, subj)
	}
	return out, rows.Err()
}

// ListEntries returns memories-scope entries, newest first.
func (s *Store) ListEntries(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, scope, subject, content, source, tags, importance, expires_at, created_at, updated_at
		FROM memories WHERE scope = ? ORDER BY updated_at DESC LIMIT ?`, string(ScopeMemories), limit)
	if err != nil {
		return nil, fmt.Errorf("memory: list entries: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var (
			e                Entry
			tags             sql.NullString
			expires          sql.NullString
			created, updated sql.NullString
		)
		if err := scanEntry(rows, &e, &tags, &expires, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Search runs an FTS5 full-text query over the memories scope (spec:
// on-demand retrieval — only the memories tier is searchable; user/navi
// documents are always-in-context). Terms are ORed; FTS5 operator
// syntax is rejected.
func (s *Store) Search(query string, limit int) ([]Entry, error) {
	match, err := ftsQuery(query)
	if err != nil {
		return nil, err
	}
	if match == "" {
		return []Entry{}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT m.id, m.scope, m.subject, m.content, m.source, m.tags, m.importance, m.expires_at, m.created_at, m.updated_at
		FROM memories_fts f
		JOIN memories m ON m.rowid = f.rowid
		WHERE memories_fts MATCH ? AND m.scope = ?
		ORDER BY bm25(memories_fts)
		LIMIT ?`, match, string(ScopeMemories), limit)
	if err != nil {
		return nil, fmt.Errorf("memory: search %q: %w", query, err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var (
			e                Entry
			tags             sql.NullString
			expires          sql.NullString
			created, updated sql.NullString
		)
		if err := scanEntry(rows, &e, &tags, &expires, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete removes a memory by subject (user/navi) or ID (memories).
func (s *Store) Delete(scope Scope, key string) error {
	if !scope.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
	res, err := s.db.Exec(`DELETE FROM memories WHERE scope = ? AND (subject = ? OR id = ?)`,
		string(scope), key, key)
	if err != nil {
		return fmt.Errorf("memory: delete %s/%s: %w", scope, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("memory: delete %s/%s: rows: %w", scope, key, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Prune deletes expired memories (expires_at < now) and returns the
// number removed (compaction/pruning per the issue scope).
func (s *Store) Prune() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?`,
		formatTime(time.Now().UTC()))
	if err != nil {
		return 0, fmt.Errorf("memory: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("memory: prune: rows: %w", err)
	}
	return n, nil
}

// ── helpers ──────────────────────────────────────────────────────────────

// ftsQuery converts a free-text query into an FTS5 MATCH expression:
// whitespace-separated terms ORed together, quote and apostrophe
// characters stripped, FTS5 operator tokens rejected.
func ftsQuery(query string) (string, error) {
	stripped := strings.NewReplacer(`"`, "", "'", "").Replace(query)
	terms := strings.Fields(stripped)
	if len(terms) == 0 {
		if strings.TrimSpace(query) != "" {
			return "", fmt.Errorf("memory: search query %q has no searchable terms", query)
		}
		return "", nil
	}
	for _, t := range terms {
		if strings.ContainsAny(t, "()*:^-") {
			return "", fmt.Errorf("memory: search query contains unsupported FTS5 syntax: %q", t)
		}
	}
	return strings.Join(terms, " OR "), nil
}

// rowScanner abstracts *sql.Rows and *sql.Row.
type rowScanner interface{ Scan(dest ...any) error }

func scanEntry(row rowScanner, e *Entry, tags, expires, created, updated *sql.NullString) error {
	var scope string
	var importance float64
	if err := row.Scan(&e.ID, &scope, &e.Subject, &e.Content, &e.Source, tags,
		&importance, expires, created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("memory: scan entry: %w", err)
	}
	e.Scope = Scope(scope)
	e.Importance = importance
	if tags.Valid && tags.String != "" {
		_ = json.Unmarshal([]byte(tags.String), &e.Tags)
	}
	if expires.Valid && expires.String != "" {
		if ts, perr := time.Parse(time.RFC3339Nano, expires.String); perr == nil {
			e.ExpiresAt = &ts
		}
	}
	var perr error
	if e.CreatedAt, perr = time.Parse(time.RFC3339Nano, created.String); perr != nil {
		return fmt.Errorf("memory: parse created_at: %w", perr)
	}
	if e.UpdatedAt, perr = time.Parse(time.RFC3339Nano, updated.String); perr != nil {
		return fmt.Errorf("memory: parse updated_at: %w", perr)
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
