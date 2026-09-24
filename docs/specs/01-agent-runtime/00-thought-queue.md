---
status: draft
created: '08'
updated: '2026-09-23'
tags:
  - spec
  - agent-runtime
priority: medium
spec-refs: [010000, 010001, 010002, 010003]
---

# Plan: Thought Process System: Thoughts & Queue

**Scope:** The thought process system — thoughts as the fundamental unit of a navi's short-term memory, the thought queue with priority management, per-thought session logging, and inter-navi communication. The thought queue is the navi runtime's main loop in every run mode (see S010003); this file covers the house and its doors.

> **Part of:** [Plan: Thought Process System](./README.md) — split into 3 parts for readability.

## 00 Overview

### 1.1 The House and Its Doors

The navi runtime is the **house**; the thought queue is its **main loop** — in every run mode. Whether the house runs as an always-on daemon or as the plain binary, the same queue drives it: durable, prioritized, preemptible thoughts with identical lifecycle, persistence, and crash recovery.

The **doors** differ per run mode; the queue does not:

| Door | Enters as | Granularity |
|-------|-----------|---------|
| Connectors (gateway, cron, bot adapters, CLI enqueue) | one thought per message | discrete, schedulable work |
| TUI (human portal) | attach/steer existing thoughts, or schedule new ones | conversational (operator decision: connector messages are thought-per-message; TUI messages attach/steer/schedule) |
| Tools | the navi acting out through the governed tool registry | — (tool calls happen inside a running thought) |

**No door bypasses the loop.** There is no execution path that drives the agent around the queue: every run mode assembles the SAME runtime — the queue, pool, governed tools, and connectors (cron, metrics, portal) always load (S010003, NAVI-325) — and the TUI vs `--daemon` distinction is presentation only. The interactive session's thought occupies a foreground slot (S010003) when a session (TUI) is attached; headless runs have no foreground thought. ChatSession remains for what it actually is — council-mode and delegation coordination (short-lived multi-agent sessions) — not an alternative execution model for ordinary work.

### 1.2 The Thought

A thought is a discrete reasoning task with a defined lifecycle. For daemon navis, the thought queue is the navi's working memory: what it's currently thinking about *is* what it is.

This is a **stability feature** — a way to ensure that the things a navi needs to do get checked, prioritized, and executed reliably. A navi could have a single worker processing thoughts sequentially, or multiple workers. The queue ensures correctness regardless.

### 1.3 Navis and Sub-Agents

**Navis are navis. Sub-agents are just navis with `type: sub`.**

A navi is differentiated by its interfaces and configuration, not by a fundamental type distinction:

| | Long-running Navi | One-off Navi | Sub-agent | Council Member |
|---|---|---|---|---|
| **Interfaces** | `[gateway, cron]` | `[cli]` | Any | Any |
| **Execution** | Thought queue (daemon) | Process pending, exit | ChatSession (short-lived) | ChatSession (short-lived) |
| **Config** | Full navi config | Full navi config | Full navi config (in agents.yaml) | Full navi config (in agents.yaml) |
| **Use case** | Always-on assistant | On-demand tool | Delegated task | Multi-agent coordination |

The `type: sub` field in agent config is a **configuration signal** — it tells the system this navi is defined for use as a delegated agent (may have restricted tools, specific persona). It does not change the fundamental model.

### 1.4 Inter-Navi Communication

Navis communicate by **sending messages to each other**. The mechanism depends on the target navi's type:

- **Long-running target** → enqueue on thought queue (gateway message or file)
- **One-off target** → write to queue file, trigger via CLI
- **Council member** → add as ChatSession participant (short-lived coordination)

This is unified under the concept of **chat** — a navi opens a conversation with another navi, and the messages become thoughts (or session entries) in the recipient's context.

### Requirement: One House, One Queue, Many Doors

The system SHALL run every navi through the thought queue as its main loop: the navi runtime (the house) runs the queue whether it executes as an always-on daemon or as the plain binary, and every interface (TUI, connectors, tools) enters the house as a door feeding the same queue.

#### Scenario: Daemon navi uses thought queue

GIVEN a navi configured with gateway and/or cron interfaces
WHEN the navi starts
THEN the system SHALL initialize a thought queue with priority scheduling and crash recovery.

#### Scenario: Binary run uses the same queue in-process

GIVEN a navi run as the plain binary (interactive session)
WHEN the user submits work through the TUI
THEN the system SHALL enqueue it as a durable thought in the same in-process queue (foreground slot per S010003) with identical lifecycle, persistence, park-with-checkpoint, and crash-recovery semantics as daemon work.

#### Scenario: No door bypasses the queue

GIVEN any interface implementation (TUI, gateway adapter, cron, CLI)
WHEN it delivers work to the navi
THEN the system SHALL route it through the thought queue — no interface SHALL drive `agent.Prompt()` (or equivalent) directly as an execution model.

#### Scenario: Council mode uses ChatSession

GIVEN a council mode session with multiple navi participants
WHEN the navis coordinate on a shared task
THEN the system SHALL use ChatSession with participants, rounds, and explicit coordination.

### Requirement: Thought as Discrete Reasoning Unit

The system SHALL model a thought as a discrete reasoning task with a defined lifecycle, serving as the fundamental unit of a navi's short-term memory and working state.

#### Scenario: Thought lifecycle tracking

GIVEN a thought is created for a navi
WHEN the thought transitions through its lifecycle
THEN the system SHALL track the thought's status, session, and resource allocation.

### Requirement: Navi Configuration-Based Differentiation

The system SHALL differentiate navis by their interfaces and configuration rather than by a fundamental type distinction, where the `type: sub` field in agent configuration acts as a configuration signal.

#### Scenario: Sub-agent configuration

GIVEN a navi defined with `type: sub` in agents.yaml
WHEN the system resolves the navi's role
THEN the system SHALL recognize it as a delegated agent while using the same execution model as any other navi of its interface type.

### Requirement: Inter-Navi Message-Based Communication

The system SHALL support inter-navi communication by sending messages between navis, unified under the concept of chat, where the delivery mechanism depends on the target navi's type.

#### Scenario: Message to long-running navi

GIVEN a sending navi and a target long-running navi with a thought queue
WHEN the sending navi sends a message to the target
THEN the system SHALL enqueue the message as a thought on the target's queue.

#### Scenario: Message to council member

GIVEN a sending navi and a target council member navi
WHEN the sending navi sends a message to the target
THEN the system SHALL add the message as a ChatSession participant entry.

---


## 01 The Thought

### 2.1 Thought Structure

A thought is a discrete reasoning task with a defined lifecycle.

```go
type Thought struct {
    // Identity
    ID        string    // unique identifier
    CreatedAt time.Time // when the thought was created
    UpdatedAt time.Time // last state change

    // Content
    Prompt    string    // the reasoning task / goal
    Context   string    // supporting context (files, previous work, etc.)
    Metadata  map[string]string // arbitrary key-value metadata

    // Priority
    Priority  int       // 1 (highest) to 10 (lowest)

    // Lifecycle
    Status    ThoughtStatus
    SessionID string    // associated LLM session (if running)

    // Resource tracking
    SlotID    string    // which slot this thought occupies (if running)
    WorkerID  string    // which worker is processing this (if running)

    // Persistence
    LogPath   string    // path to session log on disk
    Checkpoint []byte   // last checkpoint state (for resume)
}

type ThoughtStatus string
const (
    ThoughtPending   ThoughtStatus = "pending"    // waiting in queue
    ThoughtRunning   ThoughtStatus = "running"    // currently being processed
    ThoughtPaused    ThoughtStatus = "paused"     // paused, can resume
    ThoughtComplete  ThoughtStatus = "complete"   // finished successfully
    ThoughtFailed    ThoughtStatus = "failed"     // failed, can retry
    ThoughtCancelled ThoughtStatus = "cancelled"  // cancelled by user
)
```

### 2.2 Thought Lifecycle

```
pending → running → complete
         → running → paused → running → complete
         → running → failed → pending (retry)
         → pending → cancelled
```

**Transitions:**

| Transition | Triggered By | Notes |
|-----------|-------------|-------|
| `pending → running` | Worker picks up thought | Slot allocated, LLM session started |
| `running → paused` | Resource pressure, manual pause | Checkpoint saved, session log flushed |
| `paused → running` | Resume requested, slot available | Session resumed from checkpoint |
| `running → complete` | Thought goal achieved | Session log finalized |
| `running → failed` | Error, timeout | Can be retried (back to pending) |
| `pending → cancelled` | User request | Resource released |

### 2.3 Priority Levels

| Priority | Meaning | Use Case |
|----------|---------|----------|
| **1** | Critical interrupt | User message, system alert |
| **2** | High priority | Incoming navi message, webhook event |
| **3** | Above average | Cron-triggered task, delegated thought |
| **4** | Normal | Routine work, user message (non-urgent) |
| **5** | Below average | Background maintenance |
| **6-10** | Low priority | Cleanup, optimization, non-urgent tasks |

Higher priority thoughts can:
- **Bump** lower-priority thoughts in the queue (move them down)
- **Preempt** a running thought (pause it, free a slot)
- **Fill reserved slots** when no slot is available

### 2.4 Thought as Short-Term Memory

The thought queue *is* the navi's short-term memory. When a navi is idle (no running thoughts), its working state is empty. When it has thoughts, its state is the collection of those thoughts.

This means:
- **Idle navis have no working memory** — they're not "thinking" about anything
- **A navi's identity is its queue** — what it's currently processing defines its current state
- **Thoughts are transient** — they exist until complete or cancelled
- **The queue persists** — survived restarts, so a navi can resume its thoughts

### Requirement: Thought Structure and Identity

The system SHALL define a thought with identity fields (ID, CreatedAt, UpdatedAt), content fields (Prompt, Context, Metadata), priority level, lifecycle status, resource tracking (SlotID, WorkerID), and persistence fields (LogPath, Checkpoint).

#### Scenario: Thought creation

GIVEN a new thought is created
WHEN the system records the thought
THEN the thought SHALL contain a unique identifier, timestamp, prompt, context, priority, and initial pending status.

### Requirement: Thought Lifecycle State Machine

The system SHALL manage thoughts through a lifecycle of states: pending, running, paused, complete, failed, and cancelled, with defined transitions between each state.

#### Scenario: Thought processing

GIVEN a thought in pending status
WHEN a worker picks up the thought
THEN the system SHALL transition the thought to running, allocate a slot and worker ID, and start an LLM session.

#### Scenario: Thought failure and retry

GIVEN a thought in running status
WHEN the thought encounters an error or timeout
THEN the system SHALL transition the thought to failed and optionally retry by returning it to pending status.

### Requirement: Priority-Based Scheduling

The system SHALL support priority levels from 1 (highest, critical interrupt) to 10 (lowest, non-urgent tasks), where higher-priority thoughts can bump lower-priority thoughts in the queue and preempt running thoughts.

#### Scenario: Priority bumping

GIVEN a lower-priority thought in the queue
WHEN a higher-priority thought is enqueued
THEN the system SHALL order the higher-priority thought before the lower-priority one.

#### Scenario: Priority preemption

GIVEN a lower-priority thought currently running
WHEN a higher-priority thought requires a slot
THEN the system SHALL pause the running thought and allocate its slot to the higher-priority thought.

### Requirement: Thought Queue as Short-Term Memory

The system SHALL treat the thought queue as the navi's short-term memory, where an idle navi with no running thoughts has no working memory, and the navi's current state is defined by its queued and running thoughts.

#### Scenario: Idle navi state

GIVEN a navi with no pending or running thoughts
WHEN the navi's state is inspected
THEN the system SHALL report the navi as idle with no working memory.

---


## 02 The Thought Queue

### 3.1 Queue Structure

The thought queue is a priority queue with bounded capacity.

```go
type ThoughtQueue struct {
    // Configuration
    maxConcurrent int          // total concurrent processing capacity
    prioritySlots int           // reserved for high-priority thoughts
    slotExhaustion SlotExhaustionPolicy

    // State
    thoughts      []*Thought  // all thoughts (pending + running)
    pending       []*Thought  // pending thoughts, sorted by priority
    running       []*Thought  // currently running thoughts
    slots         []Slot      // available slots
    mu            sync.RWMutex

    // Events
    events        chan QueueEvent
}

type Slot struct {
    ID        string
    WorkerID  string
    ThoughtID string
    Available bool
}

type SlotExhaustionPolicy string
const (
    PolicyBlock  SlotExhaustionPolicy = "block"   // block new thoughts until slot available
    PolicyReject SlotExhaustionPolicy = "reject"  // reject new thoughts with error
    PolicyQueue  SlotExhaustionPolicy = "queue"   // queue indefinitely (unbounded)
)
```

### 3.2 Slot Management

Slots are the navi's processing resources. The user configures how many slots are available and how many are reserved for priority work.

```yaml
navis:
  hermes:
    name: "Hermes"
    thought_queue:
      max_concurrent: 3        # total processing capacity
      priority_slots: 1        # reserved for priority thoughts
      slot_exhaustion: "queue" # what to do when all slots full
```

**Slot allocation rules:**

1. **Priority thoughts** fill reserved slots first
2. **Normal thoughts** fill non-reserved slots
3. **When no slots available:**
   - `block` — enqueue thought, wait for slot
   - `reject` — return error to caller
   - `queue` — add to unbounded pending queue

**Slot preemption:**

A priority thought can preempt a lower-priority running thought:

```go
func (q *ThoughtQueue) preemptLowerPriority(newThought *Thought) *Thought {
    // Find lowest-priority running thought
    for _, t := range q.running {
        if t.Priority <= newThought.Priority {
            // Preempt this thought
            q.pauseThought(t)
            return t
        }
    }
    return nil // nothing to preempt
}
```

### 3.3 Queue Operations

```go
type ThoughtQueue struct {
    // ... (above)

    // Public API
}

// Enqueue a new thought
func (q *ThoughtQueue) Enqueue(thought *Thought) error

// Dequeue the highest-priority pending thought
func (q *ThoughtQueue) Dequeue() (*Thought, bool)

// Bump a thought's priority
func (q *ThoughtQueue) BumpPriority(thoughtID string, newPriority int) error

// Pause a running thought
func (q *ThoughtQueue) Pause(thoughtID string) error

// Resume a paused thought
func (q *ThoughtQueue) Resume(thoughtID string) error

// Cancel a thought
func (q *ThoughtQueue) Cancel(thoughtID string) error

// Get all thoughts (for inspection)
func (q *ThoughtQueue) GetThoughts() []*Thought

// Get thought by ID
func (q *ThoughtQueue) GetThought(id string) (*Thought, error)
```

### 3.4 Queue Persistence

The queue persists to disk so it survives navi restarts.

```
~/.opennavi/profiles/<name>/
└── thoughts/
    ├── queue.yaml           # pending thoughts, sorted by priority
    ├── running.yaml          # running thoughts (if navi crashed)
    └── <thought_id>/        # per-thought directory
        ├── thought.md        # thought metadata
        ├── session.md        # LLM session log
        ├── checkpoint.yaml   # last checkpoint
        └── result.md         # final output (if complete)
```

**Persistence format:**

- `queue.yaml` — pending thoughts, priority-sorted
- `running.yaml` — running thoughts (for crash recovery)
- `<id>/thought.md` — thought metadata (prompt, priority, status)
- `<id>/session.md` — full LLM session log (messages, tool calls)
- `<id>/checkpoint.yaml` — serialized session state for resume
- `<id>/result.md` — final output when thought completes

### 3.5 Crash Recovery

On navi start, the queue is restored from disk:

```go
func (q *ThoughtQueue) Restore() error {
    // 1. Load pending thoughts from queue.yaml
    pending, err := loadYAML[[]Thought]("queue.yaml")
    if err != nil {
        return err
    }

    // 2. Load running thoughts from running.yaml
    running, err := loadYAML[[]Thought]("running.yaml")
    if err != nil {
        return err
    }

    // 3. Transition running → pending (they need to be re-acquired)
    for _, t := range running {
        t.Status = ThoughtPending
        t.SlotID = ""
        t.WorkerID = ""
        pending = append(pending, t)
    }

    // 4. Sort pending by priority
    sort.Slice(pending, func(i, j int) bool {
        return pending[i].Priority < pending[j].Priority
    })

    q.pending = pending
    return nil
}
```

### Requirement: Priority Queue with Bounded Capacity

The system SHALL implement the thought queue as a priority queue with bounded capacity, configurable via max_concurrent, priority_slots, and slot_exhaustion policy.

#### Scenario: Queue initialization

GIVEN a navi configured with thought_queue settings
WHEN the navi starts
THEN the system SHALL create a thought queue with the configured max_concurrent slots, priority_slots reserved for high-priority thoughts, and the specified slot exhaustion policy.

### Requirement: Slot Allocation and Preemption

The system SHALL allocate processing slots where priority thoughts fill reserved slots first, normal thoughts fill non-reserved slots, and priority thoughts can preempt lower-priority running thoughts when no slots are available.

#### Scenario: Normal slot allocation

GIVEN a normal thought and available non-reserved slots
WHEN the thought is enqueued
THEN the system SHALL allocate a non-reserved slot to the thought.

#### Scenario: Priority preemption

GIVEN a priority thought and all slots occupied by lower-priority thoughts
WHEN the priority thought is enqueued
THEN the system SHALL pause the lowest-priority running thought and allocate its slot to the priority thought.

### Requirement: Slot Exhaustion Policy

The system SHALL support three slot exhaustion policies: block (enqueue thought and wait for slot), reject (return error to caller immediately), and queue (add to unbounded pending queue).

#### Scenario: Block exhaustion policy

GIVEN a thought queue with slot_exhaustion set to "block"
WHEN all slots are full and a new thought arrives
THEN the system SHALL enqueue the thought and wait until a slot becomes available.

#### Scenario: Reject exhaustion policy

GIVEN a thought queue with slot_exhaustion set to "reject"
WHEN all slots are full and a new thought arrives
THEN the system SHALL return an error to the caller immediately.

### Requirement: Queue Persistence and Crash Recovery

The system SHALL persist the thought queue to disk so it survives navi restarts, and restore the queue on navi start by transitioning running thoughts to pending for re-acquisition.

#### Scenario: Queue persistence

GIVEN a running navi with pending and running thoughts
WHEN the navi shuts down gracefully
THEN the system SHALL persist pending thoughts to queue.yaml and running thoughts to running.yaml.

#### Scenario: Crash recovery

GIVEN a navi starts after an unexpected crash
WHEN the queue is restored from disk
THEN the system SHALL load pending thoughts from queue.yaml, load running thoughts from running.yaml, transition all running thoughts to pending status, and sort them by priority.

---


---

## 06 Binary-Run Semantics (S010003)

### Requirement: Foreground Slot for Binary Runs

A navi run without connectors (the plain binary, interactive session) SHALL drive the same thought queue in-process: the session's current thought occupies a foreground slot, and all work submitted through the TUI is enqueued as durable thoughts.

#### Scenario: User message becomes a durable thought

GIVEN a navi running as the plain binary with an attached TUI
WHEN the user schedules new work through the TUI
THEN the system SHALL enqueue a thought with the same lifecycle as daemon-enqueued work.

#### Scenario: Interactive work parks on interrupt

GIVEN a foreground thought is running in an interactive session
WHEN the session is interrupted (preemption, Ctrl+C, process signal)
THEN the system SHALL park the thought with a checkpoint (running → paused), never kill-and-lose.

#### Scenario: Session ends and the queue is drained

GIVEN a binary run whose foreground thought is complete and no further work is queued
WHEN the user exits the session
THEN the navi process SHALL exit; no daemon lifecycle is implied.

#### Scenario: Foreground work is schedulable like any other

GIVEN a binary run with a foreground thought running and additional enqueued thoughts
WHEN the queue schedules work
THEN the foreground session's thoughts SHALL compete for slots by the same priority and preemption rules as daemon work.

## Related


### Other Parts

- [Workers & Navi Communication](./01-workers-navi-comm.md)
- [Sessions & Implementation](./02-sessions-implementation.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S010000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-23 | S010003 | 0.2.0 | House model: queue is the runtime's main loop in every run mode; §1.1 rewritten from two-execution-models to house-and-doors; binary-run (in-process queue, foreground slot) spec'd; no-door-bypasses-the-queue requirement added (thought-loop case study) |
| 2026-09-23 | S010003 | 0.2.1 | One assembly (NAVI-325): every run mode assembles the same runtime — connectors (cron, metrics, portal) load in every mode; TUI vs `--daemon` differ by presentation only; the foreground slot is reserved only when a session (TUI) is attached — headless runs have no foreground thought |

