---
status: draft
created: '08'
updated: '2026-09-23'
tags:
  - spec
  - agent-runtime
priority: medium
spec-refs: [010100, 010101, 010102, 010103, 010104]
---

# Plan: Thought Process System: Workers & Navi Communication

**Scope:** The thought process system — thoughts as the fundamental unit of a navi's short-term memory, the thought queue with priority management, per-thought session logging, and inter-navi communication. Covers both daemon navis (with thought queues) and one-off navis (simple prompt processing).

> **Part of:** [Plan: Thought Process System](./README.md) — split into 3 parts for readability.

## 00 Thought Workers

### 4.1 Worker Pool

Thought workers are goroutines that process thoughts from the queue. The pool size is bounded by `max_concurrent`.

```go
type WorkerPool struct {
    maxWorkers int
    workers    []*Worker
    queue      *ThoughtQueue
    stopCh     chan struct{}
}

type Worker struct {
    ID      string
    queue   *ThoughtQueue
    stopCh  chan struct{}
}
```

### 4.2 Worker Lifecycle

Each worker runs a simple loop:

```go
func (w *Worker) Run(ctx context.Context) {
    for {
        // 1. Try to dequeue a thought
        thought, ok := w.queue.Dequeue()
        if !ok {
            // No thoughts available — wait for signal
            select {
            case <-w.queue.ThoughtAvailable():
                continue
            case <-ctx.Done():
                return
            }
        }

        // 2. Process the thought
        w.processThought(ctx, thought)
    }
}

func (w *Worker) processThought(ctx context.Context, thought *Thought) {
    // 1. Update status
    thought.Status = ThoughtRunning
    thought.SlotID = w.ID
    thought.WorkerID = w.ID
    w.queue.persist()

    // 2. Start LLM session
    session := w.startSession(thought)

    // 3. Run the session loop
    result, err := session.Run(ctx)

    // 4. Handle result
    if err != nil {
        thought.Status = ThoughtFailed
        // Can retry: back to pending
        w.queue.enqueuePending(thought)
    } else {
        thought.Status = ThoughtComplete
        thought.Checkpoint = nil
    }

    // 5. Finalize session log
    w.finalizeSessionLog(thought, result)

    // 6. Free slot
    w.queue.freeSlot(w.ID)
}
```

### 4.3 LLM Session Per Thought

Each thought runs its own LLM session. This is the key to the system:

- **Each thought has its own message history** — no cross-contamination
- **Each thought has its own tool calls** — isolated execution
- **Each thought has its own session log** — persisted on disk
- **Each thought can be paused/resumed independently** — slot management

The session is copy-on-write (from 03-agent-runtime.md §3.1):

```go
type ThoughtSession struct {
    thought    *Thought
    state      *AgentState  // copy-on-write
    provider   llm.Provider
    executor   *ToolExecutor
    eventBus   *EventBus
    steeringQueue *SteeringQueue
}

func (s *ThoughtSession) Run(ctx context.Context) (*ThoughtResult, error) {
    // Same loop as agent runtime (03-agent-runtime.md §8)
    // But scoped to this thought's state
    for {
        // Check for steering messages
        if msg, ok := s.steeringQueue.Next(); ok {
            s.state = s.state.AppendMessage(AgentMessage{Role: RoleUser, Content: msg})
        }

        // Transform context
        messages := transformContext(s.state)

        // Send to LLM
        events, err := s.provider.Stream(ctx, messages, s.executor.Tools(), s.state.Config)
        if err != nil {
            return nil, err
        }

        // Process stream events (same as 03-agent-runtime.md §8)
        // ...

        // Execute tool calls
        if len(fullMessage.ToolCalls) > 0 {
            results := s.executor.Execute(ctx, fullMessage.ToolCalls)
            for _, result := range results {
                s.state = s.state.AppendMessage(AgentMessage{
                    Role: RoleTool,
                    ToolResult: result.Content,
                })
            }
        }

        // Check stop conditions
        if shouldStop(s.state) {
            break
        }
    }

    // Save checkpoint
    s.saveCheckpoint()

    return &ThoughtResult{
        Output:   fullMessage.Content,
        ToolCalls: fullMessage.ToolCalls,
    }, nil
}
```

### 4.4 Session Checkpointing

Periodic checkpoints allow thoughts to resume after pause or crash:

```go
func (s *ThoughtSession) saveCheckpoint() error {
    checkpoint := struct {
        Messages []AgentMessage
        Status   AgentStatus
        Config   AgentConfig
    }{
        Messages: s.state.Messages,
        Status:   s.state.Status,
        Config:   s.state.Config,
    }

    return saveYAML(fmt.Sprintf("%s/checkpoint.yaml", s.thought.ID), checkpoint)
}

func (s *ThoughtSession) loadCheckpoint() error {
    var checkpoint struct {
        Messages []AgentMessage
        Status   AgentStatus
        Config   AgentConfig
    }

    if err := loadYAML(fmt.Sprintf("%s/checkpoint.yaml", s.thought.ID), &checkpoint); err != nil {
        return err
    }

    s.state = &AgentState{
        Messages: checkpoint.Messages,
        Status:   checkpoint.Status,
        Config:   checkpoint.Config,
    }

    return nil
}
```

### Requirement: Worker Pool with Bounded Concurrency

The system SHALL implement a worker pool of bounded goroutines that process thoughts from the queue, where the pool size is configured by max_concurrent.

#### Scenario: Worker pool startup

GIVEN a navi with max_concurrent set to N
WHEN the navi starts
THEN the system SHALL start N worker goroutines that dequeue and process thoughts from the queue.

#### Scenario: Worker dequeue loop

GIVEN an active worker in the pool
WHEN the worker attempts to dequeue a thought
THEN if no thought is available, the worker SHALL wait for a signal that a new thought is available.

### Requirement: Per-Thought LLM Session Isolation

The system SHALL run each thought in its own LLM session with isolated message history, tool calls, session log, and pause/resume capability.

#### Scenario: Thought session isolation

GIVEN a thought is picked up by a worker
WHEN the worker starts an LLM session for the thought
THEN the session SHALL have its own copy-on-write state, provider, tool executor, event bus, and steering queue, isolated from all other thoughts.

#### Scenario: Thought checkpointing

GIVEN a running thought session
WHEN a checkpoint is requested
THEN the system SHALL save the session messages, status, and config to a checkpoint.yaml file for later resume.

### Requirement: Worker Lifecycle for Thought Processing

The system SHALL define a worker lifecycle that dequeues a thought, updates its status to running, processes it via an LLM session, handles the result (complete or failed with retry), finalizes the session log, and frees the slot.

#### Scenario: Successful thought completion

GIVEN a worker processing a thought
WHEN the LLM session completes successfully
THEN the system SHALL set the thought status to complete, clear the checkpoint, finalize the session log, and free the worker slot.

#### Scenario: Failed thought with retry

GIVEN a worker processing a thought
WHEN the LLM session returns an error
THEN the system SHALL set the thought status to failed and re-enqueue it as pending for retry.

---


## 01 Navi-to-Navi Communication

### 5.1 Communication Patterns

Navis communicate by **sending messages to each other**. The mechanism depends on the target:

| Target Type | Mechanism | Use Case |
|------------|-----------|----------|
| **Long-running navi** | Enqueue on thought queue | Delegation, async work |
| **One-off navi** | Write to queue file + CLI trigger | On-demand tool execution |
| **Council member** | Add as ChatSession participant | Multi-agent coordination |

### 5.2 Council Mode via ChatSession

For council mode, navis use **ChatSession** — a shared session with participants, rounds, and explicit coordination. This is more efficient than routing through thought queues for short-lived coordination:

```
User: /council coder researcher "Review this architecture"
    ↓
Main navi creates ChatSession with:
  - Participants: [main, coder, researcher]
  - Mode: council
  - Question: "Review this architecture"
    ↓
Main navi sends question to each member:
  @coder: "Review the implementation."
  @researcher: "Research best practices."
    ↓
Each member responds within the session:
  @coder: [implementation analysis]
  @researcher: [research findings]
    ↓
Main navi synthesizes → delivers result
```

**Why ChatSession for council?**
- All participants share one session context
- Clean "round" model (round 1, round 2, ...)
- No serialization overhead between navis
- Easy to see the full conversation history
- Explicit coordination via CouncilState

### 5.3 Council Member Configuration

Council members are configured in agents.yaml with `type: sub`:

```yaml
agents:
  default:
    type: primary
    name: "Hermes"
    interfaces: [gateway, cron]

  coder:
    type: sub
    name: "Coder"
    persona: "persona/coder.md"
    tools: ["read", "write", "edit", "bash"]

  researcher:
    type: sub
    name: "Researcher"
    persona: "persona/researcher.md"
    tools: ["web_search", "fetch_content"]
```

Each council member has its own **full navi config** — persona, tools, model, etc. This is what gives each council member its specialty. The `type: sub` field is a configuration signal, not a type distinction.

### 5.2 Addressing Navis

Navis are addressed by name, resolved from the navi registry:

```go
type NaviRegistry struct {
    navis map[string]*Navi // name → navi
}

func (r *NaviRegistry) Resolve(name string) (*Navi, error) {
    navi, ok := r.navis[name]
    if !ok {
        return nil, fmt.Errorf("navi %s not found", name)
    }
    return navi, nil
}
```

### 5.3 Delivery Methods

A navi can send a thought to another navi through multiple methods:

**Method 1: Direct message (long-running navi)**

If the target navi is running (has gateway/webhook interfaces), send directly:

```go
func (a *Navi) SendMessage(targetName, prompt string, priority int) error {
    target, err := a.registry.Resolve(targetName)
    if err != nil {
        return err
    }

    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   prompt,
        Priority: priority,
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", targetName, uuid.New().String()),
    }

    // Send via gateway (WebSocket, HTTP, etc.)
    return target.gateway.SendThought(thought)
}
```

**Method 2: File-based (one-off navi)**

If the target navi is not running, write the thought to its queue file:

```go
func (a *Navi) SendToFile(targetName, prompt string, priority int) error {
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   prompt,
        Priority: priority,
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", targetName, uuid.New().String()),
    }

    // Write to target's queue file
    queuePath := fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/queue.yaml", targetName)
    return a.enqueueToFile(queuePath, thought)
}
```

**Method 3: Trigger one-off navi**

If the target is a one-off navi (CLI interface), trigger it to process the pending thought:

```go
func (a *Navi) TriggerOneOff(targetName, prompt string, priority int) error {
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   prompt,
        Priority: priority,
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", targetName, uuid.New().String()),
    }

    // Trigger the navi via command
    cmd := exec.Command("opennavi", "run", targetName, "--thought", thought.ID)
    return cmd.Start()
}
```

### 5.4 Response Delivery

When a navi completes a thought, the response routes back to the originating navi:

```go
type ThoughtResult struct {
    ThoughtID string
    Output    string
    Success   bool
    Error     error
    Duration  time.Duration
}

func (b *Navi) deliverResponse(thought *Thought, result *ThoughtResult) error {
    // Find the originating navi (from thought metadata)
    originName := thought.Metadata["origin"]
    origin, err := b.registry.Resolve(originName)
    if err != nil {
        return err
    }

    // Create a response thought for the origin
    responseThought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   fmt.Sprintf("Result from thought %s:\n%s", thought.ID, result.Output),
        Priority: 4, // Normal priority for responses
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", originName, uuid.New().String()),
    }

    // Deliver via same method as original (gateway or file)
    if origin.gateway != nil {
        return origin.gateway.SendThought(responseThought)
    }
    return origin.enqueueToFile(responseThought)
}
```

### Requirement: Communication Patterns by Target Type

The system SHALL support inter-navi communication via three mechanisms depending on the target type: enqueue on thought queue for long-running navis, write to queue file with CLI trigger for one-off navis, and add as ChatSession participant for council members.

#### Scenario: Direct message to long-running navi

GIVEN a sending navi and a target long-running navi
WHEN the sending navi sends a message
THEN the system SHALL deliver the thought directly to the target's gateway.

#### Scenario: File-based delivery to one-off navi

GIVEN a sending navi and a target one-off navi
WHEN the sending navi sends a message
THEN the system SHALL write the thought to the target's queue file and trigger the target via CLI.

### Requirement: Council Mode via ChatSession

The system SHALL support council mode using ChatSession, a shared session with participants, rounds, and explicit coordination, where each member has its own full navi configuration including persona, tools, and model.

#### Scenario: Council session creation

GIVEN a main navi initiating a council
WHEN the main navi creates a ChatSession with council members as participants
THEN the system SHALL send the question to each member within the session and collect responses tracked by CouncilState.

### Requirement: Navi Registry and Addressing

The system SHALL maintain a navi registry that resolves navis by name to enable message routing between navis.

#### Scenario: Navi resolution

GIVEN a navi name
WHEN the system resolves the name via the registry
THEN the system SHALL return the corresponding navi instance or an error if not found.

### Requirement: Response Delivery to Originating Navi

The system SHALL route thought results back to the originating navi by reading the origin metadata from the thought and delivering via the same method as the original message.

#### Scenario: Response routing

GIVEN a thought completes with a result
WHEN the system delivers the response
THEN the system SHALL read the origin navi name from the thought metadata, create a response thought, and deliver it via the origin's gateway or queue file.

---


## 02 Navi Lifecycle

### 6.1 Doors, Not Forks

Every interface is a door into the same house: there is one execution model (the thought queue, S010000) and doors differ only in how they feed it. No interface maps to a separate execution model:

| Interfaces | Door | How it feeds the queue |
|-----------|----------------|--------|
| `[gateway, cron]`, bot adapters | **Daemon run** (external doors open) | one thought per inbound message / firing |
| `[tui]` (binary run or remote attach) | **Portal door** | schedule new thoughts; attach/steer existing ones (S010104 foreground slot in-process, or the same view attached via gateway S000407) |
| Any, used as sub-agent | **ChatSession** (delegation only) | Short-lived, session-based coordination — not an execution model for ordinary work |

### 6.2 Long-running Navi (Thought Queue)

A long-running navi starts its thought worker pool and listens for incoming thoughts:

```go
func (n *Navi) Run(ctx context.Context) error {
    // 1. Restore queue from disk
    if err := n.queue.Restore(); err != nil {
        return err
    }

    // 2. Start worker pool
    pool := NewWorkerPool(n.queue, n.config.ThoughtQueue.MaxConcurrent)
    pool.Start(ctx)

    // 3. Start interfaces (gateway, cron, etc.)
    for _, iface := range n.interfaces {
        go iface.Run(ctx, n.handleIncomingMessage)
    }

    // 4. Wait for stop signal
    <-ctx.Done()

    // 5. Cleanup
    pool.Stop()
    n.queue.Persist()

    return nil
}

func (n *Navi) handleIncomingMessage(msg Message) {
    // Convert incoming message to a thought
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   msg.Content.Text,
        Priority: n.estimatePriority(msg),
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", n.name, uuid.New().String()),
    }

    // Enqueue on the queue
    n.queue.Enqueue(thought)
}
```

### 6.3 One-off Navi (Simple Processing)

A one-off navi processes pending thoughts and exits. No worker pool, no persistence:

```go
func (n *Navi) Run(ctx context.Context) error {
    // 1. Get pending work (from queue file or CLI args)
    work := n.getPendingWork()
    if work == nil {
        return nil // Nothing to do
    }

    // 2. Process the work directly (no queue, no persistence)
    session := n.createSession(work)
    result, err := session.Run(ctx)

    // 3. Deliver response (via CLI output or webhook callback)
    n.deliverResponse(work.Origin, result)

    return err
}
```

### 6.4 Sub-agent Navi (ChatSession)

When a navi is used as a sub-agent (via `type: sub` in agents.yaml), it runs within a ChatSession:

```go
func (n *Navi) RunAsSubAgent(ctx context.Context, session *ChatSession) error {
    // 1. Get the prompt from the session
    msg := session.LastUserMessage()

    // 2. Create a short-lived session with this navi's config
    agentSession := n.createSession(msg)

    // 3. Process and deliver result back to the session
    result, err := agentSession.Run(ctx)
    session.AddCouncilResponse(n.ID, result.Output)

    return err
}
```

### 6.5 Navi Configuration

Navis are configured by their interfaces and thought queue settings:

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

  coder:
    name: "Coder"
    provider: "anthropic"
    model: "claude-sonnet-4"
    interfaces: [cli]

  researcher:
    name: "Researcher"
    provider: "openrouter"
    model: "o3-pro"
    type: sub  # Used as delegated agent
    persona: "persona/researcher.md"
    tools: ["web_search", "fetch_content", "code_search"]
```

### Requirement: Execution Model Selection by Interface

The system SHALL select the execution model for a navi based on its configured interfaces: the thought queue (daemon) for navis with gateway and/or cron interfaces, simple processing for CLI-only navis, and ChatSession for sub-agents.

#### Scenario: Long-running navi startup

GIVEN a navi configured with gateway and cron interfaces
WHEN the navi starts
THEN the system SHALL restore the thought queue from disk, start the worker pool, and begin listening on its interfaces.

#### Scenario: One-off navi processing

GIVEN a navi configured with only a CLI interface
WHEN the navi runs
THEN the system SHALL process pending work directly without a worker pool or queue persistence, and exit.

### Requirement: Long-Running Navi Lifecycle

The system SHALL run a long-running navi by restoring the queue from disk, starting the worker pool, starting its interfaces, handling incoming messages by converting them to thoughts, and persisting the queue on shutdown.

#### Scenario: Incoming message handling

GIVEN a long-running navi with an active gateway
WHEN a message arrives
THEN the system SHALL convert the message to a thought with estimated priority and enqueue it on the thought queue.

### Requirement: Sub-Agent Navi Lifecycle

The system SHALL run a sub-agent navi within a ChatSession, receiving the prompt from the session, creating a short-lived session with the sub-agent's configuration, and returning the result to the session.

#### Scenario: Sub-agent execution

GIVEN a navi used as a sub-agent via type: sub
WHEN the sub-agent receives a prompt from the ChatSession
THEN the system SHALL create a short-lived session with the sub-agent's config, process the prompt, and deliver the result back to the ChatSession.

### Requirement: Per-Navi Configuration

The system SHALL allow each navi to be configured with its own provider, model, thought queue settings, interfaces, tools, and persona.

#### Scenario: Navi configuration loading

GIVEN a navi defined in navis.yaml
WHEN the navi is initialized
THEN the system SHALL load the navi's provider, model, thought_queue, interfaces, tools, and persona from its configuration.

---


## 03 Resource Configuration

### 7.1 Per-Navi Thought Queue Limits

Each navi configures its own thought queue capacity:

```yaml
navis:
  <name>:
    thought_queue:
      max_concurrent: 3      # total processing capacity
      priority_slots: 1      # reserved for priority thoughts
      slot_exhaustion: "queue" # block, reject, or queue
```

**Configuration fields:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_concurrent` | int | 1 | Total concurrent processing capacity |
| `priority_slots` | int | 0 | Slots reserved for priority thoughts |
| `slot_exhaustion` | string | "queue" | What to do when all slots are full |

### 7.2 Priority Slot Allocation

Priority slots are reserved for high-priority thoughts (priority 1-2). They ensure that critical work always has a processing slot, even when the navi is busy.

```
max_concurrent: 3
priority_slots: 1

┌─────────────────────────────────────────────┐
│  Slot 1 (reserved) → Priority thoughts      │
│  Slot 2 (normal)   → Any thought            │
│  Slot 3 (normal)   → Any thought            │
└─────────────────────────────────────────────┘
```

**Rules:**
1. Priority thoughts fill reserved slots first
2. If reserved slots are full, priority thoughts can preempt lower-priority running thoughts
3. Normal thoughts only fill non-reserved slots
4. When non-reserved slots are full, normal thoughts are blocked/rejected/queued based on `slot_exhaustion`

### 7.3 Slot Exhaustion Policies

| Policy | Behavior | Use Case |
|--------|----------|----------|
| `block` | Block the caller until a slot is available | Long-running navis (don't lose work) |
| `reject` | Return error immediately | One-off navis (fail fast) |
| `queue` | Add to unbounded pending queue | Navis that want to defer work |

### Requirement: Per-Navi Thought Queue Limits

The system SHALL allow each navi to configure its thought queue capacity with max_concurrent, priority_slots, and slot_exhaustion fields.

#### Scenario: Thought queue configuration

GIVEN a navi with thought_queue configuration
WHEN the navi initializes its queue
THEN the system SHALL apply max_concurrent as total processing capacity, priority_slots as reserved slots for high-priority thoughts, and slot_exhaustion as the policy when all slots are full.

### Requirement: Priority Slot Allocation

The system SHALL reserve priority slots for high-priority thoughts (priority 1-2), ensuring critical work always has a processing slot even when the navi is busy, and allow priority thoughts to use non-reserved slots when reserved slots are full.

#### Scenario: Priority slot reservation

GIVEN a navi with priority_slots set to 1
WHEN a priority thought (priority 1 or 2) arrives
THEN the system SHALL allocate the reserved slot to the priority thought.

### Requirement: Slot Exhaustion Policy Behavior

The system SHALL apply the configured slot exhaustion policy when all slots are occupied: block the caller, reject with an error, or queue the thought in an unbounded pending queue.

#### Scenario: Block policy behavior

GIVEN a navi with slot_exhaustion set to "block"
WHEN all slots are full
THEN the system SHALL block the caller until a slot becomes available.

#### Scenario: Reject policy behavior

GIVEN a navi with slot_exhaustion set to "reject"
WHEN all slots are full
THEN the system SHALL return an error to the caller immediately.

---


---

## 05 Binary-Run Navi (S010104)

### Requirement: Binary Run Drives the Queue In-Process

A navi run with only the TUI interface SHALL run the thought queue in-process with a foreground slot, rather than a separate one-off execution path.

#### Scenario: TUI session attached to the in-process queue

GIVEN a navi run as the plain binary with the TUI attached
WHEN the session schedules or steers thoughts
THEN the system SHALL route them through the same queue, pool, and runner surface a daemon uses, with the session's current thought in the foreground slot.

## Related


### Other Parts

- [Thoughts & Queue](./00-thought-queue.md)
- [Sessions & Implementation](./02-sessions-implementation.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S010100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-23 | S010104 | 0.2.0 | Doors-not-forks: §6.1 mapping revised — no interface maps to a separate execution model; binary run uses the in-process queue with a foreground slot (thought-loop case study) |

