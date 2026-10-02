// Package sqlite is the SQLite persistence implementation. It owns the
// database connection and the connection-level cipher, and provides the
// durable thought-row and stateful-tool-state stores over it. The full
// datastore backend — the view set the datastore contract defines — lands
// with NAVI-364; the session-CRUD interface this package used to
// implement is deleted (ADR 0011 decision 2).
package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Store owns the SQLite connection. It is not a datastore backend: it is
// the shared connection the durable stores are built over.
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

// ── shared codec helpers ────────────────────────────────────────────────

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: parse time %q: %w", v, err)
	}
	return t, nil
}

func now() string { return formatTime(time.Now()) }
