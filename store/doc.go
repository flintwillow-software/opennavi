// Package store is the public session-persistence contract
// (S000800 §01): the session data model (Session, SessionEntry), the
// Store interface every persistence backend implements, and the
// in-memory MemoryStore. The SQLite implementation lives in the
// store/sqlite package.
//
// The session data model is the SDK-owned unit of conversation state
// (S000800 §02: copy-on-write session semantics are SDK-owned).
// Session *behavior* — the manager, compaction strategy, and agent
// snapshot sync — stays internal (internal/session).
package store
