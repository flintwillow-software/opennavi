---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - session-context
priority: medium
spec-refs: [030100, 030101, 030102, 030103, 030104, 030105, 030106, 030107, 030108, 030109, 030110, 030111]
---

# Plan: Session Management

**Scope:** Session persistence with SQLite storage, branching, tree navigation, context compaction, and export. Sessions are per-thought for daemon navis, per-session for one-off navis and council mode.

**Inspiration:** pi-mono's session management with JSONL format, extended with SQLite for cross-session search and atomic compaction.

> **See also:** [P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md) for the chat session tables (`chat_sessions`, `chat_messages`) that extend the session management schema. These tables store P2P chat history with message signatures for audit, and are managed by the same SQLite database and session lifecycle patterns defined here.

---

## 00 Overview

Sessions are the primary unit of conversation. Each session contains a sequence of messages, tool calls, and results. Sessions support branching (forking at any point), compaction (summarizing old messages), and export (HTML/JSON).

### 1.1 Session Models

| Model | Where Used | Session Scope |
|-------|-----------|---------------|
| **Per-thought** | Daemon navis (thought queue) | One session per thought |
| **Per-session** | One-off navis, council mode | One session per ChatSession |

**Per-thought sessions** (daemon navis): Each thought has its own session log on disk (`~/.opennavi/profiles/<name>/thoughts/<id>/session.md`). This provides per-thought audit trails and crash recovery.

**Per-session** (one-off navis, council mode): One session shared by all participants. Used for council mode where multiple navis coordinate in a shared context.

### 1.2 Dual Storage: SQLite + JSONL

Sessions use **two complementary storage formats**:

| Format | Purpose | When Used |
|--------|---------|-----------|
| **SQLite** | Primary storage — atomic compaction, FTS5 cross-session search, concurrent access safety, structured queries | All runtime I/O: reads, writes, compaction, search |
| **JSONL** | Human-readable backup — exportable, editable by hand, portable between instances | Export (HTML/JSON), backup/restore, manual inspection, migration |

At startup, sessions are loaded from SQLite. JSONL files can be imported to restore or migrate sessions. When exporting, the system converts SQLite rows back into a single JSON document.

**Conversion:**
- `SQLite → JSON`: All messages in a session become an array of message objects with timestamps, roles, tool call metadata.
- `JSON/JSONL → SQLite`: Parsed and inserted row-by-row within a transaction for atomicity.

Sessions are stored in SQLite for:
- **Atomic compaction** — transactional updates
- **Cross-session search** — FTS5 full-text search across all sessions
- **Concurrent access** — safe for multiple gateway instances
- **Structured queries** — filter by date, agent, tags

### Requirement: Session as Primary Unit of Conversation

The system SHALL represent each conversation as a session containing a sequence of messages, tool calls, and results, with support for branching, compaction, and export.

#### Scenario: Session created for new conversation

- GIVEN a user or external system initiates a conversation
- WHEN the system starts processing
- THEN the system SHALL create a new session with a unique ID
- AND the session SHALL record every message, tool call, and result in sequence

### Requirement: Dual Storage with SQLite Primary and JSONL Backup

The system SHALL use SQLite as the primary runtime storage for sessions and support JSONL import/export for backup, restore, and migration.

#### Scenario: Runtime reads use SQLite

- GIVEN a session is active
- WHEN the system reads or writes session data during runtime
- THEN the system SHALL use the SQLite database for all I/O operations including reads, writes, compaction, and search

#### Scenario: JSONL export produces portable file

- GIVEN a user requests session export
- WHEN the system exports the session
- THEN the system SHALL produce a JSONL file that can be edited by hand and imported into another instance

### Requirement: Session Scoping by Navi Type

The system SHALL use per-thought sessions for daemon navis (one session per thought) and per-session for one-off navis and council mode (one session shared by all participants).

#### Scenario: Daemon navi uses per-thought sessions

- GIVEN a daemon navi processes a thought from its queue
- WHEN the system creates a session for that thought
- THEN the session SHALL be scoped to that specific thought
- AND the session SHALL be stored at `~/.opennavi/profiles/<name>/thoughts/<id>/session.md`

---

## 01 Session Entry

```go
type SessionEntry struct {
    ID        string    // UUID
    ParentID  string    // parent session ID (empty for root)
    Timestamp time.Time
    Message   string    // user message content
    BranchID  string    // branch identifier
    Label     string    // user-assigned label
    Role      Role      // user, assistant, tool
}
```

### Requirement: Session Entry with Unique ID and Metadata

The system SHALL store every session entry with a UUID, timestamp, message content, role (user, assistant, tool), and optional parent ID, branch ID, and label.

#### Scenario: Entry recorded with all fields

- GIVEN a message is added to a session
- WHEN the system creates a session entry
- THEN the entry SHALL contain a UUID, the message content, the role, and a timestamp
- AND the entry SHALL optionally include a parent ID, branch ID, and user-assigned label

### Requirement: Entry Role Classification

The system SHALL classify each session entry by role type: `user`, `assistant`, or `tool`, corresponding to the origin of the message.

#### Scenario: Tool results marked as tool role

- GIVEN a tool execution completes and produces a result
- WHEN the result is recorded in the session
- THEN the entry SHALL have role `tool`
- AND the entry SHALL include the tool name and tool input metadata

---

## 02 SQLite Schema

```sql
-- Sessions table
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    name TEXT,
    agent_id TEXT NOT NULL,
    profile TEXT NOT NULL,
    work_dir TEXT,
    branch_id TEXT,
    parent_id TEXT,
    label TEXT,
    status TEXT DEFAULT 'active',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (parent_id) REFERENCES sessions(id)
);

-- Messages table (one row per message in a session)
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    entry_index INTEGER NOT NULL,
    role TEXT NOT NULL,          -- 'user', 'assistant', 'tool', 'system'
    content TEXT NOT NULL,
    tool_call_id TEXT,           -- for tool results
    tool_name TEXT,              -- for tool calls
    tool_input TEXT,             -- JSON for tool call inputs
    is_compacted INTEGER DEFAULT 0,  -- 1 if this is a compaction summary
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

-- FTS5 virtual table for cross-session search
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    session_id,
    content_rowid=id,
    tokenize='unicode61'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content, session_id)
    VALUES (new.id, new.content, new.session_id);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid=old.id;
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid=old.id;
    INSERT INTO messages_fts(rowid, content, session_id)
    VALUES (new.id, new.content, new.session_id);
END;

-- Session tags for organization
CREATE TABLE IF NOT EXISTS session_tags (
    session_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (session_id, tag),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_sessions_profile ON sessions(profile);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, entry_index);
CREATE INDEX IF NOT EXISTS idx_messages_compacted ON messages(is_compacted);
```

### Requirement: SQLite Schema for Sessions and Messages

The system SHALL use a SQLite database with `sessions` and `messages` tables as defined in the schema, including foreign key relationships, status tracking, and entry ordering.

#### Scenario: Session table with required fields

- GIVEN the system initializes the session database
- WHEN the system creates the `sessions` table
- THEN the table SHALL include columns for `id`, `agent_id`, `profile`, `status`, `created_at`, and `updated_at`
- AND the `sessions` table SHALL support a foreign key from `parent_id` to `id` for session branching

### Requirement: FTS5 Full-Text Search Across Sessions

The system SHALL maintain an FTS5 virtual table on the `messages` table and keep it synchronized via INSERT, DELETE, and UPDATE triggers.

#### Scenario: FTS5 triggers sync on message change

- GIVEN a message is inserted into the `messages` table
- WHEN the `messages_ai` trigger fires
- THEN the FTS5 index SHALL be updated to include the new message content
- AND the index SHALL also be updated on DELETE and UPDATE via the corresponding triggers

### Requirement: Performance Indexes for Common Queries

The system SHALL create indexes on `sessions(profile)`, `sessions(agent_id)`, `sessions(created_at)`, `messages(session_id, entry_index)`, and `messages(is_compacted)`.

#### Scenario: Indexes created on schema initialization

- GIVEN the system creates the SQLite schema
- WHEN the system initializes the database
- THEN the SHALL create all specified indexes
- AND each index SHALL cover the columns listed in the schema definition

### Requirement: Session Tags Organization

The system SHALL support tagging sessions through a `session_tags` table with a composite primary key of `(session_id, tag)`.

#### Scenario: Tag assigned to session

- GIVEN a session exists in the database
- WHEN a user or system assigns a tag to the session
- THEN the system SHALL insert a row into `session_tags` with the `session_id` and `tag`
- AND the same tag SHALL NOT be duplicated on the same session

---

## 03 Session Struct

```go
type Session struct {
    ID        string
    Name      string
    AgentID   string
    Profile   string
    WorkDir   string
    ParentID  string
    BranchID  string
    Label     string
    Status    string     // "active", "archived", "compacted"
    Entries   []SessionEntry
    Current   int        // current entry index (for branching)
    CreatedAt time.Time
    UpdatedAt time.Time
    Tags      []string
}

func (s *Session) AppendEntry(entry SessionEntry)
func (s *Session) GetEntry(id string) (*SessionEntry, error)
func (s *Session) GetPathToRoot() []SessionEntry
func (s *Session) SetCurrent(index int)
func (s *Session) LabelEntry(id, label string)
func (s *Session) Compact(config AgentConfig, model llm.Model) error
func (s *Session) ExportHTML(path string) error
func (s *Session) ExportJSON(path string) error
```

### Requirement: Session Struct with Core Fields

The system SHALL represent a session in memory with a struct containing `ID`, `Name`, `AgentID`, `Profile`, `WorkDir`, `ParentID`, `BranchID`, `Label`, `Status`, `Entries`, `Current` index, `CreatedAt`, `UpdatedAt`, and `Tags`.

#### Scenario: Session struct initialized

- GIVEN a session is loaded from SQLite or created fresh
- WHEN the system constructs the in-memory session struct
- THEN the struct SHALL contain all core fields including `ID`, `AgentID`, `Profile`, `Status`, `Entries`, `Current`, `CreatedAt`, and `UpdatedAt`

### Requirement: Session Operations as Methods

The system SHALL provide methods on the session struct for `AppendEntry`, `GetEntry`, `GetPathToRoot`, `SetCurrent`, `LabelEntry`, `Compact`, `ExportHTML`, and `ExportJSON`.

#### Scenario: AppendEntry adds to sequence

- GIVEN a session struct is in memory
- WHEN `AppendEntry` is called with a new session entry
- THEN the entry SHALL be appended to the session's `Entries` slice
- AND the `UpdatedAt` timestamp SHALL be refreshed

### Requirement: Entry Index Navigation

The system SHALL maintain a `Current` entry index in the session struct that tracks the current position for branching operations.

#### Scenario: SetCurrent changes position

- GIVEN a session has multiple entries
- WHEN `SetCurrent` is called with a valid index
- THEN the session's `Current` field SHALL be updated to that index
- AND subsequent branching operations SHALL fork from that entry

---

## 04 Session Manager

```go
type SessionManager struct {
    db        *sql.DB        // SQLite database
    profile   string
    baseDir   string         // ~/.opennavi/profiles/<name>/sessions/
}

func NewSessionManager(db *sql.DB, profile string) *SessionManager

func (m *SessionManager) CreateSession(agentID, workDir string) (*Session, error)
func (m *SessionManager) LoadSession(id string) (*Session, error)
func (m *SessionManager) ForkSession(id string) (*Session, error)
func (m *SessionManager) DeleteSession(id string) error
func (m *SessionManager) ListSessions(opts ListOptions) ([]SessionInfo, error)
func (m *SessionManager) GetMostRecent(workDir string) (*Session, error)
func (m *SessionManager) Search(query string, limit int) ([]SessionInfo, error)
func (m *SessionManager) FlushAll() error
func (m *SessionManager) LoadSessions(ctx context.Context) error

type ListOptions struct {
    Profile  string
    AgentID  string
    Status   string
    Tags     []string
    Since    time.Time
    Limit    int
    Offset   int
}

type SessionInfo struct {
    ID        string
    Name      string
    AgentID   string
    MessageCount int
    CreatedAt time.Time
    UpdatedAt time.Time
    Status    string
    Tags      []string
}
```

### Requirement: Session Lifecycle Management

The system SHALL provide a `SessionManager` that supports creating, loading, forking, deleting, and listing sessions via SQLite-backed operations.

#### Scenario: CreateSession produces persisted session

- GIVEN a user starts a new conversation
- WHEN `CreateSession` is called with an `agentID` and `workDir`
- THEN the system SHALL insert a new row into the `sessions` table
- AND return a fully initialized `Session` struct with a unique UUID

#### Scenario: ForkSession creates child session

- GIVEN an existing session with entries
- WHEN `ForkSession` is called with the session ID
- THEN the system SHALL create a new session with `parent_id` pointing to the original session
- AND the new session SHALL copy entries up to the `Current` index of the parent

### Requirement: Session Listing with Filters

The system SHALL support listing sessions with optional filters for `Profile`, `AgentID`, `Status`, `Tags`, `Since`, `Limit`, and `Offset`.

#### Scenario: ListSessions with status filter

- GIVEN the session database contains sessions with various statuses
- WHEN `ListSessions` is called with `Status: "active"`
- THEN the system SHALL return only sessions with `status = "active"`
- AND each result SHALL include `ID`, `Name`, `AgentID`, `MessageCount`, `CreatedAt`, `UpdatedAt`, `Status`, and `Tags`

### Requirement: Cross-Session FTS5 Search

The system SHALL support searching across all sessions using FTS5 full-text search via the `Search` method on `SessionManager`.

#### Scenario: Search returns matching sessions

- GIVEN the session database contains messages indexed by FTS5
- WHEN `Search` is called with a query string and limit
- THEN the system SHALL query the `messages_fts` virtual table
- AND return matching `SessionInfo` results sorted by relevance

---

## 05 Branch Navigation and Tree View

```go
type Branch struct {
    ID       string
    Label    string
    Entries  []SessionEntry
    Children []*Branch
}

// /tree UI component
// - Search/filter
// - Fold/unfold branches
// - Arrow navigation
// - Shift+L to label entries
// - Shift+T to toggle timestamps
```

### 6.1 Branching Model

```
Session A (root)
  ├── Entry 1: "Hello"
  ├── Entry 2: "Hi! How can I help?"
  ├── Entry 3: "Write a function"
  ├── Entry 4: [code]
  ├── Entry 5: "Actually, use Go instead"
  │
  └── Branch B (forked at Entry 4)
      ├── Entry 5': "No, use Rust instead"
      └── Entry 6': [rust code]
```

### Requirement: Branching Model with Parent Linkage

The system SHALL support session branching by creating a new session whose `parent_id` references the original session, with entries copied up to the fork point.

#### Scenario: Branch created at specific entry

- GIVEN a session has entries and the user forks at entry index N
- WHEN `ForkSession` is called
- THEN the new session SHALL have `parent_id` set to the original session ID
- AND the new session SHALL contain entries from the original session up to index N
- AND subsequent entries in the new session SHALL be independent of the original

### Requirement: Tree View UI Component

The system SHALL provide a `/tree` UI component that displays the session branch hierarchy with search/filter, fold/unfold, arrow navigation, and keyboard shortcuts for labeling entries and toggling timestamps.

#### Scenario: Tree view renders branches

- GIVEN a session has one or more branches
- WHEN the user opens the `/tree` view
- THEN the system SHALL render a tree structure showing the root session and all child branches
- AND the user SHALL be able to fold/unfold branches, navigate with arrow keys, label entries with Shift+L, and toggle timestamps with Shift+T

---

## 06 Context Compaction

```go
func CompactSession(session *Session, config AgentConfig, model llm.Model) error {
    // 1. Identify messages to compact (older messages beyond threshold)
    // 2. Build compaction prompt with messages to summarize
    // 3. Call LLM for summary
    // 4. Replace compacted messages with summary message (is_compacted=1)
    // 5. Full history preserved in messages table (soft delete via flag)
    // 6. All done in a single SQLite transaction
}
```

### 7.1 Compaction Strategy

- **Trigger:** When message count exceeds `MaxSessionMessages` (default: 5000) or user runs `/compact`
- **Method:** LLM summarizes oldest N messages into a single summary entry
- **Preservation:** Original messages kept in DB with `is_compacted=1` flag
- **Atomicity:** Entire compaction is a single SQLite transaction

### Requirement: Compaction Trigger Threshold

The system SHALL trigger automatic context compaction when the message count in a session exceeds `MaxSessionMessages` (default 5000), and also support manual compaction via the `/compact` command.

#### Scenario: Auto-compaction at message threshold

- GIVEN a session's message count exceeds `MaxSessionMessages`
- WHEN the system processes the next message
- THEN the system SHALL automatically trigger compaction
- AND the compaction SHALL summarize the oldest messages beyond the recent window

### Requirement: Compaction Preserves Original Messages

The system SHALL preserve original compacted messages in the database with the `is_compacted=1` flag instead of deleting them, and SHALL execute the entire compaction within a single SQLite transaction.

#### Scenario: Compacted messages flagged not deleted

- GIVEN compaction is triggered on a session
- WHEN the system replaces old messages with a summary
- THEN the original messages SHALL remain in the `messages` table with `is_compacted` set to `1`
- AND all changes (insertion of summary, flagging of old messages) SHALL occur in a single SQLite transaction

### Requirement: LLM-Based Compaction Summary

The system SHALL call the LLM to generate a summary of the compacted messages, preserving key decisions, important context, constraints, and action items.

#### Scenario: Summary generated by LLM

- GIVEN compaction is triggered on a session
- WHEN the system identifies messages to compact
- THEN the system SHALL build a compaction prompt and call the LLM
- AND the LLM-generated summary SHALL be inserted as a new message with role `system` and `is_compacted=1`

---

## 07 Session Export

### 8.1 HTML Export

Full session export with messages, tool calls, syntax highlighting, and collapsible sections.

### 8.2 JSON Export

```json
{
    "session_id": "abc-123",
    "name": "My Session",
    "agent_id": "default",
    "created_at": "2026-05-29T10:00:00Z",
    "messages": [
        {"role": "user", "content": "Hello", "timestamp": "2026-05-29T10:00:00Z"},
        {"role": "assistant", "content": "Hi!", "timestamp": "2026-05-29T10:00:01Z"},
        {"role": "tool", "tool_name": "bash", "tool_input": {"cmd": "ls"}, "content": "file1\nfile2", "timestamp": "2026-05-29T10:00:02Z"}
    ]
}
```

### Requirement: HTML Export with Full Formatting

The system SHALL export a session to HTML that includes all messages, tool calls, syntax highlighting, and collapsible sections.

#### Scenario: HTML export renders complete session

- GIVEN a session exists with messages and tool calls
- WHEN `ExportHTML` is called on the session
- THEN the system SHALL produce an HTML file containing all messages with their roles, timestamps, and tool call metadata
- AND the HTML SHALL include syntax highlighting for code blocks and collapsible sections for tool outputs

### Requirement: JSON Export with Machine-Readable Format

The system SHALL export a session to JSON with a top-level structure containing `session_id`, `name`, `agent_id`, `created_at`, and a `messages` array of objects with `role`, `content`, `timestamp`, and optional `tool_name` and `tool_input` fields.

#### Scenario: JSON export produces structured output

- GIVEN a session exists with messages
- WHEN `ExportJSON` is called on the session
- THEN the system SHALL produce a JSON file with the session metadata and a `messages` array
- AND each message object SHALL include `role`, `content`, and `timestamp`
- AND tool messages SHALL additionally include `tool_name` and `tool_input`

---

## 08 Session Recovery

Handle corrupted sessions gracefully:

```go
func (m *SessionManager) LoadSession(id string) (*Session, error) {
    // 1. Try loading from SQLite
    session, err := m.loadFromDB(id)
    if err == nil {
        return session, nil
    }

    // 2. If DB error, try WAL backup
    session, err = m.loadFromWALBackup(id)
    if err == nil {
        return session, nil
    }

    // 3. If no backup, return error with recovery suggestions
    return nil, fmt.Errorf("session %s is corrupted: %w", id, err)
}
```

### Requirement: Graceful Session Load Failure with Fallback

The system SHALL attempt to load a session from the primary SQLite database first, fall back to the WAL backup on failure, and return a descriptive error with recovery suggestions if both fail.

#### Scenario: Load from primary succeeds

- GIVEN a session exists in the SQLite database
- WHEN `LoadSession` is called with a valid session ID
- THEN the system SHALL load the session from the primary database
- AND return the fully populated `Session` struct

#### Scenario: Primary fails, WAL backup succeeds

- GIVEN the primary SQLite database has a corrupted session
- WHEN `LoadSession` is called
- THEN the system SHALL attempt to load from the WAL backup
- AND if the backup succeeds, return the session with a warning

#### Scenario: Both primary and backup fail

- GIVEN both the primary database and WAL backup are corrupted
- WHEN `LoadSession` is called
- THEN the system SHALL return an error describing the corruption
- AND the error SHALL include recovery suggestions

### Requirement: WAL Backup Preservation for Recovery

The system SHALL maintain SQLite WAL (Write-Ahead Log) backup files alongside the primary database file to enable recovery from session corruption.

#### Scenario: WAL backup created on write

- GIVEN a session is modified through a write operation
- WHEN the system commits the transaction
- THEN the system SHALL ensure the WAL backup is available for recovery
- AND the backup SHALL be checked before reporting a session as corrupted

---

## 09 File Structu

```
internal/session/
├── session.go       # Session struct and operations
├── manager.go       # SessionManager
├── entry.go         # SessionEntry type
├── branch.go        # Branch struct and tree building
├── compaction.go    # Context compaction
├── export.go        # HTML/JSON export
├── storage.go       # SQLite I/O
├── schema.go        # SQLite schema and migrations
├── search.go        # Cross-session FTS5 search
└── tree_view.go     # /tree UI component
```

### Requirement: Package Structure with Separation of Concerns

The system SHALL organize the session package into separate files for `Session` struct, `SessionManager`, `SessionEntry`, branching, compaction, export, storage, schema, search, and tree view.

#### Scenario: Each concern has its own file

- GIVEN the system is built
- WHEN the `internal/session/` package is compiled
- THEN the package SHALL contain separate source files for session struct, manager, entry, branch, compaction, export, storage, schema, search, and tree view
- AND each file SHALL contain only the types and functions relevant to its concern

### Requirement: Directory Layout Under `internal/session/`

The system SHALL place all session-related source files in the `internal/session/` directory using the specified file names and responsibilities.

#### Scenario: Files placed in correct directory

- GIVEN the session package is implemented
- WHEN the file system is inspected
- THEN the `internal/session/` directory SHALL contain the files `session.go`, `manager.go`, `entry.go`, `branch.go`, `compaction.go`, `export.go`, `storage.go`, `schema.go`, `search.go`, and `tree_view.go`
- AND each file SHALL implement its designated concern as described in the file structure

---

## 10 Implementation Order

1. **SessionEntry** — ID, ParentID, Timestamp, Message, BranchID, Label
2. **SQLite schema** — sessions, messages, FTS5, indexes
3. **SessionStorage** — SQLite read/write, migrations
4. **Session struct** — AppendEntry, GetEntry, GetPathToRoot, etc.
5. **SessionManager** — CreateSession, LoadSession, ForkSession, ListSessions, Search
6. **Branch navigation** — tree building and /tree UI
7. **Context compaction** — identify, summarize, replace (transactional)
8. **HTML export** — session to HTML
9. **JSON export** — session to JSON
10. **Session recovery** — handle corrupted sessions

---

## 11 Key Design Decisions

1. **SQLite over JSONL** — Atomic compaction, cross-session search, concurrent access safety.
2. **FTS5 for search** — Full-text search across all sessions without external search engine.
3. **Soft compaction** — Original messages preserved with `is_compacted` flag; never truly deleted.
4. **Transaction safety** — All session mutations are wrapped in SQLite transactions.
5. **Profile-scoped** — Each profile has its own session database.
6. **Branch model** — Fork at any message; branches are separate sessions with `parent_id` linkage.
7. **Export formats** — Both HTML (human-readable) and JSON (machine-readable) export.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S030100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |