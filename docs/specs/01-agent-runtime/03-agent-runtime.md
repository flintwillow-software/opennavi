---
status: draft
created: '2026-07-29'
updated: '2026-09-23'
tags:
  - spec
  - agent-runtime
priority: high
spec-refs: [010300, 010301, 010302, 010303, 010304, 010305, 010306, 010307, 010308, 010309, 010310, 010311, 010312]
---

# Plan: Agent Runtime

**Scope:** Agent state management, tool execution, event system, steering queues, context transformation, and the core agent loop.

**Inspiration:** pi-mono's agent loop with parallel/sequential tool execution, event streaming, and steering/follow-up.

> **See also:** [P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md) for the `chat_send`, `presence_list`, `presence_discover`, `key_trust_list`, `key_trust_add`, and `key_trust_revoke` tools that extend the agent's tool set. These tools are added to the agent runtime's tool executor when the P2P chat interface is enabled.

---

## 00 Overview

The agent runtime is the core execution engine. It takes a user prompt, runs a loop of:

1. Send messages + tools to LLM
2. Parse response (text or tool calls)
3. Execute tool calls
4. Feed results back
5. Repeat until stop

It manages state, fires events, and handles steering/follow-up messages during execution.

### Requirement: Core Execution Engine

The system SHALL provide an agent runtime that takes a user prompt and runs a loop of sending messages and tools to the LLM, parsing the response, executing tool calls, and feeding results back until a stop condition is met.

#### Scenario: Prompt execution

GIVEN a user prompt
WHEN the agent runtime runs
THEN the system SHALL iterate the agent loop, exchanging messages and tool calls with the LLM, until the agent completes.

### Requirement: Runtime Responsibilities

The system SHALL have the agent runtime manage state, fire events, and handle steering and follow-up messages during execution.

#### Scenario: Runtime state management

GIVEN an active agent run
WHEN execution proceeds
THEN the system SHALL manage the agent's state, emit lifecycle events, and process incoming steering/follow-up messages.

---

## 01 Agent Types

### 2.1 Agent Message

```go
type AgentMessage struct {
    Role        Role
    Content     string
    ToolCalls   []ToolCall
    ToolResult  string
    IsStreaming bool
}
```

### 2.2 Agent Events

```go
type AgentEventType string
const (
    AgentEventStart        AgentEventType = "agent_start"
    AgentEventTurnStart    AgentEventType = "turn_start"
    AgentEventMessageStart AgentEventType = "message_start"
    AgentEventMessageEnd   AgentEventType = "message_end"
    AgentEventContentDelta AgentEventType = "content_delta"  // streaming text
    AgentEventToolCall     AgentEventType = "tool_call"
    AgentEventToolResult   AgentEventType = "tool_result"
    AgentEventTurnEnd      AgentEventType = "turn_end"
    AgentEventError        AgentEventType = "error"
    AgentEventEnd          AgentEventType = "agent_end"
)

type AgentEvent struct {
    Type    AgentEventType
    Message *AgentMessage
    Tool    *ToolUpdate
    Error   error
}

type ToolUpdate struct {
    ToolCallID string
    Name       string
    Input      map[string]any
    Result     string
    IsError    bool
}
```

### 2.3 Agent Tool

```go
type AgentTool struct {
    Name        string
    Description string
    Parameters  json.RawMessage // JSON Schema
    Execute     func(args map[string]any) (*ToolResult, error)
    Mode        ToolExecutionMode
}

type ToolExecutionMode string
const (
    ToolModeParallel   ToolExecutionMode = "parallel"
    ToolModeSequential ToolExecutionMode = "sequential"
)

type ToolResult struct {
    ToolCallID string
    Content    string
    IsError    bool
}
```

### 2.4 Agent Config

```go
type AgentConfig struct {
    Provider      string
    Model         string
    Temperature   float64
    MaxTokens     int
    Thinking      ThinkingConfig
    Tools         []AgentTool
    ToolPlugins   []string         // plugin names to load
    QueueMode     QueueMode
    ContextFiles  []ContextFile
    SystemPrompt  string
    CacheRetention int // minutes
}
```

### Requirement: Agent Message Model

The system SHALL model agent messages with role, content, tool calls, tool result, and streaming status fields.

#### Scenario: Message recording

GIVEN a message produced during an agent run
WHEN the message is recorded in the agent state
THEN the system SHALL store its role, content, tool calls, tool result, and streaming flag.

### Requirement: Agent Event Types

The system SHALL emit typed agent events covering the full lifecycle, including agent start/end, turn start/end, message start/end, content deltas, tool calls, tool results, and errors.

#### Scenario: Streaming event emission

GIVEN an LLM streaming response
WHEN content deltas arrive
THEN the system SHALL emit content_delta events carrying the incremental message.

#### Scenario: Tool call events

GIVEN a tool call requested by the LLM
WHEN the tool executes
THEN the system SHALL emit tool_call and tool_result events with the tool's name, input, result, and error status.

### Requirement: Tool Execution Modes

The system SHALL support parallel and sequential tool execution modes, configurable per agent, with parallel as the default for tools that declare parallel capability.

#### Scenario: Tool mode configuration

GIVEN an agent configuration with a tool execution mode
WHEN the agent executes tool calls
THEN the system SHALL execute the tools either in parallel or sequentially according to the configured mode.

---

## 02 Agent State

```go
type AgentState struct {
    Messages []AgentMessage
    Tools    []AgentTool
    Config   AgentConfig
    Status   AgentStatus
    Error    error
}

type AgentStatus string
const (
    StatusIdle    AgentStatus = "idle"
    StatusRunning AgentStatus = "running"
    StatusPaused  AgentStatus = "paused"
    StatusError   AgentStatus = "error"
)
```

### 3.1 Copy-on-Write Pattern

State mutations use copy-on-write to avoid race conditions:

```go
func (s *AgentState) AppendMessage(msg AgentMessage) *AgentState {
    next := *s
    next.Messages = make([]AgentMessage, len(s.Messages)+1)
    copy(next.Messages, s.Messages)
    next.Messages[len(s.Messages)] = msg
    return &next
}

func (s *AgentState) SetStatus(status AgentStatus) *AgentState {
    next := *s
    next.Status = status
    return &next
}
```

### Requirement: Copy-on-Write State Mutations

The system SHALL mutate agent state using copy-on-write, where each mutation creates a new state instance and never mutates the existing one in place, to avoid race conditions.

#### Scenario: Append message

GIVEN an agent state with existing messages
WHEN a new message is appended
THEN the system SHALL return a new state containing all previous messages plus the new message, leaving the original state unchanged.

### Requirement: Agent Status Model

The system SHALL track agent status through idle, running, paused, and error states.

#### Scenario: Status transitions

GIVEN an agent state
WHEN the agent's execution state changes
THEN the system SHALL update the status to reflect idle, running, paused, or error.

---

## 03 Event Subscriber System

```go
type EventSubscriber struct {
    fn     func(AgentEvent)
    once   bool // fire once then unsubscribe
    done   chan struct{}
}

type EventBus struct {
    subscribers []EventSubscriber
    mu          sync.RWMutex
}

func (eb *EventBus) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func()) {
    // Returns unsubscribe func
    // If once=true, channel is closed after first fire
}

func (eb *EventBus) Fire(event AgentEvent) {
    eb.mu.RLock()
    defer eb.mu.RUnlock()
    for _, sub := range eb.subscribers {
        go sub.fn(event) // fire in goroutine
    }
}
```

### 4.1 Agent End Barrier

`agent_end` fires only after all `once` subscribers have completed:

```go
type AgentEndBarrier struct {
    mu     sync.Mutex
    count  int
    doneCh chan struct{}
}

func (b *AgentEndBarrier) Add() {
    b.mu.Lock()
    b.count++
    b.mu.Unlock()
}

func (b *AgentEndBarrier) Signal() {
    b.mu.Lock()
    b.count--
    if b.count == 0 {
        close(b.doneCh)
    }
    b.mu.Unlock()
}

func (b *AgentEndBarrier) Wait() {
    <-b.doneCh
}
```

### Requirement: Event Bus Subscription

The system SHALL provide an event bus where subscribers can register to receive agent events, with support for one-shot subscriptions that fire once then unsubscribe.

#### Scenario: Subscriber registration

GIVEN a subscriber registering for agent events
WHEN an event fires
THEN the system SHALL invoke the subscriber's handler with the event.

### Requirement: Agent End Barrier

The system SHALL fire the agent_end event only after all one-shot subscribers have completed processing, using an end barrier that tracks subscriber completion.

#### Scenario: Barrier completion

GIVEN one-shot subscribers active during an agent run
WHEN the agent run ends
THEN the system SHALL wait for all one-shot subscribers to finish before firing agent_end.

### Requirement: Event Delivery Semantics

The system SHALL fire events to subscribers in goroutines so that a slow subscriber does not block event producers.

#### Scenario: Non-blocking dispatch

GIVEN an event firing
WHEN subscribers are dispatched
THEN the system SHALL dispatch each subscriber in its own goroutine without blocking the firing path.

---

## 04 Tool Execution Engine

```go
type ToolExecutor struct {
    tools       map[string]AgentTool
    mode        ToolExecutionMode
    maxParallel int          // max concurrent tool calls
    semaphore   chan struct{} // bounded channel for parallel mode
}

func (e *ToolExecutor) Execute(ctx context.Context, calls []ToolCall) []ToolResult {
    switch e.mode {
    case ToolModeParallel:
        return e.executeParallel(ctx, calls)
    case ToolModeSequential:
        return e.executeSequential(ctx, calls)
    }
}
```

### 5.1 Before/After Tool Call Hooks

```go
func (e *ToolExecutor) Execute(ctx context.Context, calls []ToolCall) []ToolResult {
    // Before hooks — fire events for each tool call
    for _, call := range calls {
        agent.FireEvent(AgentEvent{Type: AgentEventToolCall, Tool: &ToolUpdate{Name: call.Name}})
    }

    // Execute tools
    results := e.execute(ctx, calls)

    // After hooks — fire events for each result
    for i, result := range results {
        agent.FireEvent(AgentEvent{Type: AgentEventToolResult, Tool: &ToolUpdate{
            ToolCallID: calls[i].ID,
            Result:     result.Content,
            IsError:    result.IsError,
        }})
    }

    return results
}
```

### 5.2 Parallel Execution with Bounded Concurrency

```go
func (e *ToolExecutor) executeParallel(ctx context.Context, calls []ToolCall) []ToolResult {
    if e.maxParallel == 0 {
        e.maxParallel = 4 // default
    }

    e.semaphore = make(chan struct{}, e.maxParallel)
    results := make([]ToolResult, len(calls))
    var wg sync.WaitGroup

    for i, call := range calls {
        wg.Add(1)
        go func(idx int, c ToolCall) {
            defer wg.Done()

            // Bounded concurrency via semaphore
            select {
            case e.semaphore <- struct{}{}:
                defer func() { <-e.semaphore }()
            case <-ctx.Done():
                results[idx] = ToolResult{IsError: true, Content: ctx.Err().Error()}
                return
            }

            tool := e.tools[c.Name]
            result, err := tool.Execute(c.Input)
            if err != nil {
                results[idx] = ToolResult{ToolCallID: c.ID, Content: err.Error(), IsError: true}
                return
            }
            results[idx] = *result
        }(i, call)
    }

    wg.Wait()
    return results
}
```

### Requirement: Tool Executor with Bounded Parallelism

The system SHALL execute tool calls through a tool executor that supports parallel execution bounded by a semaphore with a configurable maxParallel limit, and sequential execution mode.

#### Scenario: Parallel execution with limit

GIVEN multiple tool calls in parallel mode
WHEN the executor runs them
THEN the system SHALL execute at most maxParallel tool calls concurrently, defaulting to 4 when unset.

#### Scenario: Sequential execution

GIVEN multiple tool calls in sequential mode
WHEN the executor runs them
THEN the system SHALL execute the tool calls one at a time in order.

### Requirement: Before and After Tool Call Hooks

The system SHALL fire tool_call events before each tool executes and tool_result events after each result is produced, including the tool's input, result content, and error status.

#### Scenario: Hooks during execution

GIVEN a set of tool calls being executed
WHEN the executor processes them
THEN the system SHALL fire a tool_call event for each call before execution and a tool_result event for each result after execution.

---

## 05 Steering and Follow-up Queues

```go
type QueueMode string
const (
    QueueModeOneAtATime QueueMode = "one-at-a-time" // replace last message
    QueueModeAll        QueueMode = "all"           // queue all messages
)

type SteeringQueue struct {
    mode   QueueMode
    queue  []string
    mu     sync.RWMutex
    ch     chan string  // channel for select-based consumption
}

func (q *SteeringQueue) Add(msg string) {
    q.mu.Lock()
    defer q.mu.Unlock()
    if q.mode == QueueModeOneAtATime && len(q.queue) > 0 {
        q.queue[len(q.queue)-1] = msg // replace last
    } else {
        q.queue = append(q.queue, msg)
    }
}

func (q *SteeringQueue) Next() (string, bool) {
    q.mu.Lock()
    defer q.mu.Unlock()
    if len(q.queue) == 0 {
        return "", false
    }
    msg := q.queue[0]
    q.queue = q.queue[1:]
    return msg, true
}

func (q *SteeringQueue) Chan() <-chan string {
    return q.ch
}

func (q *SteeringQueue) Clear() {
    q.mu.Lock()
    q.queue = nil
    q.mu.Unlock()
}
```

### Requirement: Steering Queue Modes

The system SHALL support two steering queue modes: one-at-a-time, which replaces the last queued message, and all, which queues every message.

#### Scenario: One-at-a-time mode

GIVEN a steering queue in one-at-a-time mode with a pending message
WHEN a new steering message arrives
THEN the system SHALL replace the last queued message with the new one.

#### Scenario: Queue-all mode

GIVEN a steering queue in all mode
WHEN steering messages arrive
THEN the system SHALL append each message to the queue in arrival order.

### Requirement: Steering Queue Operations

The system SHALL provide steering queue operations to add messages, consume the next message, expose a channel for select-based consumption, and clear the queue.

#### Scenario: Message consumption

GIVEN a steering queue with messages
WHEN the agent loop consumes the next message
THEN the system SHALL return the oldest queued message and remove it from the queue.

### Requirement: Steering and Follow-Up Queues

The system SHALL maintain separate steering and follow-up queues, where steering messages are injected during execution and follow-up messages are queued for processing after the current turn completes.

#### Scenario: Steering during execution

GIVEN an agent mid-turn
WHEN a steering message arrives
THEN the system SHALL queue the message and inject it into the agent state at a safe point in the loop.

---

## 06 Context Transformation Pipeline

```go
func transformContext(state *AgentState) []llm.Message {
    // 1. Convert agent messages to LLM messages
    messages := convertToLLM(state.Messages)

    // 2. Filter UI-only messages (tool output, notifications)
    messages = filterUIMessages(messages)

    // 3. Prepend system prompt
    if state.Config.SystemPrompt != "" {
        messages = prependSystemMessage(messages, state.Config.SystemPrompt)
    }

    // 4. Inject context files
    for _, file := range state.Config.ContextFiles {
        messages = append(messages, llm.Message{
            Role:    RoleUser,
            Content: []ContentBlock{{Type: ContentBlockTypeText, Text: fmt.Sprintf("File: %s\n```\n%s\n```", file.Path, file.Content)}},
        })
    }

    // 5. Apply provider-specific transforms
    messages = llm.TransformMessages(messages, state.Config.Provider)

    return messages
}
```

### Requirement: Context Transformation Pipeline

The system SHALL transform agent state into LLM messages through a pipeline that converts agent messages to LLM messages, filters UI-only messages, prepends the system prompt, injects context files, and applies provider-specific transforms.

#### Scenario: System prompt injection

GIVEN an agent with a configured system prompt
WHEN the context is transformed
THEN the system SHALL prepend the system prompt to the LLM messages.

#### Scenario: Context file injection

GIVEN an agent with configured context files
WHEN the context is transformed
THEN the system SHALL inject each context file's path and content as a user message.

### Requirement: Provider-Specific Message Transformation

The system SHALL apply provider-specific message transformations as the final step of the context pipeline.

#### Scenario: Provider transform

GIVEN LLM messages for a specific provider
WHEN the context pipeline completes
THEN the system SHALL apply the provider's message transformation before sending to the LLM.

---

## 07 Agent Loop (Core Execution Engine)

```go
func (a *Agent) runLoop(ctx context.Context) {
    for {
        // Check for steering messages (user input during execution)
        if msg, ok := a.steeringQueue.Next(); ok {
            a.state = a.state.AppendMessage(AgentMessage{Role: RoleUser, Content: msg})
        }

        // Transform context to LLM format
        messages := transformContext(a.state)

        // Send to LLM provider
        events, err := a.provider.Stream(ctx, messages, a.getTools(), a.getConfig())
        if err != nil {
            a.FireEvent(AgentEvent{Type: AgentEventError, Error: err})
            a.state = a.state.SetStatus(StatusError)
            return
        }

        // Process stream events
        a.FireEvent(AgentEvent{Type: AgentEventTurnStart})

        var fullMessage AgentMessage
        for event := range events {
            switch event.Type {
            case StreamEventMessageStart:
                a.FireEvent(AgentEvent{Type: AgentEventMessageStart})

            case StreamEventContentDelta:
                fullMessage.Content += event.Content
                fullMessage.IsStreaming = true
                a.FireEvent(AgentEvent{Type: AgentEventContentDelta, Message: &fullMessage})

            case StreamEventToolCallStart:
                fullMessage.ToolCalls = append(fullMessage.ToolCalls, event.Tool)
                a.FireEvent(AgentEvent{Type: AgentEventToolCall, Tool: event.Tool})

            case StreamEventToolCallEnd:
                // Tool call complete — no additional action

            case StreamEventError:
                a.FireEvent(AgentEvent{Type: AgentEventError, Error: event.Error})
                a.state = a.state.SetStatus(StatusError)
                return

            case StreamEventStop:
                a.state = a.state.AppendMessage(fullMessage)
                a.state = a.state.SetStatus(StatusIdle)
                a.FireEvent(AgentEvent{Type: AgentEventTurnEnd})
                a.FireEvent(AgentEvent{Type: AgentEventEnd})
                return
            }
        }

        // Execute tool calls if present
        if len(fullMessage.ToolCalls) > 0 {
            results := a.executor.Execute(ctx, fullMessage.ToolCalls)
            for _, result := range results {
                a.state = a.state.AppendMessage(AgentMessage{
                    Role: RoleTool,
                    ToolResult: result.Content,
                })
            }
        }

        // Check for stop conditions
        if shouldStop(a.state) {
            return
        }
    }
}
```

### Requirement: Agent Loop Iteration

The system SHALL run a core agent loop that checks for steering messages, transforms context, streams LLM responses, processes stream events, executes tool calls, and checks stop conditions until the agent stops.

#### Scenario: Loop with tool execution

GIVEN an LLM response containing tool calls
WHEN the agent loop processes the turn
THEN the system SHALL execute the tool calls and append the tool results as tool-role messages to the agent state.

### Requirement: Stream Event Handling

The system SHALL process LLM stream events, emitting agent events for message start, content deltas, tool call start, errors, and stop, and building the full message from streaming deltas.

#### Scenario: Streaming message assembly

GIVEN a streaming LLM response
WHEN content deltas arrive
THEN the system SHALL accumulate the deltas into the full message and emit content_delta events.

#### Scenario: Stream error handling

GIVEN an error event during streaming
WHEN the agent loop receives it
THEN the system SHALL emit an error agent event, set the agent status to error, and terminate the loop.

### Requirement: Stop Condition Handling

GIVEN the agent loop reaching a stop condition
WHEN the LLM emits a stop event or the state meets the stop criteria
THEN the system SHALL append the full message to state, set status to idle, and emit turn_end and agent_end events.

#### Scenario: Turn completion

GIVEN the LLM emits a stop event
WHEN the agent loop processes it
THEN the system SHALL append the assembled message to the agent state, set status to idle, and emit turn_end and agent_end events.

---

## 08 Agent Public API

```go
type Agent struct {
    state         *AgentState
    provider      llm.Provider
    executor      *ToolExecutor
    eventBus      *EventBus
    steeringQueue *SteeringQueue
    followUpQueue  *SteeringQueue
    barrier       *AgentEndBarrier
}

func NewAgent(config AgentConfig) *Agent

func (a *Agent) Prompt(text string, images []ImageContent) error
func (a *Agent) Continue() error
func (a *Agent) Abort()
func (a *Agent) WaitForIdle()
func (a *Agent) Subscribe(fn func(AgentEvent), once bool) (unsubscribe func())
func (a *Agent) Reset()
func (a *Agent) Steer(text string)
func (a *Agent) FollowUp(text string)
func (a *Agent) ClearQueues()
func (a *Agent) State() *AgentState
```

### Requirement: Agent Public API

The system SHALL expose a public agent API providing Prompt, Continue, Abort, WaitForIdle, Subscribe, Reset, Steer, FollowUp, ClearQueues, and State operations.

#### Scenario: Prompt and continuation

GIVEN a created agent
WHEN the caller invokes Prompt
THEN the system SHALL start processing the prompt, and Continue SHALL resume processing from the current state.

#### Scenario: Steering injection

GIVEN a running agent
WHEN the caller invokes Steer
THEN the system SHALL queue the steering message for injection into the running turn.

#### Scenario: Agent state access

GIVEN an agent
WHEN the caller invokes State
THEN the system SHALL return the agent's current state.

### Requirement: Agent Lifecycle Control

The system SHALL provide lifecycle control operations that abort a running agent, wait for the agent to become idle, reset the agent to a clean state, and clear pending steering and follow-up queues.

#### Scenario: Abort and reset

GIVEN a running agent
WHEN the caller invokes Abort or Reset
THEN the system SHALL terminate the current run (Abort) or clear the agent state back to a clean state (Reset).

#### Scenario: Wait for idle

GIVEN an active agent run
WHEN the caller invokes WaitForIdle
THEN the system SHALL block until the agent completes its current run and becomes idle.

#### Scenario: Queue clearing

GIVEN pending steering and follow-up messages
WHEN the caller invokes ClearQueues
THEN the system SHALL discard all pending queued messages.

---

## 09 File Structure

```
internal/agent/
├── agent.go       # Agent struct, NewAgent, public API
├── state.go       # AgentState, copy-on-write mutations
├── events.go      # Event types, EventBus, AgentEndBarrier
├── loop.go        # Core execution loop
├── tools.go       # ToolExecutor, before/after hooks
├── queue.go       # SteeringQueue, FollowUpQueue
├── transform.go   # Context transformation pipeline
└── types.go       # AgentMessage, AgentEvent, AgentTool, AgentConfig
```

> **Implementation note:** the layout above is the original sketch. The
> actual package deviates cosmetically (no `state.go`; executor split out
> of `tools.go`; `compaction.go` and `provider.go` added). The authoritative
> mapping and rationale live in `internal/agent/README.md` (#217, NAVI-259).

---

## 10 Implementation Order

1. **Core types** — AgentMessage, AgentEvent, AgentTool, AgentConfig
2. **Agent state** — copy-on-write state management
3. **Event system** — EventBus, AgentEndBarrier
4. **Tool executor** — parallel/sequential execution with hooks
5. **Steering queues** — steering and follow-up message queues
6. **Context transform** — transformContext pipeline
7. **Agent loop** — core execution engine
8. **Public API** — NewAgent, Prompt, Continue, Abort, Subscribe, etc.

---

## 11 Key Design Decisions

1. **Copy-on-write state** — Avoids race conditions without mutexes on the hot path.
2. **Event-driven architecture** — All UI layers (TUI, WebUI, gateway) subscribe to the same events.
3. **Parallel tool execution** — Default mode; bounded by semaphore to prevent runaway concurrency.
4. **Steering during execution** — User can send messages while agent is running; queued and injected at safe points.
5. **Follow-up queue** — Messages queued for after the current turn completes.
6. **Agent end barrier** — Ensures all subscribers complete before `agent_end` fires.

## 12 Queue-Level Message Routing (S010312)

The agent-level steering queue (§05) carries messages into a running agent. The queue-level half of message routing is how humans and machines talk to *thoughts* — routed by thought ID at the queue layer, observable by portal clients (S000406):

### Requirement: Thought-Routed Messages

The system SHALL route messages to a specific thought by its ID, with delivery semantics per the thought's lifecycle state:

| Thought state | Message delivery |
|---------------|------------------|
| pending / queued | append the message to the thought's `Context` |
| running | inject via the agent steering queue (S010310, turn-boundary) |
| paused | resume the thought and attach the message |
| complete / cancelled | rejected — the thought is terminal |
| failed | rejected unless the intent is retry (S010002 retry path) |

#### Scenario: Message to a queued thought

GIVEN a thought in pending state and a message addressed to it
WHEN the message is delivered
THEN the system SHALL append the message to the thought's Context and emit a queue-level delivery event.

#### Scenario: Message to a paused thought

GIVEN a paused thought with a checkpoint
WHEN a message is addressed to it
THEN the system SHALL resume the thought (paused → pending) with the message attached to its context.

### Requirement: Delivery Acknowledgment

Every routed message SHALL emit a queue-level acknowledgment event identifying the thought, the delivery path (context-append or steering), and the outcome — so portal clients can display "delivered to thought X" or a rejection.

#### Scenario: Ack surfaces to the portal

GIVEN a message routed to a running thought
WHEN the steering queue accepts it at a turn boundary
THEN the system SHALL emit a delivery-ack queue event with the thought id and injection point.

> The in-agent steering mechanics are unchanged (§05); this subfeature specifies the queue-side routing, lifecycle-dependent semantics, and observability.

## 13 Provider/Model Per Thought (S010104)

### Requirement: Per-Thought Provider/Model Override

The system SHALL let a thought carry an optional provider + model override, resolved by the runner when it builds the per-thought agent: thought-level override → runtime primary (+ its existing fallback chain when no override is set). The runtime provider config is the fallback source, not the only source — a cheap fast model for routine thoughts, a strong model for planning.

#### Scenario: Override provider

- **GIVEN** a thought with a `provider` override
- **WHEN** the runner builds the per-thought agent
- **THEN** the system SHALL construct that provider (same construction path as the runtime primary) and use it for the run
- **AND** a thought-level `model` override, when set, SHALL override the runtime model for that run

#### Scenario: No override keeps the runtime chain

- **GIVEN** a thought without provider/model overrides
- **WHEN** the runner builds the per-thought agent
- **THEN** the system SHALL use the runtime primary provider / fallback chain and the runtime default model — behavior unchanged

#### Scenario: Unknown override provider fails loudly

- **GIVEN** a thought with a `provider` name the runtime cannot construct (unknown provider, bad endpoint config)
- **WHEN** the runner resolves it
- **THEN** the thought SHALL fail with a clear error naming the provider
- **AND** the system SHALL NOT silently fall back to the runtime primary for that thought (the fallback chain applies only when no override is set)

#### Scenario: Override survives restart

- **GIVEN** a thought with provider/model overrides
- **WHEN** the daemon restarts and crash recovery reloads it
- **THEN** the overrides SHALL be preserved (durable, S010002) and applied on re-acquisition

> S010104 delta (provider/model per thought): thought-loop follow-up batch.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S010300 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-23 | S010312 | 0.2.0 | Queue-level message routing: messages routed by thought id with lifecycle-dependent delivery (context append / steering / resume-with-message) and delivery-ack events (thought-loop case study) |
| 2026-09-23 | S010104 | 0.2.0 | Provider/model per thought: optional durable overrides resolved by the runner (override → runtime primary/fallback); unknown override providers fail the thought with a clear error, no silent fallback (thought-loop follow-up batch) |