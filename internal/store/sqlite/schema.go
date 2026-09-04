// Package sqlite implements the session.Store interface on top of
// SQLite (spec 03-session-context/01-session-management.md §02), using
// the pure-Go modernc.org/sqlite driver (no CGO) and app-level
// AES-256-GCM encryption for sensitive columns.
package sqlite

import (
	"database/sql"
	"fmt"
)

// schema is the initial schema plus migrations, executed on Open.
// Migrations are tracked in schema_migrations so future changes append
// as new numbered statements.
var migrations = []string{
	// 1: initial schema (spec §02).
	`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		name TEXT,
		agent_id TEXT NOT NULL,
		profile TEXT NOT NULL,
		work_dir TEXT,
		branch_id TEXT,
		parent_id TEXT,
		label TEXT,
		status TEXT DEFAULT 'active',
		current INTEGER,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (parent_id) REFERENCES sessions(id)
	)`,
	`CREATE TABLE IF NOT EXISTS messages (
		id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		entry_index INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_call_id TEXT,
		tool_name TEXT,
		tool_input TEXT,
		is_compacted INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (session_id, id),
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	)`,
	`CREATE TABLE IF NOT EXISTS session_tags (
		session_id TEXT NOT NULL,
		tag TEXT NOT NULL,
		PRIMARY KEY (session_id, tag),
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_profile ON sessions(profile)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, entry_index)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_compacted ON messages(is_compacted)`,
	// FTS5 full-text search across sessions (spec §02). The Search
	// method lands with #163; the table and sync triggers exist from
	// the start so messages written now are searchable then.
	`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		session_id,
		content_rowid=id,
		tokenize='unicode61'
	)`,
	`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content, session_id)
		VALUES (new.rowid, new.content, new.session_id);
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		DELETE FROM messages_fts WHERE rowid=old.rowid;
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		DELETE FROM messages_fts WHERE rowid=old.rowid;
		INSERT INTO messages_fts(rowid, content, session_id)
		VALUES (new.rowid, new.content, new.session_id);
	END`,
	// Encrypted rows (values with an enc1: prefix) are stored as
	// arbitrary bytes in TEXT columns; FTS indexes ciphertext for
	// encrypted rows. That is accepted for now: search is #163 and
	// will exclude or handle encrypted content explicitly.
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL
	)`,
	// 2: external-content FTS5 (#163). v1 created a standalone FTS
	// table with content_rowid=id (meaningless without content=) and
	// rowid triggers that inserted the TEXT message id into the rowid
	// column. External content halves the index, keeps it in sync via
	// the content table, and the 'delete' command triggers below are
	// the only correct way to remove rows from an external-content
	// table (DELETE FROM messages_fts silently no-ops and corrupts the
	// index). The rebuild statement backfills existing messages.
	`DROP TRIGGER IF EXISTS messages_ai`,
	`DROP TRIGGER IF EXISTS messages_ad`,
	`DROP TRIGGER IF EXISTS messages_au`,
	`DROP TABLE IF EXISTS messages_fts`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		session_id UNINDEXED,
		content='messages',
		content_rowid='rowid',
		tokenize='unicode61'
	)`,
	`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content, session_id)
		VALUES (new.rowid, new.content, new.session_id);
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content, session_id)
		VALUES ('delete', old.rowid, old.content, old.session_id);
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content, session_id)
		VALUES ('delete', old.rowid, old.content, old.session_id);
		INSERT INTO messages_fts(rowid, content, session_id)
		VALUES (new.rowid, new.content, new.session_id);
	END`,
	`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`,
}

// migrate applies pending migrations inside a transaction. Max schema
// version = len(migrations).
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL
	)`); err != nil {
		return fmt.Errorf("sqlite: create migrations table: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("sqlite: read schema version: %w", err)
	}

	for v := current; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("sqlite: begin migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: apply migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v+1, now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: record migration %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: commit migration %d: %w", v+1, err)
		}
	}
	return nil
}
