---
status: draft
created: '08'
updated: '2026-08-11'
tags:
  - spec
  - agent-runtime
priority: medium
spec-refs: [010200, 010201, 010202, 010203, 010204, 010205, 010206]
---

# Plan: Thought Process System: Sessions & Implementation

**Scope:** The thought process system — thoughts as the fundamental unit of a navi's short-term memory, the thought queue with priority management, per-thought session logging, and inter-navi communication. Covers both daemon navis (with thought queues) and one-off navis (simple prompt processing).

> **Part of:** [Plan: Thought Process System](./README.md) — split into 3 parts for readability.

## 00 Session Logging

### 8.1 Per-Thought Session Log

Each thought has its own session log on disk, stored in the thought's directory:

```
~/.opennavi/profiles/<name>/thoughts/<thought_id>/
├── thought.md        # Thought metadata
├── session.md        # LLM session log (messages, tool calls)
├── checkpoint.yaml   # Last checkpoint state
└── result.md         # Final output (if complete)
```

**Format:**

- `thought.md` — YAML metadata (prompt, priority, status, timestamps)
- `session.md` — Markdown session log (human-readable)
- `checkpoint.yaml` — YAML checkpoint (machine-readable)
- `result.md` — Markdown result (human-readable)

### 8.2 Session Log Format (Markdown)

```markdown
# Thought Session: <thought_id>
# Created: 2026-07-31T14:30:00Z
# Priority: 3
# Status: complete


## Integration Points

### 9.1 With Agent Runtime (03-agent-runtime.md)

The agent runtime provides **per-thought session management** for daemon navis:

| Component | Usage |
|-----------|-------|
| AgentState | Per-thought copy-on-write state |
| EventBus | Per-thought event streaming |
| ToolExecutor | Per-thought tool execution |
| SteeringQueue | Per-thought steering during execution |
| Agent Loop | Per-thought LLM session loop |

The agent runtime is **reused per thought** for daemon navis. For one-off navis and council mode, it's used per-session via ChatSession.

### 9.2 With Memory System ([00-memory-system.md](../06-memory/00-memory-system.md))

Completed thoughts write to the memory system:

```
Thought Complete
    ↓
Extract key facts, lessons, preferences
    ↓
Write to memory system ([00-memory-system.md](../06-memory/00-memory-system.md))
    - User memory: if learned about a user
    - Navi memory: if learned about another navi
    - Memories archive: if reusable knowledge
```

### 9.3 With Messaging System (16-messaging-system.md)

Incoming messages from gateways/adapters become thoughts (for daemon navis):

```
Gateway Message
    ↓
Parse message (user, platform, content)
    ↓
Create thought with priority based on sender
    ↓
Enqueue on thought queue (daemon navis)
    OR
Route to ChatSession (council mode)
```

### 9.4 With Autonomous Mode (19-autonomous-mode-interaction.md)

Cron jobs and webhooks create thoughts (for daemon navis):

```
Cron Trigger
    ↓
Create thought with cron-defined priority
    ↓
Enqueue on thought queue (daemon navis)
    OR
Process directly (one-off navis)

Webhook Event
    ↓
Create thought with webhook-defined priority
    ↓
Enqueue on thought queue (daemon navis)
    OR
Process directly (one-off navis)
```

### 9.5 With Multi-Agent System (14-multi-agent-system.md)

Sub-agents and council mode use **ChatSession** for coordination:

| Concept | Implementation |
|---------|---------------|
| Sub-agent config | `type: sub` in agents.yaml + full navi config |
| Council members | `type: sub` agents added as ChatSession participants |
| Council coordination | ChatSession with CouncilState, rounds, participants |
| Inter-navi communication | Send message → enqueue on queue (daemon) or ChatSession (council) |
| Specialization | Each sub-agent has its own persona, tools, model |

The council mode pattern (14-multi-agent-system.md) uses ChatSession:

```
Main navi starts council:
1. Creates ChatSession with council members as participants
2. Sends question to each member within the session
3. Members respond within the session (CouncilState tracks responses)
4. Main navi synthesizes → delivers result
```

### 9.6 With Context Window Management (21-context-window-management.md)

Per-thought or per-session context window management:

```
Daemon navis (per-thought):
- Each thought has its own context window
- Token budget, compaction, overflow handling per thought

One-off navis and council (per-session):
- Context window managed per ChatSession
- Same context window management as current model
```

### 9.7 With Session Management (10-session-management.md)

Per-thought session persistence for daemon navis:

```
Each thought has its own session log:
- SQLite entry (per-thought messages)
- JSONL export (human-readable backup)
- Compaction (summarize old messages within thought)

Council mode (shared session):
- ChatSession with multiple participants
- Shared SQLite entry, shared JSONL export
- Compaction at session level, not per-navi
```

### Requirement: Per-Thought Session Log on Disk

The system SHALL maintain a per-thought session log on disk for each daemon navi thought, stored in the thought's directory with thought.md, session.md, checkpoint.yaml, and result.md files.

#### Scenario: Session log directory creation

GIVEN a thought is created for a daemon navi
WHEN the thought begins processing
THEN the system SHALL create a thought directory containing metadata (thought.md), the LLM session log (session.md), the last checkpoint state (checkpoint.yaml), and the final output (result.md) when complete.

### Requirement: Session Log Formats

The system SHALL write session logs in distinct formats: YAML metadata for thought.md, human-readable Markdown for session.md and result.md, and machine-readable YAML for checkpoint.yaml.

#### Scenario: Session log writing

GIVEN an LLM session running for a thought
WHEN messages and tool calls are produced
THEN the system SHALL append them to the thought's session.md in a human-readable Markdown format.

### Requirement: Agent Runtime Integration Per Thought

The system SHALL reuse the agent runtime per thought for daemon navis, providing per-thought copy-on-write AgentState, EventBus, ToolExecutor, SteeringQueue, and agent loop.

#### Scenario: Per-thought agent runtime

GIVEN a daemon navi processing a thought
WHEN the thought's LLM session runs
THEN the system SHALL provide the thought with its own AgentState, EventBus, ToolExecutor, and SteeringQueue instances scoped to that thought.

### Requirement: Memory System Integration on Completion

The system SHALL extract key facts, lessons, and preferences from completed thoughts and write them to the memory system, routing to user memory, navi memory, or the memories archive based on the content.

#### Scenario: Completed thought memory write

GIVEN a thought completes successfully
WHEN the system extracts knowledge from the thought
THEN the system SHALL write learned facts to user memory if learned about a user, navi memory if learned about another navi, and the memories archive if reusable knowledge.

### Requirement: Incoming Message to Thought Conversion

The system SHALL convert incoming gateway messages, cron triggers, and webhook events into thoughts for daemon navis, with priority based on the sender or event definition.

#### Scenario: Gateway message conversion

GIVEN a message arrives from a gateway
WHEN the system processes the incoming message
THEN the system SHALL parse the message into user, platform, and content fields and enqueue it as a thought with priority based on the sender.

#### Scenario: Cron trigger conversion

GIVEN a cron job fires for a daemon navi
WHEN the system processes the trigger
THEN the system SHALL create a thought with the cron-defined priority and enqueue it on the thought queue.

### Requirement: Per-Thought Session Persistence

The system SHALL persist per-thought session data for daemon navis, including SQLite entries per thought, JSONL export for human-readable backup, and compaction that summarizes old messages within the thought.

#### Scenario: Per-thought compaction

GIVEN a thought with accumulated old messages
WHEN the context exceeds the compaction threshold
THEN the system SHALL summarize the old messages within that thought's session rather than across navis.

---


## 01 Implementation Order

### Phase 1: Core Thought Queue

1. **Thought struct** — ID, priority, status, lifecycle
2. **ThoughtQueue** — priority queue, slot management
3. **Worker pool** — bounded goroutine pool
4. **Queue persistence** — YAML queue file, crash recovery
5. **Basic navi** — long-running navi with thought queue

### Phase 2: Session Logging

6. **Per-thought session log** — markdown format
7. **Checkpoint/resume** — save and restore thought state
8. **Log cleanup** — rotation and retention policy

### Phase 3: Inter-Navi Communication

9. **Navi registry** — resolve navis by name
10. **Direct messaging** — gateway-based thought delivery
11. **File-based delivery** — queue file for one-off navis
12. **One-off triggering** — CLI trigger for one-off navis
13. **Response routing** — deliver responses back to origin

### Phase 4: Resource Management

14. **Priority slots** — reserved slot allocation
15. **Preemption** — bump/preempt lower-priority thoughts
16. **Slot exhaustion policies** — block, reject, queue
17. **Resource configuration** — per-navi thought queue limits

### Phase 5: Integration

18. **Agent runtime integration** — per-thought state/events
19. **Memory system integration** — write completed thoughts to memory
20. **Messaging integration** — gateway messages → thoughts
21. **Autonomous mode integration** — cron/webhook → thoughts
22. **Multi-agent pattern** — council as conversation pattern

---


## 02 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Thought queue for daemon navis** — priority scheduling, crash recovery | Stability for always-on navis; not needed for one-off navis |
| **ChatSession for council mode** — shared session with participants | Clean coordination model; more efficient than thought routing for short-lived sessions |
| **Sub-agent `type: sub`** — configuration signal, not type distinction | Each sub-agent has its own persona, tools, model — this is the specialization we want |
| **Inter-navi communication as chat** — message-based | Works for both long-running (queue) and council (ChatSession) navis |
| **Per-thought session log** — md/yaml/json for daemon navis | Human-readable, crash recovery, audit trail |
| **Per-session session log** — for one-off navis and council | Shared context, exportable history |
| **Copy-on-write state** — reuse agent runtime pattern | No race conditions, clean state management |
| **Queue persistence** — survives restarts (daemon navis only) | Stability; no lost work on crash |
| **One worker per thought** — isolated processing | Each thought is independent; no cross-contamination |
| **Thought is the navi's short-term memory** — idle = empty | The navi's state is its queue; no implicit working state |
| **Log cleanup** — rotation and retention | Prevents unbounded disk growth |
| **Simple processing for one-off navis** — no queue overhead | Process pending work and exit; no persistence needed |

---


## 03 File Structure

```
internal/
├── agent/
│   ├── agent.go           # Agent struct, NewAgent, public API
│   ├── state.go           # AgentState, copy-on-write mutations
│   ├── events.go          # Event types, EventBus, AgentEndBarrier
│   ├── loop.go            # Core execution loop
│   ├── tools.go           # ToolExecutor, before/after hooks
│   ├── queue.go           # SteeringQueue, FollowUpQueue
│   ├── transform.go       # Context transformation pipeline
│   ├── types.go           # AgentMessage, AgentEvent, AgentTool, AgentConfig
│   ├── registry.go        # AgentRegistry
│   ├── session.go         # ChatSession, Participant, SessionMode
│   ├── council.go         # CouncilState, council coordination
│   └── modify.go          # modify_config tool implementation
│
├── thought/               # Thought process system (daemon navis)
│   ├── thought.go         # Thought struct, lifecycle
│   ├── queue.go           # ThoughtQueue, priority queue
│   ├── slot.go            # Slot management
│   ├── worker.go          # WorkerPool, worker lifecycle
│   ├── session.go         # ThoughtSession, per-thought LLM session
│   ├── checkpoint.go      # Checkpoint save/load
│   ├── log.go             # Session log management
│   └── cleanup.go         # Log rotation, retention
│
└── navi/                  # Navi lifecycle
    ├── navi.go            # Navi struct, Run
    ├── longrunning.go     # Long-running navi (daemon + thought queue)
    ├── oneoff.go          # One-off navi (simple processing)
    ├── subagent.go        # Sub-agent execution (ChatSession)
    └── config.go          # Navi configuration
```

---


## 04 Migration Notes

### What Changes

1. **Add `thought_queue` config for daemon navis** — optional, only for navis with gateway/cron interfaces
2. **Add per-thought session logging** — new directory structure for daemon navis
3. **Add queue persistence** — YAML queue file for daemon navis
4. **Add ChatSession for council mode** — shared session with participants (no change to existing council model)
5. **Keep `type: sub`** — configuration signal for delegated agents

### What Stays the Same

1. **Agent runtime components** — state, events, tool executor (reused per thought or per-session)
2. **Memory system** — same four-tier memory ([00-memory-system.md](../06-memory/00-memory-system.md))
3. **Session management** — same SQLite + JSONL (10-session-management.md)
4. **Context window management** — same token budgeting (21-context-window-management.md)
5. **Messaging system** — same gateway, adapters (16-messaging-system.md)
6. **Cross-cutting concerns** — same concurrency model, error handling (17-cross-cutting-concerns.md)
7. **Sub-agent config** — same agents.yaml format with `type: sub`
8. **Council mode** — same ChatSession with CouncilState (14-multi-agent-system.md)

### What's New

1. **Thought queue** — for daemon navis only (gateway/cron interfaces)
2. **Per-thought session logs** — md/yaml/json on disk
3. **Worker pool** — bounded goroutine pool for processing thoughts
4. **Simple processing for one-off navis** — no queue, process and exit

---


## 05 Example: Project Manager Navi

### Configuration

```yaml
navis:
  hermes:
    name: "Hermes"
    provider: "anthropic"
    model: "claude-sonnet-4"
    thought_queue:
      max_concurrent: 3
      priority_slots: 1
      slot_exhaustion: "queue"
    interfaces: [gateway, cron]
    tools: ["read", "write", "edit", "bash", "grep", "find"]
    persona: "persona/hermes.md"
```

### Thought Lifecycle

```
1. User sends message via gateway
   → Thought created (priority 1)
   → Enqueued on Hermes' queue

2. Hermes picks up thought
   → Worker starts LLM session
   → Session log: ~/.opennavi/profiles/hermes/thoughts/<id>/session.md

3. Hermes delegates to Coder
   → Creates thought for Coder
   → Sends via gateway (Coder is long-running)
   → Or writes to Coder's queue file (Coder is one-off)

4. Coder processes thought
   → LLM session for Coder
   → Session log: ~/.opennavi/profiles/coder/thoughts/<id>/session.md

5. Coder responds
   → Response thought routed back to Hermes
   → Hermes enqueues on its queue

6. Hermes synthesizes and responds to user
   → Thought completes
   → Session log finalized
   → Key facts written to memory system ([00-memory-system.md](../06-memory/00-memory-system.md))
```

---


## 06 Key Design Decisions Summary

| Decision | Rationale |
|----------|-----------|
| **Thought queue for daemon navis** — priority scheduling, crash recovery | Stability for always-on navis |
| **ChatSession for council mode** — shared session with participants | Clean coordination; more efficient than thought routing |
| **Sub-agent `type: sub`** — configuration signal | Each sub-agent has its own persona, tools, model |
| **Inter-navi communication as chat** — message-based | Works for both long-running (queue) and council (ChatSession) navis |
| **Per-thought session log** — md/yaml/json for daemon navis | Human-readable, crash recovery, audit trail |
| **Per-session session log** — for one-off navis and council | Shared context, exportable history |
| **Queue persistence** — survives restarts (daemon navis only) | Stability; no lost work |
| **Copy-on-write per thought** — reuse agent runtime | Clean state, no races |
| **One worker per thought** — isolated processing | Independence, no cross-contamination |
| **Council as ChatSession** — multi-navi session pattern | Shared context, explicit coordination |
| **Log cleanup** — rotation and retention | Prevents unbounded disk growth |
| **Simple processing for one-off navis** — no queue overhead | Process pending work and exit |

---

## Related


### Other Parts

- [Thoughts & Queue](./00-thought-queue.md)
- [Workers & Navi Communication](./01-workers-navi-comm.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S010200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |