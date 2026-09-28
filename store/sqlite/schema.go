// Package sqlite implements the session.Store interface on top of
// SQLite, using
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
	// 1: initial schema.
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
	// FTS5 full-text search across sessions. The Search
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
	// 3: thought queue persistence (#151) — Queue
	// state lives in the same SQLite DB as sessions (per the #151
	// handoff: reuse the store layer, don't invent parallel storage);
	// the spec's queue.yaml/running.yaml files are superseded. Crash
	// recovery is a load-side transition (running → pending).
	`CREATE TABLE IF NOT EXISTS thoughts (
		id TEXT PRIMARY KEY,
		prompt TEXT NOT NULL,
		context TEXT,
		metadata TEXT,
		priority INTEGER NOT NULL DEFAULT 4,
		status TEXT NOT NULL DEFAULT 'pending',
		session_id TEXT,
		slot_id TEXT,
		worker_id TEXT,
		log_path TEXT,
		checkpoint BLOB,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_thoughts_status ON thoughts(status)`,
	`CREATE INDEX IF NOT EXISTS idx_thoughts_priority ON thoughts(priority)`,
	// 4: memory store (#181) — four-scope memory:
	// user/navi are document scopes (one row per
	// subject); memories is the long-term entry archive with FTS5
	// search. The session tier maps to the #149 session system, not
	// this table. Expiry pruning deletes rows where expires_at < now.
	`CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		content TEXT NOT NULL,
		source TEXT,
		tags TEXT,
		importance REAL NOT NULL DEFAULT 0.5,
		expires_at DATETIME,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(scope, subject, id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope, subject)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_expires ON memories(expires_at)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
		content,
		scope UNINDEXED,
		subject UNINDEXED,
		content='memories',
		content_rowid='rowid',
		tokenize='unicode61'
	)`,
	`CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
		INSERT INTO memories_fts(rowid, content, scope, subject)
		VALUES (new.rowid, new.content, new.scope, new.subject);
	END`,
	`CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content, scope, subject)
		VALUES ('delete', old.rowid, old.content, old.scope, old.subject);
	END`,
	`CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content, scope, subject)
		VALUES ('delete', old.rowid, old.content, old.scope, old.subject);
		INSERT INTO memories_fts(rowid, content, scope, subject)
		VALUES (new.rowid, new.content, new.scope, new.subject);
	END`,
	`INSERT INTO memories_fts(memories_fts) VALUES('rebuild')`,
	// 5: memory vector index (#182) — Tier 5 knowledge base. Pure-Go
	// design: embeddings stored as BLOBs in the same DB, brute-force
	// cosine in Go (sqlite-vec/vec0 is a C extension, unshippable under
	// CGO_ENABLED=0). Embeddings come from Ollama /api/embeddings.
	`CREATE TABLE IF NOT EXISTS memory_embeddings (
		memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
		embedding BLOB NOT NULL,
		dims INTEGER NOT NULL,
		created_at DATETIME NOT NULL
	)`,
	// 6: stateful tool storage seam — durable todo and
	// note state for stateful catalog tools (todo_*, note_*), scoped
	// per navi identity (navi_id on every row; all access is
	// navi-filtered at the store layer). State survives restarts:
	// the tables live in the same SQLite DB as sessions/thoughts.
	`CREATE TABLE IF NOT EXISTS todos (
		id TEXT NOT NULL,
		navi_id TEXT NOT NULL,
		text TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		priority INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (navi_id, id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_todos_status ON todos(navi_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_todos_activity ON todos(navi_id, priority, updated_at)`,
	`CREATE TABLE IF NOT EXISTS notes (
		id TEXT NOT NULL,
		navi_id TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		tags TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (navi_id, id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_notes_activity ON notes(navi_id, updated_at)`,
	// 7: defer_until queue mechanism — not-before
	// timestamp on thoughts for durable deferred pickup:
	// deferred thoughts are real queue records that survive restarts.
	`ALTER TABLE thoughts ADD COLUMN defer_until DATETIME`,
	// 8: per-thought provider/model overrides — optional
	// durable provider + model for a thought; empty keeps the runtime
	// primary (and its fallback chain) and runtime model.
	`ALTER TABLE thoughts ADD COLUMN provider TEXT`,
	`ALTER TABLE thoughts ADD COLUMN model TEXT`,
	// 9: thought completion message (NAVI-348) — the outcome a runner
	// reports when a thought reaches complete, so "what did this thought
	// produce?" is answerable from the queue rather than only from its
	// session transcript. Expected to be revisited when the agent runner
	// lands: a real agent produces tool calls and multi-turn state, not
	// one string.
	`ALTER TABLE thoughts ADD COLUMN result TEXT`,
	// 10: per-thought mode (ADR 0007) — the capability profile the runner
	// resolves the tool surface, permission ceiling, and optional model
	// from. Empty selects the navi's default surface.
	`ALTER TABLE thoughts ADD COLUMN mode TEXT`,
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
