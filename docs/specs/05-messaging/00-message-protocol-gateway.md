---
status: draft
created: '16'
updated: '2026-08-11'
tags:
  - spec
  - messaging
priority: medium
spec-refs: [050000, 050001]
---

# Plan: Unified Messaging System: Message Protocol & Gateway

**Scope:** Unified message protocol, gateway architecture, platform adapters, routing to ThoughtQueue (daemon navis) or ChatSession (council mode), and delivery system.

> **Part of:** [Plan: Unified Messaging System](./README.md) — split into 3 parts for readability.

## 00 Unified Message Protocol

### 1.1 Core Message Type

All messages — regardless of origin platform or destination — flow through a single `Message` type:

```go
type Message struct {
    ID        string            // unique message ID (UUID)
    Type      MessageType       // message category
    From      MessageSource     // who sent it
    To        MessageTarget     // who it's for
    Content   MessageContent    // unified content
    Metadata  map[string]any    // cross-platform metadata
    Timestamp time.Time
    ParentID  string            // reply-to reference
    SessionID string            // owning session
}

type MessageType string
const (
    MessageTypeText       MessageType = "text"        // plain message
    MessageTypeThinking   MessageType = "thinking"     // agent reasoning
    MessageTypeToolCall   MessageType = "tool_call"    // tool invocation
    MessageTypeToolResult MessageType = "tool_result"  // tool output
    MessageTypeError      MessageType = "error"        // error
    MessageTypeCouncilInvite MessageType = "council_invite" // session entered council mode
    MessageTypeCouncilMsg MessageType = "council_msg"  // council member response
    MessageTypeNotification MessageType = "notification" // system notification
    MessageTypeConfigChange MessageType = "config_change" // self-improvement notification
)

type MessageSource struct {
    Platform  string // "telegram", "discord", "webhook", "gateway_cron", "agent_internal"
    UserID    string // platform-specific user ID
    AgentID   string // agent ID (for internal messages)
    IsBot     bool   // sender is a bot
}

type MessageTarget struct {
    Platform  string           // destination platform
    UserID    string           // destination user
    ChannelID string           // destination channel
    AgentID   string           // destination agent (multi-agent routing)
    SessionID string           // destination session
    CouncilID string           // destination council session (if session is in council mode)
}
```

### 1.2 Unified Content Format

Content is platform-agnostic. Each platform's delivery adapter handles the conversion:

```go
type MessageContent struct {
    // Core content
    Text       string              // plain text
    Markdown   string              // markdown content
    HTML       string              // HTML content (for webui)

    // Rich media
    Images     []ImageRef          // embedded images
    Files      []FileRef           // attached files

    // Tool calls (for agent messages)
    ToolCalls  []ToolInvocation    // tools the agent wants to call

    // Council-specific
    Council    *CouncilMessage     // council chat data

    // Notifications
    Notification *NotificationData // system notifications
}

type ImageRef struct {
    URL     string
    Data    []byte   // base64 inline data
    MimeType string
}

type FileRef struct {
    Name    string
    Data    []byte
    MimeType string
}

type ToolInvocation struct {
    ID        string
    Name      string
    Arguments map[string]any  // JSON Schema typed arguments
}

type CouncilMessage struct {
    MemberID   string   // which council member is responding
    Mentions   []string // @mentions in this message
    IsReply    bool     // is this a reply to a previous council message
    ReplyTo    string   // council message ID being replied to
    Round      int      // which council round this belongs to
}

type NotificationData struct {
    Title   string
    Body    string
    Actions []NotificationAction // actionable buttons
    Level   string              // "info", "warning", "error"
}

type NotificationAction struct {
    Label string
    Value string // value to send back as a message
}
```

### 1.3 Message Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Message Lifecycle                            │
│                                                                     │
│  Platform Event                     Gateway Processing              │
│  ┌──────────┐                      ┌──────────────────┐            │
│  │ Adapter  │                      │ MessageRouter     │            │
│  │ receives │                      │                   │            │
│  │ message  │                      │ 1. Parse raw      │            │
│  └────┬─────┘                      │ 2. Create Message │            │
│       │                             │ 3. Resolve target  │            │
│       ▼                             │ 4. Apply hooks     │            │
│  RawPayload                       └────────┬───────────┘            │
│                                             │                       │
│                                             ▼                       │
│                                      ┌───────────────┐             │
│                                      │ ChatSession    │             │
│                                      │ (single/council)│            │
│                                      └───────┬───────┘             │
│                                              │                       │
│                    ┌─────────────────────────┼──────────┐           │
│                    ▼                         ▼          ▼           │
│              ┌─────────┐              ┌─────────┐  ┌─────────┐    │
│              │ AIAgent │              │ AIAgent │  │ AIAgent │    │
│              │ (main)  │              │ (sub)   │  │ (sub)   │    │
│              └────┬────┘              └────┬────┘  └────┬────┘    │
│                   │                         │            │          │
│                   ▼                         ▼            ▼          │
│              ┌─────────┐              ┌─────────┐  ┌─────────┐    │
│              │ Delivery│              │ Delivery│  │ Delivery│    │
│              │ Adapter │              │ Adapter │  │ Adapter │    │
│              └────┬────┘              └────┬────┘  └────┬────┘    │
│                   │                         │            │          │
│                   └─────────────────────────┴────────────┘          │
│                                             │                       │
│                                             ▼                       │
│                                      Platform-Specific             │
│                                      Response Delivery             │
└─────────────────────────────────────────────────────────────────────┘
```

### 1.4 Message Serialization

For storage and cross-process communication:

```go
type SerializedMessage struct {
    ID        string            `json:"id"`
    Type      MessageType     `json:"type"`
    From      MessageSource   `json:"from"`
    To        MessageTarget   `json:"to"`
    Content   SerializedContent `json:"content"`
    Metadata  map[string]any  `json:"metadata,omitempty"`
    Timestamp time.Time       `json:"timestamp"`
    ParentID  string          `json:"parent_id,omitempty"`
    SessionID string          `json:"session_id,omitempty"`
}

type SerializedContent struct {
    Text       string            `json:"text,omitempty"`
    Markdown   string            `json:"markdown,omitempty"`
    Images     []SerializedImage `json:"images,omitempty"`
    ToolCalls  []SerializedTool  `json:"tool_calls,omitempty"`
    Council    *SerializedCouncil `json:"council,omitempty"`
    Notification *SerializedNotification `json:"notification,omitempty"`
}
```

### Requirement: Unified Message Type Structure

The system SHALL define a single `Message` struct with identity, type, source, target, content, metadata, timestamp, parent, and session fields, serving as the universal message type for all platforms and scenarios.

#### Scenario: Message created from parsed platform payload
- GIVEN a platform adapter receives a raw payload from a user
- WHEN the gateway router parses the payload
- THEN the system SHALL create a `Message` with a unique UUID, a message type, the sender's platform and user ID, and the unified content

#### Scenario: Message carries session context
- GIVEN a message is processed by the gateway
- WHEN the router resolves the message target
- THEN the system SHALL populate the `SessionID` and `To` fields with the owning session and the destination target

### Requirement: Message Type Enumeration

The system SHALL support a `MessageType` enumeration covering text, thinking, tool_call, tool_result, error, council_invite, council_msg, notification, and config_change categories.

#### Scenario: Tool messages routed to tool subsystem
- GIVEN a message has type `MessageTypeToolCall` or `MessageTypeToolResult`
- WHEN the gateway processes the message
- THEN the system SHALL route it to the tool execution subsystem rather than user delivery

#### Scenario: Council session messages
- GIVEN a session enters council mode
- WHEN the system notifies participants
- THEN the system SHALL emit messages with type `MessageTypeCouncilInvite` for invitations
- AND `MessageTypeCouncilMsg` for council member responses

### Requirement: Platform-Agnostic Content Format

The system SHALL represent message content in a platform-agnostic `MessageContent` struct with text, markdown, HTML, images, files, tool calls, council data, and notification fields, leaving platform-specific conversion to each delivery adapter.

#### Scenario: Markdown content converted per platform
- GIVEN a message contains `Markdown` content
- WHEN a delivery adapter sends the message
- THEN the adapter SHALL convert the markdown to the platform's supported format (e.g., HTML for Telegram, mrkdwn for Slack)

#### Scenario: Notification content delivery
- GIVEN a message contains a `Notification` payload with title, body, and actions
- WHEN a delivery adapter sends the message
- THEN the adapter SHALL render the notification with its actionable buttons on the destination platform

### Requirement: Rich Media Attachments

The system SHALL support embedded images and attached files in message content, referencing either a URL or inline data with a MIME type.

#### Scenario: Image attachment delivery
- GIVEN a message contains an `ImageRef` with a URL
- WHEN the delivery adapter sends the message
- THEN the adapter SHALL upload or reference the image on the destination platform

#### Scenario: File attachment
- GIVEN a message contains a `FileRef` with a name and data
- WHEN the delivery adapter sends the message
- THEN the adapter SHALL attach the file to the outgoing platform message

### Requirement: Message Lifecycle Flow

The system SHALL process every incoming message through a defined lifecycle: the adapter receives the raw payload, the router parses and creates a `Message`, resolves the target, applies hooks, routes to the session, and delivers the response via platform adapters.

#### Scenario: End-to-end message processing
- GIVEN a platform adapter receives a user message
- WHEN the gateway processes the message
- THEN the message SHALL flow through parsing, target resolution, hook application, session routing, and delivery

#### Scenario: Message routed to chat session
- GIVEN the router resolves a target session
- WHEN the session behavior is single-agent or council mode
- THEN the system SHALL route the message to the appropriate `ChatSession` handler
- AND deliver agent responses through the matching delivery adapter

### Requirement: Platform Response Delivery

The system SHALL deliver agent responses back to the originating platform through platform-specific delivery adapters, converting unified content into the platform's message format.

#### Scenario: Response delivered to originating platform
- GIVEN an agent produces a response for a message from a platform
- WHEN the gateway completes processing
- THEN the system SHALL convert the response to the platform's format and deliver it to the original channel

#### Scenario: Multiple delivery targets
- GIVEN a message is destined for multiple platforms or channels
- WHEN the gateway delivers the message
- THEN the system SHALL deliver it through each target platform's delivery adapter

### Requirement: Message Serialization

The system SHALL serialize messages to JSON for storage and cross-process communication using the `SerializedMessage` struct, omitting empty optional fields.

#### Scenario: Message persisted to storage
- GIVEN a message is ready for storage
- WHEN the system serializes the message
- THEN the system SHALL produce a JSON document with the `SerializedMessage` structure
- AND omit empty optional fields

#### Scenario: Serialized message reconstructed
- GIVEN a serialized message is received from another process or from storage
- WHEN the system deserializes the message
- THEN the system SHALL reconstruct the `Message` struct with all serialized fields

### Requirement: Serialized Content Representation

The system SHALL serialize message content into a `SerializedContent` struct that carries the core text, markdown, images, tool calls, council, and notification fields relevant for storage and cross-process transfer.

#### Scenario: Serialized content preserves core fields
- GIVEN a message with text, markdown, images, tool calls, council data, and a notification
- WHEN the message is serialized
- THEN the `SerializedContent` SHALL preserve all present content fields

#### Scenario: Serialized content omits absent fields
- GIVEN a message contains only plain text
- WHEN the message is serialized
- THEN the system SHALL omit markdown, images, tool calls, council, and notification fields from the serialized content

---


## 01 Gateway Architecture

### 2.1 Gateway Components

```
┌─────────────────────────────────────────────────────────────────┐
│                    opennavi-gateway (daemon)                     │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                    GatewayRunner                        │    │
│  │  (core event loop, lifecycle management)                │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐         │
│  │ Platform     │  │ Platform     │  │ Platform     │  ...   │
│  │ Adapter Mgmt │  │ Adapter Mgmt │  │ Adapter Mgmt │         │
│  │ (Telegram)   │  │ (Discord)    │  │ (Slack)      │         │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘         │
│         │                  │                  │                 │
│         └──────────────────┴──────────────────┘                 │
│                            │                                    │
│                    MessageRouter                                │
│         (parse → resolve → create Message)                     │
│                            │                                    │
│                    SessionManager                               │
│         (ChatSession creation, loading, persistence)            │
│                            │                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐         │
│  │ Agent        │  │ Agent        │  │ Agent        │         │
│  │ Pool         │  │ Pool         │  │ Pool         │         │
│  │ (main + sub) │  │ (main + sub) │  │ (main + sub) │         │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘         │
│         │                  │                  │                 │
│         └──────────────────┴──────────────────┘                 │
│                            │                                    │
│                    DeliveryRegistry                             │
│         (platform-specific response delivery)                   │
│                                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
│  │ Hook     │  │ Cron     │  │ Auth     │  │ Mirror   │      │
│  │ System   │  │ Scheduler│  │ System   │  │ System   │      │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘      │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 GatewayRunner

The central event loop:

```go
type GatewayRunner struct {
    profile          string
    config           *GatewayConfig
    adapters         map[string]Adapter
    router           *MessageRouter
    sessionManager   *SessionManager
    agentPool        *AgentPool
    deliveryRegistry *DeliveryRegistry
    hookRegistry     *HookRegistry
    cronScheduler    *CronScheduler
    auth             *AuthManager
    mirror           *MirrorSystem
    stopCh           chan struct{}
    started          bool
    mu               sync.RWMutex
}

func (g *GatewayRunner) Start(ctx context.Context) error {
    // 1. Load config
    g.loadConfig()

    // 2. Initialize adapters
    for name, adapterConfig := range g.config.Adapters {
        adapter, err := g.createAdapter(name, adapterConfig)
        if err != nil {
            return err
        }
        g.adapters[name] = adapter
    }

    // 3. Start adapters (each begins listening)
    for name, adapter := range g.adapters {
        if err := adapter.Start(ctx, g.handleIncoming); err != nil {
            return fmt.Errorf("adapter %s: %w", name, err)
        }
    }

    // 4. Load existing sessions
    g.sessionManager.LoadSessions(ctx)

    // 5. Start cron scheduler
    g.cronScheduler.Start(ctx)

    // 6. Start hook system
    g.hookRegistry.Start()

    g.started = true
    return nil
}

func (g *GatewayRunner) handleIncoming(rawMsg RawMessage) {
    // 1. Parse into unified Message
    msg, err := g.router.Parse(rawMsg)
    if err != nil {
        g.logError("parse error", err)
        return
    }

    // 2. Apply auth check
    if err := g.auth.Check(msg.From); err != nil {
        g.deliverError(msg.From, "Unauthorized")
        return
    }

    // 3. Fire message_in hook
    g.hookRegistry.Fire(HookMessageIn, msg)

    // 4. Resolve target session/agent
    target, err := g.router.Resolve(msg)
    if err != nil {
        g.logError("resolve error", err)
        return
    }

    // 5. Get or create session
    session, err := g.sessionManager.GetOrCreate(target)
    if err != nil {
        g.logError("session error", err)
        return
    }

    // 6. Route to appropriate handler
    switch session.Behavior() {
    case SessionBehaviorOneToOne:
        g.routeToAgent(session, msg)
    case SessionBehaviorGroup:
        g.routeToGroup(session, msg)
    case SessionBehaviorCouncil:
        g.routeToCouncil(session, msg)
    }

    // 7. Fire message_out hook
    g.hookRegistry.Fire(HookMessageOut, msg)
}

func (g *GatewayRunner) Stop() error {
    close(g.stopCh)

    // Stop adapters
    for name, adapter := range g.adapters {
        if err := adapter.Stop(); err != nil {
            g.logError("adapter stop error", err)
        }
    }

    // Stop cron
    g.cronScheduler.Stop()

    // Flush sessions
    g.sessionManager.FlushAll()

    g.started = false
    return nil
}
```

### 2.3 MessageRouter

Handles parsing and resolution:

```go
type MessageRouter struct {
    profileConfig *ProfileConfig
    agentRegistry *AgentRegistry
    sessionManager *SessionManager
}

func (r *MessageRouter) Parse(raw RawMessage) (*Message, error) {
    // Each adapter provides a parser
    parser := raw.Adapter.Parser()
    parsed, err := parser.Parse(raw.Payload)
    if err != nil {
        return nil, err
    }

    return &Message{
        ID:        uuid.New().String(),
        Type:      MessageTypeText,
        From: MessageSource{
            Platform: raw.Adapter.Name(),
            UserID:   parsed.UserID,
            IsBot:    parsed.IsBot,
        },
        To:        MessageTarget{}, // resolved later
        Content: MessageContent{
            Text:     parsed.Text,
            Markdown: parsed.Markdown,
            Images:   parsed.Images,
        },
        Metadata:  parsed.Metadata,
        Timestamp: time.Now(),
    }, nil
}

func (r *MessageRouter) Resolve(msg *Message) (*SessionTarget, error) {
    // 1. Check for @mentions targeting specific agents
    if mentions := extractMentions(msg.Content.Text); len(mentions) > 0 {
        return r.resolveAgentTarget(msg, mentions)
    }

    // 2. Check for council commands (/council, @navi council)
    if isCouncilCommand(msg.Content.Text) {
        return r.resolveCouncilTarget(msg)
    }

    // 3. Default: resolve to user's session
    return r.resolveSessionTarget(msg)
}

func (r *MessageRouter) resolveAgentTarget(msg *Message, mentions []string) (*SessionTarget, error) {
    target := &SessionTarget{}

    // If mentioning a specific agent, route to that agent's session
    for _, mention := range mentions {
        agent, err := r.agentRegistry.Get(mention)
        if err != nil {
            continue
        }
        target.AgentID = agent.ID
        target.SessionID = r.sessionManager.GetOrCreateSession(agent.ID, msg.From)
    }

    // If no valid agent mentioned, fall through to default session
    if target.SessionID == "" {
        return r.resolveSessionTarget(msg)
    }

    return target, nil
}

func (r *MessageRouter) resolveCouncilTarget(msg *Message) (*SessionTarget, error) {
    // Parse council command: /council coder researcher
    parts := strings.Fields(msg.Content.Text)
    if len(parts) < 2 {
        return nil, fmt.Errorf("invalid council command")
    }

    agentNames := parts[1:] // skip /council
    agents := make([]*Agent, 0, len(agentNames))

    for _, name := range agentNames {
        agent, err := r.agentRegistry.Get(name)
        if err != nil {
            continue
        }
        agents = append(agents, agent)
    }

    if len(agents) == 0 {
        return nil, fmt.Errorf("no valid agents in council")
    }

    // Get or create session, then switch to council mode
    sessionID := r.sessionManager.GetOrCreateSession("", SessionKey{
        Platform:  msg.From.Platform,
        UserID:    msg.From.UserID,
        ChannelID: msg.Metadata["channel_id"].(string),
    })
    // Council mode is set when StartCouncil is called on the session

    return &SessionTarget{
        SessionID: sessionID,
        CouncilID: sessionID,
    }, nil
}

func (r *MessageRouter) resolveSessionTarget(msg *Message) (*SessionTarget, error) {
    sessionKey := SessionKey{
        Platform:  msg.From.Platform,
        UserID:    msg.From.UserID,
        ChannelID: msg.Metadata["channel_id"].(string),
    }

    sessionID := r.sessionManager.GetOrCreateSession("", sessionKey)
    return &SessionTarget{
        SessionID: sessionID,
    }, nil
}
```

### Requirement: Gateway Component Architecture

The system SHALL structure the gateway daemon around a `GatewayRunner` core event loop that coordinates platform adapter management, the `MessageRouter`, `SessionManager`, agent pool, `DeliveryRegistry`, hook system, cron scheduler, auth system, and mirror system.

#### Scenario: Gateway starts with all subsystems
- GIVEN the gateway is started
- WHEN the `GatewayRunner` initializes
- THEN the system SHALL initialize adapters, router, session manager, agent pool, delivery registry, hooks, cron, auth, and mirror subsystems

#### Scenario: Subsystem coordination
- GIVEN a message arrives at the gateway
- WHEN the `GatewayRunner` processes it
- THEN the system SHALL coordinate parsing, session resolution, agent routing, and response delivery across the subsystems

### Requirement: Adapter Lifecycle Management

The system SHALL manage the lifecycle of each platform adapter, starting each adapter to begin listening for incoming messages and stopping each adapter during shutdown.

#### Scenario: Adapter startup
- GIVEN an adapter is configured and enabled
- WHEN the gateway starts
- THEN the system SHALL create the adapter and start its listener with the incoming message handler

#### Scenario: Adapter startup failure
- GIVEN an adapter fails to start
- WHEN the gateway initializes
- THEN the system SHALL return an error naming the failed adapter

### Requirement: Gateway Startup Sequence

The system SHALL initialize the gateway in a defined order: load config, initialize adapters, start adapters, load existing sessions, start the cron scheduler, and start the hook system.

#### Scenario: Startup follows defined order
- GIVEN the gateway is starting
- WHEN initialization runs
- THEN the system SHALL load configuration first, then initialize and start adapters, load sessions, start cron, and start the hook system

#### Scenario: Startup failure aborts initialization
- GIVEN an adapter fails during startup
- WHEN the gateway initializes
- THEN the system SHALL abort startup and return the error without marking the gateway started

### Requirement: Incoming Message Processing

The system SHALL process each incoming raw message through a pipeline: parse into a unified `Message`, check authentication, fire the `message_in` hook, resolve the target, get or create the session, route to the behavior-appropriate handler, and fire the `message_out` hook.

#### Scenario: Authenticated message processed
- GIVEN a raw message passes the auth check
- WHEN the gateway handles it
- THEN the system SHALL parse it, fire the `message_in` hook, resolve the target session, route it to the matching behavior handler, and fire the `message_out` hook

#### Scenario: Unauthorized message rejected
- GIVEN a raw message fails the auth check
- WHEN the gateway handles it
- THEN the system SHALL deliver an "Unauthorized" error to the sender without routing the message

#### Scenario: Parse failure
- GIVEN a raw message cannot be parsed
- WHEN the gateway handles it
- THEN the system SHALL log the parse error and drop the message

### Requirement: Graceful Gateway Shutdown

The system SHALL stop the gateway gracefully by closing the stop channel, stopping all adapters, stopping the cron scheduler, and flushing sessions.

#### Scenario: Shutdown stops all subsystems
- GIVEN a stop signal is received
- WHEN the gateway shuts down
- THEN the system SHALL stop all adapters and the cron scheduler
- AND flush all sessions to persist their state

#### Scenario: Adapter stop error logged
- GIVEN an adapter fails to stop during shutdown
- WHEN the gateway shuts down
- THEN the system SHALL log the adapter stop error and continue shutting down remaining subsystems

### Requirement: Message Parsing into Unified Format

The system SHALL parse raw payloads from each platform adapter through the adapter's `Parser`, producing a unified `Message` with a fresh UUID, text content, source platform, and parsed metadata.

#### Scenario: Parsed payload becomes unified message
- GIVEN a raw payload from a platform adapter
- WHEN `MessageRouter.Parse` runs
- THEN the system SHALL invoke the adapter's parser and build a `Message` with a new UUID, parsed text, markdown, images, and metadata

#### Scenario: Parse error returned
- GIVEN a payload the adapter parser cannot process
- WHEN `MessageRouter.Parse` runs
- THEN the system SHALL return the parser error

### Requirement: Target Resolution Strategy

The system SHALL resolve a message's target by checking agent `@mentions` first, then council commands, and finally falling back to the user's session.

#### Scenario: Agent mention resolution
- GIVEN a message contains `@mentions` of specific agents
- WHEN the router resolves the target
- THEN the system SHALL route the message to the mentioned agents' session

#### Scenario: Council command resolution
- GIVEN a message contains a council command such as `/council`
- WHEN the router resolves the target
- THEN the system SHALL resolve the named agents into a council session target
- AND switch the session to council mode

#### Scenario: Invalid council command
- GIVEN a council command names no valid agents
- WHEN the router resolves the target
- THEN the system SHALL return an error indicating no valid agents were found

### Requirement: Session Target Fallback

The system SHALL resolve messages without mentions or council commands to a session keyed by the sender's platform, user ID, and channel ID, creating the session if it does not exist.

#### Scenario: Session resolved by sender identity
- GIVEN a message with no agent mentions or council commands
- WHEN the router resolves the target
- THEN the system SHALL get or create the session keyed by platform, user ID, and channel ID

#### Scenario: Mentioned agent unavailable
- GIVEN a message mentions agents but none resolve to a valid agent
- WHEN the router resolves the target
- THEN the system SHALL fall back to the sender's default session

---


---

## Related


### Other Parts

- [Platform Adapters & Routing](./01-platform-adapters-routing.md)
- [Hooks, Cron & Implementation](./02-hooks-cron-impl.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S050000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |