package sqlite

import "testing"

// TestMigrationsDropIsCompacted pins the NAVI-370 migration: the vestigial
// messages.is_compacted column and its index are gone after Open, while the
// rest of the table survives for the SQLite datastore backend (NAVI-364) to
// build on.
func TestMigrationsDropIsCompacted(t *testing.T) {
	st, err := Open(t.TempDir()+"/schema.db", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	columns, err := tableColumns(st, "messages")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := columns["is_compacted"]; ok {
		t.Fatalf("messages still carries is_compacted: %v", columns)
	}
	if _, ok := columns["content"]; !ok {
		t.Fatalf("the migration dropped more than the flag: %v", columns)
	}

	indexes, err := indexNames(st, "messages")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range indexes {
		if name == "idx_messages_compacted" {
			t.Fatalf("idx_messages_compacted survived: %v", indexes)
		}
	}

	// Re-opening applies no pending migrations and does not error.
	st2, err := Open(t.TempDir()+"/reopen.db", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
}

func tableColumns(st *Store, table string) (map[string]bool, error) {
	rows, err := st.DB().Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultVal, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func indexNames(st *Store, table string) ([]string, error) {
	rows, err := st.DB().Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
