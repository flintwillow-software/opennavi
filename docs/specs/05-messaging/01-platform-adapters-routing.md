---
status: draft
created: '16'
updated: '2026-08-11'
tags:
  - spec
  - messaging
priority: medium
spec-refs: [050100, 050101, 050102]
---

# Plan: Unified Messaging System: Platform Adapters & Routing

**Scope:** Unified message protocol, gateway architecture, platform adapters, routing to ThoughtQueue (daemon navis) or ChatSession (council mode), and delivery system.

> **Part of:** [Plan: Unified Messaging System](./README.md) — split into 3 parts for readability.

## 00 Platform Adapters

### 3.1 Adapter Interface (Expanded)

```go
type Adapter interface {
    Name() string
    Start(ctx context.Context, handler MessageHandler) error
    Stop() error
    Deliver(sessionKey SessionKey, msg Message) error
    DeliverStreaming(sessionKey SessionKey, events <-chan StreamEvent) error
    Parser() MessageParser
}

type MessageParser interface {
    Parse(payload []byte) (*ParsedMessage, error)
}

type ParsedMessage struct {
    UserID    string
    Text      string
    Markdown  string
    Images    []ImageRef
    Metadata  map[string]any
    IsBot     bool
    ChannelID string
}

type MessageHandler func(RawMessage) error

type RawMessage struct {
    Adapter  Adapter
    Payload  []byte
    Metadata map[string]any
}
```

### 3.2 Platform-Specific Message Format → Unified Message

Each adapter implements `Parser` to convert platform-specific formats to our unified `Message`:

```go
// Telegram
type TelegramParser struct{}

func (p *TelegramParser) Parse(payload []byte) (*ParsedMessage, error) {
    var update struct {
        Message struct {
            From struct {
                ID    int64  `json:"id"`
                IsBot bool   `json:"is_bot"`
            } `json:"from"`
            Chat struct {
                ID int64 `json:"id"`
            } `json:"chat"`
            Text    string  `json:"text"`
            Entities []struct {
                Type string `json:"type"`
                Offset int  `json:"offset"`
                Length int  `json:"length"`
            } `json:"entities"`
            Photo []struct {
                FileID string `json:"file_id"`
            } `json:"photo"`
        } `json:"message"`
    }
    json.Unmarshal(payload, &update)

    return &ParsedMessage{
        UserID:    fmt.Sprintf("%d", update.Message.From.ID),
        Text:      update.Message.Text,
        IsBot:     update.Message.From.IsBot,
        ChannelID: fmt.Sprintf("%d", update.Message.Chat.ID),
        Metadata:  map[string]any{"update_id": ...},
    }, nil
}

// Discord
type DiscordParser struct{}

func (p *DiscordParser) Parse(payload []byte) (*ParsedMessage, error) {
    var msg struct {
        Type    int    `json:"type"`
        Author  struct {
            ID    string `json:"id"`
            Bot   bool   `json:"bot"`
        } `json:"author"`
        ChannelID string `json:"channel_id"`
        Content   string `json:"content"`
        Attachments []struct {
            URL    string `json:"url"`
            Filename string `json:"filename"`
        } `json:"attachments"`
    }
    json.Unmarshal(payload, &msg)

    // Discord message types: 0=regular, 19=bot command, etc.
    return &ParsedMessage{
        UserID:    msg.Author.ID,
        Text:      msg.Content,
        IsBot:     msg.Author.Bot,
        ChannelID: msg.ChannelID,
        Metadata:  map[string]any{"message_type": msg.Type},
    }, nil
}

// Slack
type SlackParser struct{}

func (p *SlackParser) Parse(payload []byte) (*ParsedMessage, error) {
    var event struct {
        Type     string `json:"type"`
        User     string `json:"user"`
        Channel  string `json:"channel"`
        Text     string `json:"text"`
        BotID    string `json:"bot_id"`
        Team     string `json:"team"`
    }
    json.Unmarshal(payload, &event)

    return &ParsedMessage{
        UserID:    event.User,
        Text:      event.Text,
        IsBot:     event.BotID != "",
        ChannelID: event.Channel,
        Metadata:  map[string]any{"team": event.Team, "event_type": event.Type},
    }, nil
}

// Webhook
type WebhookParser struct{}

func (p *WebhookParser) Parse(payload []byte) (*ParsedMessage, error) {
    var req struct {
        User     string `json:"user"`
        Message  string `json:"message"`
        Platform string `json:"platform"`
    }
    json.Unmarshal(payload, &req)

    return &ParsedMessage{
        UserID:    req.User,
        Text:      req.Message,
        IsBot:     false,
        ChannelID: "",
        Metadata:  map[string]any{"platform": req.Platform},
    }, nil
}
```

### 3.3 Delivery Adapters

Each platform needs a delivery adapter to convert our `Message` back to platform format:

```go
type DeliveryAdapter interface {
    SendText(sessionKey SessionKey, text string) error
    SendMarkdown(sessionKey SessionKey, markdown string) error
    SendStream(sessionKey SessionKey, events <-chan StreamEvent) error
    SendError(sessionKey SessionKey, err error) error
    SendImage(sessionKey SessionKey, image ImageRef) error
    SendFile(sessionKey SessionKey, file FileRef) error
    SendNotification(sessionKey SessionKey, notif NotificationData) error
}

type DeliveryRegistry struct {
    adapters map[string]DeliveryAdapter
}

func (r *DeliveryRegistry) Deliver(msg Message) error {
    adapter, ok := r.adapters[msg.To.Platform]
    if !ok {
        return fmt.Errorf("no delivery adapter for platform %s", msg.To.Platform)
    }

    content := msg.Content

    switch {
    case content.Markdown != "":
        return adapter.SendMarkdown(toSessionKey(msg.To), content.Markdown)
    case content.Text != "":
        return adapter.SendText(toSessionKey(msg.To), content.Text)
    case content.Notification != nil:
        return adapter.SendNotification(toSessionKey(msg.To), *content.Notification)
    case content.ToolCalls != nil:
        // Stream tool calls as they execute
        return nil // handled by streaming
    default:
        return adapter.SendText(toSessionKey(msg.To), "No content")
    }
}

// Telegram delivery
type TelegramDelivery struct {
    bot        *tgbotapi.BotAPI
    chatCache  map[string]int64
    mu         sync.RWMutex
}

func (d *TelegramDelivery) SendMarkdown(key SessionKey, markdown string) error {
    chatID := d.resolveChatID(key)
    // Telegram uses MarkdownV2 or HTML
    return d.bot.Send(tgbotapi.NewHTMLMessage(chatID, markdown))
}

func (d *TelegramDelivery) SendStream(key SessionKey, events <-chan StreamEvent) error {
    chatID := d.resolveChatID(key)
    // Telegram requires max 4096 chars per message
    // Use a typewriter pattern: send initial message, then edit as content grows
    msg, _ := d.bot.Send(tgbotapi.NewMessage(chatID, "..."))
    return d.typewriterEdit(msg.Chat.ID, msg.MessageID, events)
}

// Discord delivery
type DiscordDelivery struct {
    session    *discordgo.Session
    channelCache map[string]string
}

func (d *DiscordDelivery) SendMarkdown(key SessionKey, markdown string) error {
    channelID := d.resolveChannelID(key)
    // Discord has 2000 char limit per message
    for _, chunk := range splitByChunk(markdown, 2000) {
        _, err := d.session.ChannelMessageSend(channelID, chunk)
        if err != nil {
            return err
        }
    }
    return nil
}

func (d *DiscordDelivery) SendStream(key SessionKey, events <-chan StreamEvent) error {
    channelID := d.resolveChannelID(key)
    // Discord: send initial message, then edit (up to 5 edits/min)
    msg, _ := d.session.ChannelMessageSend(channelID, "...")
    return d.editStream(channelID, msg.ID, events)
}

// Slack delivery
type SlackDelivery struct {
    client *slack.Client
}

func (d *SlackDelivery) SendMarkdown(key SessionKey, markdown string) error {
    channelID := d.resolveChannelID(key)
    // Slack uses mrkdwn format
    _, _, err := d.client.PostMessage(channelID, slack.MsgOptionText(markdown, true))
    return err
}

// Webhook delivery
type WebhookDelivery struct {
    baseURL string
}

func (d *WebhookDelivery) SendText(key SessionKey, text string) error {
    resp, err := http.Post(
        fmt.Sprintf("%s/deliver/%s", d.baseURL, key.UserID),
        "application/json",
        strings.NewReader(`{"message": `+jsonEncode(text)+`}`),
    )
    return err
}
```

### Requirement: Adapter Interface Contract

The system SHALL define a common `Adapter` interface that every platform adapter implements, providing name, start, stop, delivery, streaming delivery, and parser capabilities.

#### Scenario: Adapter started with handler
- GIVEN an adapter is configured and enabled
- WHEN the gateway starts the adapter
- THEN the system SHALL call `Start` with the gateway's message handler
- AND the adapter SHALL deliver incoming platform events to the handler as `RawMessage`

#### Scenario: Adapter stopped
- GIVEN the gateway is shutting down
- WHEN the gateway calls `Stop` on each adapter
- THEN the system SHALL terminate the adapter's listener and release its platform connection

### Requirement: Parser Contract

The system SHALL require each platform adapter to provide a `Parser` that converts a raw payload into a `ParsedMessage` with user ID, text, markdown, images, metadata, bot flag, and channel ID.

#### Scenario: Payload parsed into ParsedMessage
- GIVEN a raw platform payload arrives
- WHEN the adapter's parser processes it
- THEN the system SHALL produce a `ParsedMessage` with the sender's user ID, message text, channel ID, and metadata

#### Scenario: Parse failure returned
- GIVEN a payload the parser cannot decode
- WHEN the adapter's parser processes it
- THEN the system SHALL return an error

### Requirement: Platform-Specific Message Normalization

The system SHALL convert each platform's native message format into the unified `ParsedMessage` via a platform-specific parser for Telegram, Discord, Slack, and webhook payloads.

#### Scenario: Telegram update normalized
- GIVEN a Telegram `update` payload with a message
- WHEN the `TelegramParser` parses it
- THEN the system SHALL extract the sender ID, chat ID as channel, text, bot flag, and photo file IDs

#### Scenario: Discord event normalized
- GIVEN a Discord message event payload
- WHEN the `DiscordParser` parses it
- THEN the system SHALL extract the author ID, channel ID, content, attachments, and message type metadata

#### Scenario: Slack event normalized
- GIVEN a Slack event payload
- WHEN the `SlackParser` parses it
- THEN the system SHALL extract the user ID, channel, text, bot ID, and team metadata
- AND set `IsBot` when a bot ID is present

#### Scenario: Webhook request normalized
- GIVEN a webhook HTTP request body
- WHEN the `WebhookParser` parses it
- THEN the system SHALL extract the user, message, and platform fields

### Requirement: Bot Message Identification

The system SHALL mark messages sent by bots in `ParsedMessage.IsBot`, using each platform's bot indicator (Telegram `is_bot`, Discord `author.bot`, Slack `bot_id`).

#### Scenario: Bot message flagged
- GIVEN a platform message whose sender is a bot
- WHEN the parser normalizes the message
- THEN the system SHALL set `IsBot` to true

#### Scenario: Human message not flagged
- GIVEN a platform message whose sender is a human
- WHEN the parser normalizes the message
- THEN the system SHALL set `IsBot` to false

### Requirement: Delivery Adapter Interface

The system SHALL define a `DeliveryAdapter` interface with send operations for text, markdown, streaming events, errors, images, files, and notifications, and register them in a `DeliveryRegistry` keyed by platform.

#### Scenario: Delivery adapter registered per platform
- GIVEN the gateway is configured with multiple platforms
- WHEN the delivery registry is built
- THEN the system SHALL register a delivery adapter for each platform

#### Scenario: Unregistered platform delivery fails
- GIVEN a message targets a platform with no delivery adapter
- WHEN `DeliveryRegistry.Deliver` runs
- THEN the system SHALL return an error stating no delivery adapter exists for the platform

### Requirement: Content-Type Based Delivery Selection

The system SHALL select the delivery method based on message content: markdown preferred over text, notifications rendered natively, tool calls streamed, and empty content sent as a fallback message.

#### Scenario: Markdown delivery preferred
- GIVEN a message has both `Markdown` and `Text` content
- WHEN the delivery registry delivers it
- THEN the system SHALL send the markdown via the adapter's `SendMarkdown`

#### Scenario: Notification delivered natively
- GIVEN a message contains a notification
- WHEN the delivery registry delivers it
- THEN the system SHALL send the notification via the adapter's `SendNotification`

#### Scenario: Tool calls streamed
- GIVEN a message contains tool calls
- WHEN the delivery registry delivers it
- THEN the system SHALL leave delivery to the streaming mechanism rather than sending static text

#### Scenario: Empty content fallback
- GIVEN a message has no recognized content
- WHEN the delivery registry delivers it
- THEN the system SHALL send a "No content" text message

### Requirement: Platform Content Limits Handling

The system SHALL respect each platform's message length limits during delivery, splitting long content and using edit-based streaming where required (Telegram 4096 chars, Discord 2000 chars).

#### Scenario: Discord markdown chunked
- GIVEN a markdown message exceeds 2000 characters on Discord
- WHEN the Discord delivery adapter sends it
- THEN the system SHALL split the content into chunks of at most 2000 characters and send each chunk

#### Scenario: Streaming via typewriter edits
- GIVEN a streaming event channel for a long-running response
- WHEN the Telegram or Discord delivery adapter streams it
- THEN the system SHALL send an initial placeholder message
- AND edit the message as content grows, sending overflow as new messages

---


## 01 Multi-Agent Routing in Gateway

### 4.1 Routing Decision

The gateway routes incoming messages to either **ThoughtQueue** (for daemon navis) or **ChatSession** (for council mode):

```go
func (pool *AgentPool) RouteToAgent(sessionID string, msg *Message) error {
    session, err := pool.GetOrCreate(sessionID)
    if err != nil {
        return err
    }

    switch session.Behavior() {
    case SessionBehaviorOneToOne:
        // Daemon navi: enqueue on thought queue
        return pool.routeToThoughtQueue(session, msg)
    case SessionBehaviorCouncil:
        // Council mode: route within ChatSession
        return pool.routeToCouncil(session, msg)
    case SessionBehaviorGroup:
        // Group chat: route to main agent
        return pool.routeToAgent(session, msg)
    }
}
```

### 4.2 Daemon Navi Routing (ThoughtQueue)

For long-running navis with gateway/cron interfaces, messages are enqueued on the thought queue:

```go
func (pool *AgentPool) routeToThoughtQueue(session *AgentSession, msg *Message) error {
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   msg.Content.Text,
        Priority: pool.estimatePriority(msg),
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", session.Profile, uuid.New().String()),
    }

    // Enqueue on the navi's thought queue
    return session.Navi.Queue.Enqueue(thought)
}
```

### 4.3 Council Mode Routing (ChatSession)

For council mode, messages are routed within the ChatSession:

```go
func (pool *AgentPool) routeToCouncil(session *ChatSession, msg *Message) error {
    council := session.CouncilState

    if council.Round == 0 {
        // Session not yet in council mode — start it
        council = CouncilState{
            Mode:      CouncilDiscussion,
            Round:     1,
            Question:  msg.Content.Text,
            Responses: make(map[string]string),
        }
        session.Mode = SessionCouncil
        session.CouncilState = council
    }

    // Check if this is a council member responding
    if isCouncilMember(msg.From.UserID, session.Participants) {
        return pool.handleCouncilResponse(session, msg)
    }

    // Check if this is the main agent continuing the council
    if msg.From.AgentID == council.MainAgent.ID {
        return pool.handleCouncilContinuation(session, msg)
    }

    // If no council is active, create one
    if !council.IsComplete() {
        council.Round++
        council.Question = msg.Content.Text
        return pool.startCouncilRound(session)
    }

    // Council is complete, deliver a summary
    summary := council.Synthesize()
    return pool.deliverResponse(session.Key, summary)
}
```

### 4.4 Agent Pool (for non-queue navis)

For one-off navis and non-queue routing:

```go
type AgentPool struct {
    profile  string
    registry *AgentRegistry
    sessions map[string]*AgentSession // sessionID → AgentSession
    mu       sync.RWMutex
}

type AgentSession struct {
    SessionID string
    Agent     *Agent       // the main agent for this session
    Council   *CouncilState // if council mode
    Status    AgentStatus
    LastActive time.Time
}

func (p *AgentPool) GetOrCreate(sessionID string, agentConfig *AgentConfig) (*AgentSession, error) {
    p.mu.RLock()
    if session, ok := p.sessions[sessionID]; ok {
        p.mu.RUnlock()
        return session, nil
    }
    p.mu.RUnlock()

    p.mu.Lock()
    defer p.mu.Unlock()

    // Double-check after acquiring lock
    if session, ok := p.sessions[sessionID]; ok {
        return session, nil
    }

    // Create new agent
    agent := agent.NewAgent(agentConfig)
    session := &AgentSession{
        SessionID: sessionID,
        Agent:     agent,
        Status:    StatusIdle,
        LastActive: time.Now(),
    }
    p.sessions[sessionID] = session
    return session, nil
}

func (p *AgentPool) RouteToAgent(sessionID string, msg *Message) error {
    session, err := p.GetOrCreate(sessionID, nil)
    if err != nil {
        return err
    }

    // Check if agent is already running
    if session.Status == StatusRunning {
        // Queue the message as steering/follow-up
        session.Agent.Steer(msg.Content.Text)
        return nil
    }

    // Reset agent state
    session.Status = StatusRunning
    session.LastActive = time.Now()

    // Run agent in goroutine
    go func() {
        // Subscribe to agent events
        session.Agent.Subscribe(func(event AgentEvent) {
            switch event.Type {
            case AgentEventMessageStart, AgentEventContentDelta:
                // Stream partial content to platform
                p.deliverStreaming(sessionID, event)

            case AgentEventToolCall:
                // Deliver tool call as a message on the platform
                p.deliverToolCall(sessionID, event.Tool)

            case AgentEventToolResult:
                // Deliver tool result as a message
                p.deliverToolResult(sessionID, event.Tool)

            case AgentEventError:
                p.deliverError(sessionID, event.Error)
                session.Status = StatusError

            case AgentEventEnd:
                session.Status = StatusIdle
            }
        })

        // Run the agent
        output, err := session.Agent.Prompt(msg.Content.Text)
        if err != nil {
            p.deliverError(sessionID, err)
            session.Status = StatusError
            return
        }

        // Deliver final response
        p.deliverResponse(sessionID, output)
        session.Status = StatusIdle
    }()

    return nil
}
```

### 4.2 Council Routing

```go
func (g *GatewayRunner) routeToCouncil(session *ChatSession, msg *Message) error {
    council := session.CouncilState

    if council.Round == 0 {
        // Session not yet in council mode — start it
        council = CouncilState{
            Mode:      CouncilDiscussion,
            Round:     1,
            Question:  msg.Content.Text,
            Responses: make(map[string]string),
        }
        session.Mode = SessionCouncil
        session.CouncilState = council
    }

    // Check if this is a council member responding
    if isCouncilMember(msg.From.UserID, session.Participants) {
        return g.handleCouncilResponse(session, msg)
    }

    // Check if this is the main agent continuing the council
    if msg.From.AgentID == council.MainAgent.ID {
        return g.handleCouncilContinuation(session, msg)
    }

    // If no council is active, create one
    if !council.IsComplete() {
        // This message is a continuation of the council
        council.Round++
        council.Question = msg.Content.Text
        return g.startCouncilRound(session)
    }

    // Council is complete, deliver a summary
    summary := council.Synthesize()
    return g.deliverResponse(session.Key, summary)
}

func (g *GatewayRunner) handleCouncilResponse(session *ChatSession, msg *Message) error {
    council := session.Council
    councilResponses[msg.From.UserID] = msg.Content.Text

    // Deliver the response to all platforms the members are on
    for _, member := range council.Members {
        for _, platform := range member.DeliveryPlatforms {
            g.deliverMessage(Message{
                Type:    MessageTypeCouncilMsg,
                From:    msg.From,
                To:      MessageTarget{Platform: platform, ChannelID: session.Key.ChannelID},
                Content: MessageContent{Markdown: msg.Content.Markdown},
                Council: &CouncilMessage{
                    MemberID: msg.From.UserID,
                    Round:    council.Round,
                },
            })
        }
    }

    // Check if all members have responded
    if council.IsComplete() {
        // Synthesize and deliver summary
        summary := council.Synthesize()
        g.deliverResponse(session.Key, summary)
    }

    return nil
}

func (g *GatewayRunner) startCouncilRound(session *ChatSession) error {
    council := session.Council
    council.Round++
    council.Responses = make(map[string]string)

    // Send question to each council member on their platform
    for _, member := range council.Members {
        for _, platform := range member.DeliveryPlatforms {
            g.deliverMessage(Message{
                Type: MessageTypeCouncilInvite,
                From: MessageSource{AgentID: council.MainAgent.ID},
                To:   MessageTarget{Platform: platform, UserID: member.PlatformUserID},
                Content: MessageContent{
                    Text: fmt.Sprintf("Council round %d: %s", council.Round, council.Question),
                },
                Council: &CouncilMessage{
                    MemberID: member.ID,
                    Round:    council.Round,
                },
            })
        }
    }

    return nil
}
```

### Requirement: Behavior-Based Routing Decision

The system SHALL route incoming messages based on the resolved session's behavior: one-to-one sessions enqueue on the daemon navi's thought queue, council sessions route within the ChatSession, and group sessions route to the main agent.

#### Scenario: One-to-one session routed to thought queue
- GIVEN a session with `SessionBehaviorOneToOne`
- WHEN the agent pool routes a message
- THEN the system SHALL enqueue the message on the daemon navi's thought queue

#### Scenario: Council session routed within ChatSession
- GIVEN a session with `SessionBehaviorCouncil`
- WHEN the agent pool routes a message
- THEN the system SHALL route the message within the ChatSession council logic

#### Scenario: Group session routed to main agent
- GIVEN a session with `SessionBehaviorGroup`
- WHEN the agent pool routes a message
- THEN the system SHALL route the message to the session's main agent

### Requirement: Session Resolution Before Routing

The system SHALL get or create the agent session before routing a message, so the session's behaviour determines which routing path is used.

#### Scenario: Existing session resolved
- GIVEN a session ID already exists in the pool
- WHEN the agent pool routes a message
- THEN the system SHALL use the existing session's behaviour to select the routing path

#### Scenario: Missing session created
- GIVEN a session ID does not yet exist
- WHEN the agent pool routes a message
- THEN the system SHALL create the session before selecting the routing path

### Requirement: Thought Enqueue for Daemon Navis

The system SHALL enqueue messages destined for daemon navis as `Thought` entries on the navi's thought queue, with a UUID, the message text as prompt, an estimated priority, pending status, and a per-thought log path.

#### Scenario: Message enqueued as thought
- GIVEN a message is routed to a daemon navi
- WHEN `routeToThoughtQueue` runs
- THEN the system SHALL create a `Thought` with a new UUID, the message text as prompt, and pending status
- AND enqueue it on the navi's queue

#### Scenario: Thought log path assigned
- GIVEN a thought is enqueued for a daemon navi
- WHEN the thought is created
- THEN the system SHALL assign a log path under the profile's thoughts directory

### Requirement: Thought Priority Estimation

The system SHALL estimate the priority of each message before enqueuing it as a thought, so urgent messages get scheduled ahead of routine work.

#### Scenario: Urgent message prioritized
- GIVEN a message is flagged as urgent or comes from a critical source
- WHEN `estimatePriority` runs
- THEN the system SHALL assign the thought a high priority value

#### Scenario: Routine message normal priority
- GIVEN a routine message is enqueued
- WHEN `estimatePriority` runs
- THEN the system SHALL assign the thought a normal or low priority value

### Requirement: Council Session Initiation

The system SHALL start council mode when a routed message arrives at a session not yet in council mode, initializing the council state with a question, round one, and empty responses, and switching the session to council mode.

#### Scenario: Council started on first message
- GIVEN a message is routed to a session with no active council
- WHEN the router processes it
- THEN the system SHALL set the session to council mode
- AND initialize the council state with the message text as the question and round one

#### Scenario: Council invite sent to members
- GIVEN a council round starts
- WHEN `startCouncilRound` runs
- THEN the system SHALL send a `MessageTypeCouncilInvite` to each council member on their delivery platforms
- AND include the round number and question in the invitation

### Requirement: Council Member Response Handling

The system SHALL handle responses from council members by recording their answer, delivering the response to all member platforms, and synthesizing a summary when all members have responded.

#### Scenario: Member response recorded and relayed
- GIVEN a council member sends a response
- WHEN the gateway handles the council response
- THEN the system SHALL record the member's answer in the council state
- AND deliver the response to all platforms the members are on

#### Scenario: Completed council synthesized
- GIVEN all council members have responded to a round
- WHEN the council is complete
- THEN the system SHALL synthesize a summary from the responses
- AND deliver the summary to the session

### Requirement: Council Round Progression

The system SHALL advance the council to the next round when a continuation message arrives and the council is not complete, incrementing the round and resetting responses.

#### Scenario: Continuation starts next round
- GIVEN the main agent continues a council that is not complete
- WHEN the gateway routes the continuation
- THEN the system SHALL increment the council round
- AND reset the responses for the new round

#### Scenario: Completed council delivers summary
- GIVEN a council is complete
- WHEN the main agent sends a further message
- THEN the system SHALL synthesize and deliver the council summary rather than starting a new round

### Requirement: Agent Session Pooling

The system SHALL maintain an agent pool that gets or creates an `AgentSession` per session ID, creating a new agent on first access and reusing the existing session on subsequent access.

#### Scenario: Existing session reused
- GIVEN a session ID already in the pool
- WHEN `GetOrCreate` is called
- THEN the system SHALL return the existing `AgentSession` without creating a new agent

#### Scenario: New session created
- GIVEN a session ID not yet in the pool
- WHEN `GetOrCreate` is called
- THEN the system SHALL create a new agent and register a new `AgentSession` with idle status

### Requirement: Agent Event Streaming and Delivery

The system SHALL run agents asynchronously per session, subscribe to agent events, stream partial content and tool events to the platform, and deliver the final response, updating session status throughout.

#### Scenario: Running agent receives steering message
- GIVEN an agent session is currently running
- WHEN a new message arrives for the session
- THEN the system SHALL queue the message as a steering or follow-up instruction instead of starting a second run

#### Scenario: Agent events streamed to platform
- GIVEN an agent run is in progress
- WHEN agent events fire
- THEN the system SHALL stream content deltas to the platform
- AND deliver tool calls and tool results as platform messages

#### Scenario: Agent completion and errors
- GIVEN an agent run finishes
- WHEN the agent emits an end or error event
- THEN the system SHALL deliver the final response on success, deliver an error on failure, and set the session status accordingly

### Requirement: Council Response Delivery to Member Platforms

The system SHALL deliver each council member's response to all platforms the council members are on, using `MessageTypeCouncilMsg` messages with the member ID and round number, so every participant sees the discussion.

#### Scenario: Member response broadcast to member platforms
- GIVEN a council member submits a response
- WHEN the gateway handles the council response
- THEN the system SHALL deliver a `MessageTypeCouncilMsg` carrying the member ID and round to each member's delivery platforms

#### Scenario: Completion check after broadcast
- GIVEN all council members have responded
- WHEN the gateway delivers the responses
- THEN the system SHALL synthesize a summary
- AND deliver the summary to the session

---


## 02 Message Mirroring

### 5.1 MirrorSystem

Cross-session and cross-platform message mirroring:

```go
type MirrorSystem struct {
    rules      []MirrorRule
    mirrorLog  map[string]bool // dedup
    mu         sync.RWMutex
}

type MirrorRule struct {
    SourcePlatform  string
    SourceChannel   string // "" = all channels
    TargetPlatforms []string
    TargetChannels  []string
    Filter          string // regex filter for message content
}

func (m *MirrorSystem) ShouldMirror(msg *Message) []MirrorTarget {
    m.mu.RLock()
    defer m.mu.RUnlock()

    // Dedup
    key := fmt.Sprintf("%s-%s-%s", msg.ID, msg.From.Platform, msg.From.ChannelID)
    if m.mirrorLog[key] {
        return nil
    }
    m.mirrorLog[key] = true

    var targets []MirrorTarget
    for _, rule := range m.rules {
        if rule.SourcePlatform != "" && rule.SourcePlatform != msg.From.Platform {
            continue
        }
        if rule.SourceChannel != "" && rule.SourceChannel != msg.From.ChannelID {
            continue
        }
        if rule.Filter != "" {
            matched, _ := regexp.MatchString(rule.Filter, msg.Content.Text)
            if !matched {
                continue
            }
        }

        for _, targetPlatform := range rule.TargetPlatforms {
            for _, targetChannel := range rule.TargetChannels {
                targets = append(targets, MirrorTarget{
                    Platform:  targetPlatform,
                    ChannelID: targetChannel,
                    Message:   *msg,
                })
            }
        }
    }

    return targets
}
```

### 5.2 Mirror Config

```yaml
# ~/.opennavi/profiles/<name>/mirror.yaml
mirror:
  rules:
    # Mirror Telegram DMs to Discord
    - source_platform: "telegram"
      source_channel: ""  # all channels
      target_platforms: ["discord"]
      target_channels: ["work-channel"]

    # Mirror Discord work channel to Slack
    - source_platform: "discord"
      source_channel: "work-channel"
      target_platforms: ["slack"]
      target_channels: ["#agent-work"]

    # Mirror webhook events to Telegram
    - source_platform: "webhook"
      source_channel: ""
      target_platforms: ["telegram"]
      target_channels: ["alerts"]
      filter: "alert|incident|critical"
```

### Requirement: Mirror Rule Matching

The system SHALL mirror a message to target platforms and channels when a `MirrorRule` matches its source platform, source channel, and optional content filter.

#### Scenario: Platform rule match
- GIVEN a rule whose `SourcePlatform` matches the message's platform and whose `SourceChannel` is empty or matches
- WHEN `ShouldMirror` evaluates the rule
- THEN the system SHALL add each target platform and channel pair as a mirror target

#### Scenario: Filter mismatch suppresses mirror
- GIVEN a rule has a content filter that does not match the message text
- WHEN `ShouldMirror` evaluates the rule
- THEN the system SHALL skip the rule for that message

### Requirement: Mirror Deduplication

The system SHALL prevent duplicate mirroring by recording each message's platform and channel in a dedup log and skipping messages already mirrored.

#### Scenario: Duplicate message suppressed
- GIVEN a message was already evaluated for mirroring from the same platform and channel
- WHEN `ShouldMirror` is called again with the same message
- THEN the system SHALL return no mirror targets

#### Scenario: First evaluation mirrors normally
- GIVEN a message is evaluated for mirroring for the first time
- WHEN `ShouldMirror` runs
- THEN the system SHALL record the message in the dedup log and return matching targets

### Requirement: Mirror Rule Configuration

The system SHALL load mirror rules from a `mirror.yaml` file in the profile directory, with each rule specifying a source platform, optional source channel, target platforms, target channels, and an optional content filter.

#### Scenario: Rules loaded from mirror.yaml
- GIVEN a `mirror.yaml` exists in the profile directory
- WHEN the mirror system initializes
- THEN the system SHALL load all configured rules into the `MirrorSystem`

#### Scenario: Empty source channel matches all channels
- GIVEN a rule with `source_channel: ""`
- WHEN `ShouldMirror` evaluates the rule
- THEN the rule SHALL match messages from all channels on the source platform

### Requirement: Filter-Based Mirroring

The system SHALL apply a regex content filter defined in a mirror rule so that only matching messages are mirrored.

#### Scenario: Regex filter restricts mirrored content
- GIVEN a rule with `filter: "alert|incident|critical"`
- WHEN a message text matches the filter
- THEN the system SHALL mirror the message
- AND when the message text does not match, the system SHALL NOT mirror it

---


---

## Related


### Other Parts

- [Message Protocol & Gateway](./00-message-protocol-gateway.md)
- [Hooks, Cron & Implementation](./02-hooks-cron-impl.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S050100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |