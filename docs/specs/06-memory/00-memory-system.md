---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - memory
priority: medium
spec-refs: [060000, 060001, 060002, 060003, 060004, 060005, 060006, 060007, 060008, 060009, 060010]
---

# Memory System

**Scope:** Four-scope memory architecture (session, user, navi, memories) with strict scoping and on-demand retrieval.

---

## 00 Memory Architecture Overview

```
~/.opennavi/profiles/<name>/
├── memory/
│   ├── session/            # Per-interaction state (ephemeral)
│   │   └── <session_id>.md # Current interaction context
│   ├── user/               # Human users the navi has worked with
│   │   └── <user_id>.md    # Preferences, style, lessons learned about that user
│   ├── navi/               # Other navis this navi has interacted with
│   │   └── <navi_name>.md  # How to communicate with, delegate to, or collaborate with that navi
│   └── memories/           # Long-term archive (indexed, searchable)
│       └── YYYY-MM-DD.md   # Date-organized knowledge base entries
```

### Design Philosophy

Memory is scoped by *who* it belongs to and *when* it's needed. Four distinct scopes: session (the navi's working state), user (what the navi knows about a human), navi (what one agent has learned about another), memories (long-term searchable archive).

| Tier | Scope | Lifespan | When Loaded | Purpose |
|------|-------|----------|-------------|---------|
| **Session** | `<name>/memory/session/` | Navi-specific | One interaction/wakeup | On resume or explicit recall | Working context for current task |
| **User** | `<name>/memory/user/<id>.md` | This navi writes about that user | Persistent per-user | Always injected into system prompt | Relationship notes, preferences |
| **Navi** | `<name>/memory/navi/<other>.md` | This navi writes about that other navi | Long-term with pruning | When interacting with that navi | How to communicate, delegate, or collaborate |
| **Memories** | `<name>/memory/memories/` | This navi's archive | Long-term with pruning | Only when needed (searched on-demand) | Knowledge base of facts and learned information |

### Requirement: Four-Scope Memory Architecture

The system SHALL maintain four distinct memory scopes — session (the navi's working state), user (what the navi knows about a human), navi (what one agent has learned about another), and memories (long-term searchable archive) — with each scope scoped by who it belongs to and when it's needed.

#### Scenario: Distinct memory scopes

GIVEN a navi profile with memory storage available
WHEN the system stores information
THEN the system SHALL route it to the session, user, navi, or memories scope based on ownership and purpose.

### Requirement: Scope-Based Memory Layout

The system SHALL organize memory under `~/.opennavi/profiles/<name>/memory/` with `session/`, `user/`, `navi/`, and `memories/` directories, storing per-scope entries as markdown files at their documented locations.

#### Scenario: Memory directory initialization

GIVEN a navi profile is initialized
WHEN the memory store is created
THEN the system SHALL create the `session/`, `user/`, `navi/`, and `memories/` directories under `memory/`.

### Requirement: Load Timing Per Memory Tier

The system SHALL load user memory always (injected into the system prompt), navi memory when interacting with that navi, session memory on resume or explicit recall, and memories only when needed via on-demand search.

#### Scenario: Always-in-context user memory

GIVEN a session starts for a known user
WHEN the system builds the initial context
THEN the system SHALL inject user memory into the system prompt without an explicit tool call.

#### Scenario: On-demand memories retrieval

GIVEN the navi needs information not present in current context
WHEN the navi searches the memories archive
THEN the system SHALL load matching entries only at that point.

---

## 01 Session Memory (Per-Interaction State)

### Purpose

Working context for a single interaction or wakeup cycle. Holds the current task state, conversation flow, and temporary working data that doesn't belong to any user or long-term archive.

### Scope

Session memory is **navi-specific**. It's scoped to the navi instance and the specific interaction/wakeup event. When the session ends (user closes chat, integration disconnects, cron job finishes), this context can be archived or discarded based on configuration.

### Structure

```markdown
# Session: <session_id>
# Started: 2026-07-31T14:30:00Z
# Ended: 2026-07-31T15:45:00Z (if completed)

## State
- Current task: <description>
- Step count: N/M

## Context
- Relevant files being modified: [file paths]
- Open questions or blockers

## Notes
- Temporary working notes for this interaction only
```

### Character Limit

No hard character limit — session memory is loaded into context when the session resumes. The agent manages what fits within its token budget during active conversation. When a session exceeds reasonable length, it should be compacted (summarized) rather than truncated.

### Requirement: Session Memory for Per-Interaction State

The system SHALL maintain session memory as navi-specific per-interaction state that holds the current task, conversation flow, and temporary working data that doesn't belong to any user or long-term archive, scoped to the navi instance and the specific interaction or wakeup event.

#### Scenario: Working context tracking

GIVEN an active interaction or wakeup cycle
WHEN the navi records its working state
THEN the system SHALL store the current task, step count, relevant file paths, open questions, and temporary notes in the session file.

#### Scenario: Session end handling

GIVEN a session ends (user closes chat, integration disconnects, or cron job finishes)
WHEN the session lifecycle completes
THEN the system SHALL archive or discard the session context based on configuration.

### Requirement: Session Memory Compaction

The system SHALL compact (summarize) session memory when it exceeds reasonable length rather than truncating it, with no hard character limit.

#### Scenario: Overlong session compaction

GIVEN a session that has exceeded reasonable length
WHEN the conversation continues
THEN the system SHALL compact the session context by summarization rather than truncation.

---

## 02 User Memory (Human User-Scoped Notes)

### Purpose

Notes that a navi has learned about a specific user — preferences, communication style, pet peeves, workflow habits. This memory is **user-scoped**, meaning it belongs to the user and can potentially be shared across multiple navis if they're designed for cross-navi awareness.

### Structure

```markdown
# User: <user_id>

## Preferences
- Prefers concise responses
- Likes code examples before explanations
- Uses TypeScript, not JavaScript
- Prefers functional over OOP patterns

## Communication Style
- Direct, no fluff
- Enjoys humor in technical discussions
- Dislikes verbose explanations

## Workflow
- Work repo: ~/projects/acme-api
- Personal repo: ~/projects/hobby-blog
- Favorite editor: Neovim
- Shell: zsh

## Lessons Learned
- Don't suggest Docker for local dev (uses podman)
- Prefers PRs over direct commits
- Always run tests before suggesting changes
```

### Key Properties

1. **User-owned**: The user can read, edit, or delete their own memory files directly. They're plain markdown — human-readable and editable without tooling.

2. **Navi-written**: Navis write entries based on what they learn during interactions. A single user can have memory files from multiple navis if cross-navi sharing is enabled.

3. **Always-in-context**: User memory is injected into the system prompt at session start so the navi always has access to it without explicit tool calls.

4. **No auto-compaction**: Like Hermes Agent, when adding would exceed reasonable limits, the agent self-manages by consolidating or replacing entries rather than silently dropping content.

### Requirement: User Memory for Human User Notes

The system SHALL maintain user-scoped notes about a specific user — preferences, communication style, pet peeves, and workflow habits — stored as plain markdown in `user/<user_id>.md`, potentially shared across multiple navis if cross-navi awareness is enabled.

#### Scenario: User note recording

GIVEN a navi has learned something about a user
WHEN the navi records the observation
THEN the system SHALL write or update the entry in `user/<user_id>.md`.

### Requirement: User Memory Ownership

The system SHALL allow users to read, edit, and delete their own memory files directly as plain markdown, without requiring any tooling.

#### Scenario: Direct user editing

GIVEN a user memory file `user/<user_id>.md`
WHEN the user opens it in an editor
THEN the user SHALL be able to read, edit, and delete entries without any tooling.

### Requirement: Always-in-Context User Memory

The system SHALL inject user memory into the system prompt at session start so the navi always has access to it without explicit tool calls.

#### Scenario: Session start injection

GIVEN a session begins for a known user
WHEN the system builds the initial system prompt
THEN the system SHALL include that user's memory file content in the prompt.

### Requirement: User Memory Self-Management

The system SHALL self-manage user memory growth by consolidating or replacing entries when adding would exceed reasonable limits, rather than silently dropping content or auto-compacting.

#### Scenario: User memory at capacity

GIVEN user memory that has grown beyond reasonable limits
WHEN the navi needs to add a new entry
THEN the system SHALL consolidate or replace overlapping entries without silently dropping content.

---

## 03 Navi Memory (Other Navis)

### Purpose

Notes that a navi has learned about *other navis* it interacts with — how to communicate with them, delegate tasks, or collaborate effectively. This is the inter-navi relationship layer.

### Structure

```markdown
# Navi: project-manager
# Type: github-project-manager

## Communication Protocol
- Prefers structured task descriptions (title + description + acceptance criteria)
- Responds well to issue IDs when referencing work items
- Handles delegation via `/delegate` command format

## Collaboration History
- Worked together on issues #142, #147, #153
- Successful pattern: send issue ID → wait for status update → proceed with implementation

## Preferences
- Likes concise status updates over verbose explanations
- Prefers parallel workstreams when possible (don't block waiting)
- Dislikes being interrupted mid-review cycle

## Lessons Learned
- Don't ask it to modify its own project board — use issue transitions only
- It takes ~30s between delegation and response; don't retry immediately
```

### Key Properties

1. **Navi-specific**: Written by one navi about another — not shared or editable by humans directly (though users can review).
2. **Auto-discovered**: When a navi first interacts with another, it creates an initial entry based on the interaction contract/skill definition.
3. **Self-improving**: Navis update these notes as they learn better ways to collaborate — similar to how Hermes Agent self-improves skills.
4. **Searchable**: The navi can search its `navi/` directory to find which navis it knows about and how best to work with them for a given task.

### Requirement: Navi Memory for Inter-Navi Relationships

The system SHALL maintain navi-scoped notes about other navis — communication protocols, collaboration history, preferences, and lessons learned — stored in `navi/<navi_name>.md`, forming the inter-navi relationship layer.

#### Scenario: Navi relationship notes

GIVEN a navi that has interacted with another navi
WHEN the navi records what it learned
THEN the system SHALL store the notes in `navi/<navi_name>.md`.

### Requirement: Navi Memory Auto-Discovery

The system SHALL auto-create an initial `navi/<name>.md` entry when a navi first interacts with another navi, based on the interaction contract or skill definition.

#### Scenario: First interaction discovery

GIVEN a navi that has not yet interacted with another specific navi
WHEN the navi first interacts with that navi
THEN the system SHALL create an initial entry in the `navi/` directory from the interaction contract or skill definition.

### Requirement: Navi Memory Search

The system SHALL allow the navi to search its `navi/` directory to identify which navis it knows about and how best to work with them for a given task.

#### Scenario: Navi lookup for a task

GIVEN a task requiring collaboration with a known navi
WHEN the navi searches its `navi/` directory
THEN the system SHALL return the collaboration notes for that navi.

---

## 04 Memories (Long-Term Archive)

### Purpose

Indexed, searchable long-term storage for facts, patterns, and learned information that doesn't belong to a specific user but is useful for future interactions. Acts as the navi's knowledge base — only loaded when needed via search or explicit recall.

### Structure

```markdown
---
date: 2026-07-31T14:30:00Z
source: session_<id> | user_memory | explicit_add | auto_store
tags: [go, testing, ci]
importance: 0.85
expires: null # nil = permanent
---

# Project uses Go 1.22 with sqlc for database queries and chi router for HTTP.
Run tests with `make test`. CI via GitHub Actions. The staging server needs SSH port 2222, not 22.

## Key Facts
- Database: PostgreSQL 16
- ORM: sqlc (not gorm)
- Router: chi v5
- Test framework: testify
```

### Search & Retrieval

Memories are searchable via FTS5 or vector embeddings when needed. The agent searches this archive only when it can't find the answer in current context, user memory, or session state. This keeps the always-in-context data minimal and focused.

### Requirement: Memories as Long-Term Archive

The system SHALL maintain an indexed, searchable long-term archive of facts, patterns, and learned information that doesn't belong to a specific user, stored in date-organized `memories/YYYY-MM-DD.md` files.

#### Scenario: Archive entry storage

GIVEN a fact or pattern not tied to a specific user
WHEN the navi stores it for future use
THEN the system SHALL write it to a date-organized markdown file under `memories/`.

### Requirement: Memory Entry Metadata

The system SHALL store each memory entry with metadata including date, source, tags, importance, and expiration.

#### Scenario: Entry with metadata

GIVEN a memory entry is stored
WHEN the system writes the file
THEN the entry SHALL include date, source, tags, importance, and expiration frontmatter.

### Requirement: On-Demand Memory Retrieval

The system SHALL search the memories archive via FTS5 or vector embeddings only when needed — when the answer can't be found in current context, user memory, or session state.

#### Scenario: On-demand archive search

GIVEN the navi cannot find an answer in current context, user memory, or session state
WHEN the navi searches the archive
THEN the system SHALL retrieve matching entries via FTS5 or vector embeddings.

---

## 05 Knowledge Base (Vector-Based — Tier 5)

> **Status:** Spec'd but not yet implemented. See [GAPS.md](../../gaps/GAPS.md) Gap 5 for the RAG landscape analysis.

### Purpose

A vector-indexed knowledge base that provides semantic search across resolved tickets, documentation, runbooks, FAQs, and learned patterns. Unlike the text-based memories tier (FTS5), the knowledge base uses embeddings for semantic similarity search — finding conceptually related content even when keywords don't match.

### Use Cases

- **Customer support:** "Find similar resolved tickets" — search by issue description, not just keywords
- **Code review:** "Have we seen this pattern before?" — semantic search across past reviews
- **Runbook lookup:** "What's the procedure for this error?" — find the right runbook by similarity
- **Self-learning:** "What did I learn about this API last time?" — retrieve past solutions

### Architecture

The knowledge base sits alongside the existing four memory tiers. It's not a replacement — it's a specialized search index for the long-term memories tier.

```
~/.opennavi/profiles/<name>/memory/
├── session/          # Tier 1: Per-interaction state
├── user/             # Tier 2: Human user notes
├── navi/             # Tier 3: Inter-navi notes
├── memories/         # Tier 4: Long-term archive (FTS5)
└── knowledge/        # Tier 5: Vector index (embeddings)
    ├── kb.sqlite     # sqlite-vec virtual table
    └── sources/      # Original source documents
```

### Storage Backend

**Primary recommendation: [sqlite-vec](https://github.com/asg017/sqlite-vec)** — a pure C SQLite extension with no dependencies. Uses the same SQLite engine navis already use for session storage. Vector search via `vec0` virtual tables.

```sql
-- Create a vector index
CREATE VIRTUAL TABLE kb_embeddings USING vec0(
    embedding float[768]
);

-- Query: find top-5 similar entries
SELECT rowid, distance
FROM kb_embeddings
WHERE embedding MATCH ?
  AND k = 5;
```

**Alternative backends** (for larger deployments):
- **Qdrant** — standalone vector database, good for shared team knowledge bases
- **pgvector** — PostgreSQL extension, good if the team already uses Postgres
- **chromem-go** — embedded Go vector DB, no external server

### Population

Knowledge base entries are created automatically:

| Source | Trigger | Example |
|--------|---------|--------|
| Resolved tickets | Agent closes a support ticket | "User reported password reset not arriving. Root cause: SendGrid degraded. Resolution: add failover to SES." |
| Completed runbooks | Agent follows and completes a runbook | "P0 incident: database connection pool exhausted. Fix: restore pool config." |
| FAQ extraction | Agent identifies a recurring question | "How do I reset my password? → Check email, wait 15min, try security questions." |
| Explicit add | Agent or user adds a knowledge entry | "The staging server's SSH port is 2222, not 22." |

### Retrieval Strategy

The agent queries the knowledge base when:
1. It can't find the answer in current context, user memory, or session state
2. It encounters a new issue that might match a resolved one (semantic similarity)
3. Explicitly asked to search ("What did we do about X last time?")

Results are injected into context as reference material, not as authoritative facts — the agent evaluates relevance before using.

### Key Design Decisions

1. **Embedded by default** — sqlite-vec means no external service, no network dependency, no infra to manage
2. **Semantic, not keyword** — embeddings capture meaning, not just text matching
3. **Auto-populated, not manual** — knowledge base grows naturally from resolved work
4. **Complementary to FTS5** — the memories tier (FTS5) handles exact-match search; knowledge base handles semantic search
5. **Opt-in for agents** — not all agents need vector search; it's a compile-time option

### Gaps / Future Work

- [ ] Vector embedding generation (requires an embedding model — local or API)
- [ ] Automatic re-indexing when source documents are updated
- [ ] Cross-navi knowledge base sharing (team-level shared KB)
- [ ] Confidence scoring for retrieved results
- [ ] Expiry and staleness detection for KB entries

### Requirement: Vector-Based Knowledge Base

The system SHALL provide a vector-indexed knowledge base (Tier 5) that supports semantic similarity search across resolved tickets, documentation, runbooks, FAQs, and learned patterns, using embeddings rather than keyword matching.

#### Scenario: Semantic search

GIVEN a query with no exact keyword match to stored knowledge
WHEN the navi searches the knowledge base
THEN the system SHALL return conceptually similar entries via embeddings.

### Requirement: Knowledge Base Storage Backend

The system SHALL store the knowledge base under `memory/knowledge/` with a sqlite-vec backend by default (`kb.sqlite` vector index plus `sources/`), supporting Qdrant, pgvector, and chromem-go as alternative backends for larger deployments.

#### Scenario: Embedded default storage

GIVEN a navi with the knowledge base enabled
WHEN the knowledge base is initialized
THEN the system SHALL create `memory/knowledge/` with a sqlite-vec `kb_embeddings` virtual table and a `sources/` directory, without requiring an external service.

### Requirement: Automatic Knowledge Base Population

The system SHALL automatically create knowledge base entries from resolved tickets, completed runbooks, FAQ extraction, and explicit additions by the agent or user.

#### Scenario: Entry from resolved ticket

GIVEN an agent closes a support ticket
WHEN the resolution is recorded
THEN the system SHALL add the resolution as a knowledge base entry.

#### Scenario: Explicit knowledge add

GIVEN a user or agent explicitly adds knowledge
WHEN the entry is submitted
THEN the system SHALL store it in the knowledge base.

### Requirement: Knowledge Base Retrieval Strategy

The system SHALL query the knowledge base only when the answer can't be found in current context, user memory, or session state, when a new issue might match a resolved one, or when explicitly asked, and SHALL inject results into context as reference material rather than authoritative facts.

#### Scenario: Reference-only injection

GIVEN the navi queries the knowledge base
WHEN results are retrieved
THEN the system SHALL inject them into context as reference material for the agent to evaluate relevance before use.

---

## 06 Memory Integration with Agent

### Session Start Flow

When a session begins (CLI launch, gateway message, cron wakeup):

1. **Load User Memory** — Read all `user/<user_id>.md` files for resolved users → inject into system prompt
2. **Load/Resume Session** — If resuming an existing session, load its state from `session/<id>.md`; otherwise create fresh session context
3. **Build Context** — System prompt + user memory + session state (if resumed) form the initial context

### During Interaction

- Agent can append to current session's working notes as it progresses through tasks
- When learning something about a user, agent writes/updates `user/<user_id>.md`
- When encountering reusable knowledge, agent stores in `memories/YYYY-MM-DD.md` with tags and metadata

### Session End Flow

When a session completes or ends:

1. **Archive Session** — Move current session to archive if still relevant for future reference
2. **Flush to Memories (Optional)** — If the session produced reusable knowledge, store key findings in memories with appropriate tagging
3. **Compact User Memory (If Needed)** — If user memory has grown beyond reasonable limits, consolidate overlapping entries

### Requirement: Session Start Memory Loading

The system SHALL load user memory files for resolved users and inject them into the system prompt, load or resume the session state, and build the initial context from the system prompt plus user memory plus session state at session start.

#### Scenario: Fresh session start

GIVEN a session begins (CLI launch, gateway message, or cron wakeup)
WHEN the system initializes context
THEN the system SHALL read `user/<user_id>.md` files, create fresh session context, and form the initial context from system prompt plus user memory.

#### Scenario: Session resume

GIVEN a resuming existing session
WHEN the system initializes context
THEN the system SHALL load the session state from `session/<id>.md` and include it in the initial context.

### Requirement: During-Interaction Memory Operations

The system SHALL append to the current session's working notes as the navi progresses, write or update `user/<user_id>.md` when learning something about a user, and store reusable knowledge in `memories/YYYY-MM-DD.md` with tags and metadata.

#### Scenario: Reusable knowledge storage

GIVEN the navi encounters reusable knowledge during an interaction
WHEN the knowledge is identified as worth keeping
THEN the system SHALL store it in `memories/YYYY-MM-DD.md` with tags and metadata.

### Requirement: Session End Memory Operations

The system SHALL archive the session when it completes if still relevant for future reference, flush key findings to memories with appropriate tagging when the session produced reusable knowledge, and compact user memory when it has grown beyond reasonable limits.

#### Scenario: Session completion

GIVEN a session completes or ends
WHEN the system finalizes the session
THEN the system SHALL archive the session if relevant, flush reusable knowledge to memories with appropriate tagging, and consolidate overlapping user memory entries if needed.

---

## 07 Navi Memory Is Not Shared

Navi memory (`navi/<name>.md`) is **private to the writing agent** — each navi maintains its own inter-navi notes about other navis it has encountered. This keeps inter-agent communication patterns private and avoids one agent's assumptions leaking into another's context.

### Requirement: Navi Memory Privacy

The system SHALL keep navi memory (`navi/<name>.md`) private to the writing agent, so each navi maintains its own inter-navi notes about other navis it has encountered.

#### Scenario: Private inter-navi notes

GIVEN a navi has written notes about another navi
WHEN another agent requests access to memory
THEN the system SHALL restrict access to the writing agent only.

### Requirement: No Cross-Agent Context Leakage

The system SHALL prevent one agent's inter-navi notes from leaking into another agent's context, keeping inter-agent communication patterns private.

#### Scenario: No cross-agent context leakage

GIVEN an agent that is not the writer of a navi's `navi/` notes
WHEN the agent's context is built
THEN the system SHALL NOT include the other agent's navi memory in the context.

---

## 08 Cross-Navi Memory Sharing (User Memory)

### Configuration

```yaml
memory:
  sharing_enabled: false  # Default off for privacy
  shared_user_scope: true # Allow multiple navis to read/write same user files
```

When enabled:
- Multiple navis can write notes about the same user in `user/<user_id>.md`
- Each navi's contributions are prefixed or sectioned by navi identity
- User retains full control — can merge, edit, or delete any entry regardless of source

### Example Shared User File

```markdown
# User: alice

## Hermes (coding navi)
- Prefers concise code reviews
- Uses Go for backend projects
- Dislikes verbose explanations

## Onyx (home automation)
- Wakes up at 7am on weekdays
- Prefers gradual lighting changes in morning
- Has a cat named Mochi

## Project Manager Navi
- Responds best to bullet points
- Needs task deadlines clearly stated
```

### Requirement: Cross-Navi Sharing Configuration

The system SHALL default cross-navi memory sharing to disabled (`sharing_enabled: false`), and when enabled with `shared_user_scope: true`, SHALL allow multiple navis to read and write notes about the same user in `user/<user_id>.md`, with each navi's contributions prefixed or sectioned by navi identity.

#### Scenario: Sharing disabled by default

GIVEN a default memory configuration
WHEN cross-navi sharing is not explicitly enabled
THEN the system SHALL keep `sharing_enabled: false` and scope user files to a single navi.

#### Scenario: Sharing enabled

GIVEN `memory.sharing_enabled: true` and `shared_user_scope: true`
WHEN multiple navis write notes about the same user
THEN the system SHALL allow concurrent reads and writes to `user/<user_id>.md` and prefix or section each navi's contributions by navi identity.

### Requirement: User Control Over Shared Entries

The system SHALL retain full user control over shared user files — the user can merge, edit, or delete any entry regardless of source.

#### Scenario: User edits shared file

GIVEN a shared user file with contributions from multiple navis
WHEN the user modifies the file
THEN the system SHALL honor the user's merge, edit, or deletion regardless of entry source.

---

## 09 Right to Be Forgotten

### Purpose

Users can request that all navis delete their notes about them. This respects data ownership principles — even though each navi writes its own memory files, the user is the subject of those memories and has rights over them.

### Implementation

1. **User initiates deletion**:
   ```bash
   opennavi forget <user_id>    # Delete all notes about this user across all navis
   # or via gateway message:
   "Please delete your memory of me"  → navi processes and deletes relevant files
   ```

2. **Scan and remove**:
   - Find all `~/.opennavi/profiles/*/memory/user/<user_id>.md` files
   - Delete matching entries across all navis' user directories
   - Optionally scan `navi/` and `memories/` directories for references to the user (configurable)

3. **Confirmation**:
   - List what will be deleted before removing
   - Require explicit confirmation for bulk operations
   - Log deletion events in audit trail (optional, for compliance)

### Scope

- **Always deletes**: Files in `memory/user/<user_id>.md` across all navi profiles
- **Optional to delete**: References in session archives and long-term memories (configurable per user preference)
- **Does not affect**: Other users' data or the navi's own session/memory structure

### Edge Cases

1. **Multiple navis share a user file** — Remove only this navi's contribution if cross-navi sharing is enabled, or delete the entire file if it was created by a single source
2. **User mentioned in other contexts** — Session archives and long-term memories may still reference the user; these are configurable (see Section 10 for privacy preferences)
3. **Already-deleted references** — If a navi has already compacted or removed entries about the user, no action needed

### Phase 1: Core Memory
1. **Session memory** — per-interaction state with resume/compaction
2. **User memory** — human-user scoped notes, always-in-context injection
3. **Navi memory** — inter-navi interaction notes (private to writing agent)
4. **Memories archive** — long-term storage with date-based organization
5. **Memory tool** — explicit store/search/delete for user control

### Phase 2: Integration & Search
6. **Session resume** — load previous session context when resuming interactions
7. **FTS5 search** — index memories for on-demand retrieval
8. **Navi discovery** — auto-create `navi/<name>.md` entries on first interaction

### Phase 3: Advanced Management
9. **Auto-archiving** — move completed sessions to archive based on age/usage
10. **Pruning** — long-term memory cleanup when storage grows too large
11. **Expiration** — TTL-based cleanup for time-sensitive memories
12. **User review tools** — CLI commands for browsing and editing all memory tiers

### Requirement: User-Initiated Memory Deletion

The system SHALL allow users to request deletion of all notes about them across all navis, via the `opennavi forget <user_id>` command or a natural-language gateway message.

#### Scenario: CLI deletion request

GIVEN a user runs `opennavi forget <user_id>`
WHEN the command is executed
THEN the system SHALL delete all notes about that user across all navis' user directories.

#### Scenario: Gateway message deletion request

GIVEN a user sends a natural-language deletion request via the gateway (e.g., "Please delete your memory of me")
WHEN the navi processes the request
THEN the system SHALL delete the relevant user memory files.

### Requirement: Deletion Scope

The system SHALL always delete `memory/user/<user_id>.md` files across all navi profiles, and SHALL optionally scan and delete references in session archives, `navi/`, and `memories/` directories based on configuration.

#### Scenario: Optional reference cleanup

GIVEN a user deletion request with reference cleanup enabled
WHEN the system scans for references to the user
THEN the system SHALL also remove matching references in `navi/` and `memories/` directories where configured.

### Requirement: Deletion Confirmation

The system SHALL list what will be deleted before removing anything and SHALL require explicit confirmation for bulk operations.

#### Scenario: Bulk deletion confirmation

GIVEN a bulk deletion request
WHEN the system prepares to remove entries
THEN the system SHALL list the files to be deleted and require explicit confirmation before removing them.

### Requirement: Shared File Deletion Edge Cases

The system SHALL remove only the relevant navi's contribution when multiple navis share a user file with cross-navi sharing enabled, delete the entire file when it was created by a single source, and take no action when references have already been removed.

#### Scenario: Shared user file deletion

GIVEN a shared user file with cross-navi sharing enabled
WHEN a user requests deletion
THEN the system SHALL remove only the relevant navi's contribution rather than the entire file.

#### Scenario: Already-deleted references

GIVEN entries about the user that were already compacted or removed by a navi
WHEN the deletion request is processed
THEN the system SHALL take no action on those references.

---

## 10 Key Design Decisions

1. **Four distinct scopes with user rights**: Session (navi-specific, ephemeral), User (user-owned, persistent — deletable by subject), Navi (agent-to-agent, private to writer), Memories (global archive, on-demand)
2. **User owns their data**: Users can read/edit/delete user-scoped files directly as plain markdown — no tool required
3. **Navi memory is private**: Inter-navi notes are only readable by the writing agent; prevents one agent's assumptions leaking into another's context
4. **Always-in-context for user memory**: Unlike session and memories tiers which are loaded explicitly, user notes are always part of the system prompt so the navi naturally incorporates them
5. **Memories only when needed**: Long-term archive stays out of context until searched, keeping the active window focused
6. **No auto-compaction on user/navi memory**: When limits approach, agent self-manages via consolidation/replacement rather than silent truncation (Hermes Agent pattern)
7. **Right to be forgotten**: Users can request deletion of their notes across all navis — data ownership trumps navi autonomy
8. **Privacy by default**: Cross-navi sharing is opt-in; users control what's shared and can review all contributions


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S060000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |