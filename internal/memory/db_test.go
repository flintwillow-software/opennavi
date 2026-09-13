package memory

import (
	"database/sql"
	"testing"

	"github.com/flintwillow-software/opennavi/store/sqlite"
)

// sqlOpen opens the shared SQLite store database with migrations
// applied (delegating to the sqlite package's Open, which owns the
// schema) and returns the raw handle for the memory store.
func sqlOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	st, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB()
}
