// Package datastore is the public persistence contract (ADR 0011): RAM is the
// source of truth at runtime and the datastore is the eventual-consistency
// persistence layer beneath it. See doc.go.
// the session data model (Session, SessionEntry), the
// Store interface every persistence backend implements, and the
// in-memory MemoryStore. The SQLite implementation lives in the
// store/sqlite package.
//
// The session data model is the SDK-owned unit of conversation state
// Copy-on-write session semantics are SDK-owned.
// Session *behavior* — the manager, compaction strategy, and agent
// snapshot sync — stays internal (internal/session).
package datastore
