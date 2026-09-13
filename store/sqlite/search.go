package sqlite

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/store"
)

// Search performs full-text search across all sessions via FTS5 (spec
// §04), returning SessionInfo ordered by relevance (best bm25 rank per
// session).
//
// Encrypted message content is stored as ciphertext and therefore not
// searchable: encrypted sessions surface in results only via their
// plaintext metadata or non-encrypted entries. Decrypt-then-filter
// search for encrypted deployments is a documented future option.
func (s *Store) Search(query string, limit int) ([]store.SessionInfo, error) {
	match, err := ftsQuery(query)
	if err != nil {
		return nil, err
	}
	if match == "" {
		return []store.SessionInfo{}, nil
	}
	if limit <= 0 {
		limit = 10
	}

	// bm25() only works in a direct query against the FTS table (not
	// inside CTEs/joins on this SQLite build), so rank first, dedupe to
	// the best hit per session in Go, then fetch session metadata.
	rankRows, err := s.db.Query(`
		SELECT session_id, bm25(messages_fts) AS rank
		FROM messages_fts WHERE messages_fts MATCH ?
		ORDER BY rank`, match)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search %q: %w", query, err)
	}
	defer rankRows.Close()

	type hit struct {
		rank float64
	}
	best := map[string]float64{}
	var order []string
	for rankRows.Next() {
		var sid string
		var rank float64
		if err := rankRows.Scan(&sid, &rank); err != nil {
			return nil, fmt.Errorf("sqlite: scan search hit: %w", err)
		}
		if _, seen := best[sid]; !seen {
			order = append(order, sid)
		}
		if cur, seen := best[sid]; !seen || rank < cur {
			best[sid] = rank
		}
	}
	if err := rankRows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate search hits: %w", err)
	}
	if len(order) == 0 {
		return []store.SessionInfo{}, nil
	}
	if len(order) > limit {
		order = order[:limit]
	}

	out := make([]store.SessionInfo, 0, len(order))
	for _, sid := range order {
		rows, err := s.db.Query(`
			SELECT s.id, s.name, s.agent_id, s.created_at, s.updated_at, s.status,
				(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id),
				(SELECT json_group_array(t.tag) FROM session_tags t WHERE t.session_id = s.id)
			FROM sessions s WHERE s.id = ?`, sid)
		if err != nil {
			return nil, fmt.Errorf("sqlite: search metadata %s: %w", sid, err)
		}
		if rows.Next() {
			info, err := scanSessionInfo(rows)
			rows.Close()
			if err != nil {
				return nil, err
			}
			out = append(out, info)
		} else {
			rows.Close()
		}
	}
	return out, nil
}

// ftsQuery converts a free-text query into an FTS5 MATCH expression:
// whitespace-separated terms ORed together, with quote characters
// stripped (they'd break FTS5 syntax). An empty result means "nothing
// to search".
func ftsQuery(query string) (string, error) {
	// Quotes and apostrophes break FTS5 syntax (and "don't"-style
	// terms would error); strip both before tokenizing.
	stripped := strings.NewReplacer(`"`, "", "'", "").Replace(query)
	terms := strings.Fields(stripped)
	if len(terms) == 0 {
		if strings.TrimSpace(query) != "" {
			return "", fmt.Errorf("sqlite: search query %q has no searchable terms", query)
		}
		return "", nil
	}
	for _, t := range terms {
		if strings.ContainsAny(t, "()*:^") {
			return "", fmt.Errorf("sqlite: search query contains unsupported FTS5 syntax: %q", t)
		}
	}
	return strings.Join(terms, " OR "), nil
}
