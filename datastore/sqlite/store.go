// Package sqlite is the SQLite datastore backend: a navi's state in one
// transactional file, with WAL so a flush never blocks a read. ADR 0011 makes
// it the second shipped durable backend — `fs` stays the compiled default
// because state a person can read and diff is the friendlier default, and
// `sqlite` is the single-file option selected with `datastore.backend: sqlite`.
//
// # Layout
//
//	thoughts + thought_transcripts + thought_transcript_meta   thought/transcript aggregate
//	todos + notes                                              stateful tool state (per navi)
//	memories                                                   memory aggregate
//
// # Two structural properties
//
// Metadata without bodies. LoadThoughts/LoadThought select the thoughts table
// alone, and TranscriptInfo answers from thought_transcript_meta — message
// count and latest entry time — so a cold start over thousands of thoughts
// never selects a message body. Only LoadTranscript reads a transcript, and
// the backend reports those reads through conformance.ReadReporter.
//
// One writer, WAL. Mutations arrive through the datastore's ordered writer, so
// the backend needs no locking of its own beyond SQLite's; WAL plus a busy
// timeout keeps a concurrent read from failing or blocking behind a write, and
// lets another process (the `navi enqueue` mailbox, the cron connector) share
// the file.
package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Store owns the SQLite connection and is the datastore backend: it provides
// one view per aggregate (Thoughts/ToolState/Memory) plus the
// conformance.ReadReporter instrumentation. The connection-level cipher it
// carries is applied to transcript bodies (see cipher.go).
type Store struct {
	db     *sql.DB
	cipher Cipher

	// bodyReads counts transcript body loads for the conformance suite.
	bodyReads int64
}

// DB exposes the underlying connection for diagnostics and tooling.
func (s *Store) DB() *sql.DB { return s.db }

// Options configures the store.
type Options struct {
	// Cipher encrypts transcript message bodies and tool input; nil means
	// NoopCipher (plaintext).
	Cipher Cipher
}

// pragmas are applied to every pooled connection through the DSN.
// modernc/sqlite reads `_pragma=name(value)` query parameters and executes
// each on connection open, so the settings are not lost when the pool grows a
// connection. journal_mode=WAL is persistent in the file; the rest are
// per-connection.
var pragmas = []string{
	"_pragma=journal_mode(WAL)",
	"_pragma=busy_timeout(5000)",
	"_pragma=synchronous(NORMAL)",
	"_pragma=foreign_keys(1)",
}

// dsnWithPragmas appends the connection pragmas to a file path. An in-memory
// database is returned untouched: WAL does not apply to it, and a query string
// on `:memory:` would not name a database.
func dsnWithPragmas(path string) string {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(pragmas, "&")
}

// Open opens (creating if needed) the database at path, applies migrations,
// and returns the backend. The caller owns Close.
func Open(path string, opts Options) (*Store, error) {
	db, err := sql.Open("sqlite", dsnWithPragmas(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// WAL serves many readers alongside the datastore's single writer, so the
	// pool is not pinned to one connection; SQLite's own lock plus the busy
	// timeout serialize the rare concurrent writers (another process sharing
	// the file).
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
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

// OpenDB wraps an existing *sql.DB (driver "sqlite") in a Store. The caller
// owns the DB's lifecycle; Close is a no-op. The pragmas are applied to the
// connections this call can reach; a caller supplying the DSN itself gets them
// on every connection.
func OpenDB(db *sql.DB, opts Options) (*Store, error) {
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("sqlite: %s: %w", p, err)
		}
	}
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
	return t.UTC(), nil
}

func now() string { return formatTime(time.Now()) }

// encryptSecret encrypts a transcript value for storage; an empty value stays
// empty rather than NULL. The cipher is self-describing, so a plaintext value
// written before encryption was enabled still reads back.
func (s *Store) encryptSecret(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	ct, err := s.cipher.Encrypt([]byte(v))
	if err != nil {
		return "", fmt.Errorf("sqlite: encrypt: %w", err)
	}
	return string(ct), nil
}

// decryptSecret reverses encryptSecret, passing plaintext through unchanged.
func (s *Store) decryptSecret(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	pt, err := s.cipher.Decrypt([]byte(v))
	if err != nil {
		return "", fmt.Errorf("sqlite: decrypt: %w", err)
	}
	return string(pt), nil
}
