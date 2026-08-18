---
status: draft
created: '16'
updated: '2026-08-11'
tags:
  - spec
  - messaging
priority: medium
spec-refs: [050200, 050201, 050202, 050203, 050204, 050205, 050206, 050207]
---

# Plan: Unified Messaging System: Hooks, Cron & Implementation

**Scope:** Unified message protocol, gateway architecture, platform adapters, routing to ThoughtQueue (daemon navis) or ChatSession (council mode), and delivery system.

> **Part of:** [Plan: Unified Messaging System](./README.md) — split into 3 parts for readability.

## 00 Hook System (Expanded)

### 6.1 Hook Types

```go
type HookType string
const (
    HookMessageIn       HookType = "message_in"         // before routing
    HookMessageOut      HookType = "message_out"        // after routing
    HookSessionCreate   HookType = "session_create"     // new session created
    HookSessionLoad     HookType = "session_load"       // existing session loaded
    HookAgentStart      HookType = "agent_start"        // agent begins processing
    HookAgentEnd        HookType = "agent_end"          // agent finishes processing
    HookToolCall        HookType = "tool_call"          // tool invocation
    HookToolResult      HookType = "tool_result"        // tool result
    HookError           HookType = "error"              // any error
    HookCouncilStart    HookType = "council_start"      // council session starts
    HookCouncilEnd      HookType = "council_end"        // council session ends
    HookConfigChange    HookType = "config_change"      // agent self-modification
    HookCronTrigger     HookType = "cron_trigger"       // cron job fires
)

type HookFunc func(msg *Message) error
```

### 6.2 Hook Registry

```go
type HookRegistry struct {
    hooks map[HookType][]HookFunc
    mu    sync.RWMutex
}

func (r *HookRegistry) Register(hookType HookType, fn HookFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.hooks[hookType] = append(r.hooks[hookType], fn)
}

func (r *HookRegistry) Fire(hookType HookType, msg *Message) error {
    r.mu.RLock()
    fns := r.hooks[hookType]
    r.mu.RUnlock()

    for _, fn := range fns {
        if err := fn(msg); err != nil {
            return err
        }
    }
    return nil
}
```

### 6.3 Example Hooks

```go
// Alert hook: notify on error
func errorAlertHook() HookFunc {
    return func(msg *Message) error {
        if msg.Type == MessageTypeError {
            // Send alert to Telegram
            return deliverAlert(msg.From.Platform, msg.From.UserID, msg.Content.Text)
        }
        return nil
    }
}

// Auto-council hook: if user mentions multiple agents, start council
func autoCouncilHook() HookFunc {
    return func(msg *Message) error {
        mentions := extractAgentMentions(msg.Content.Text)
        if len(mentions) >= 2 {
            // Auto-start council with mentioned agents
            agents := make([]*Agent, 0, len(mentions))
            for _, name := range mentions {
                agent, _ := agentRegistry.Get(name)
                if agent != nil {
                    agents = append(agents, agent)
                }
            }
            if len(agents) >= 2 {
                // Return a special signal to trigger council
                msg.Type = MessageTypeCouncilInvite
                msg.Content.Council = &CouncilMessage{
                    Members: agents,
                    Question: msg.Content.Text,
                }
            }
        }
        return nil
    }
}

// Memory hook: store important messages to long-term memory
func memoryHook() HookFunc {
    return func(msg *Message) error {
        if msg.Type == MessageTypeToolResult {
            // Store tool results to long-term memory
            // (with some filtering to avoid noise)
            if shouldStoreToMemory(msg) {
                return storeToMemory(msg)
            }
        }
        return nil
    }
}
```

### Requirement: Hook Type Enumeration

The system SHALL define a `HookType` enumeration with lifecycle hooks for message_in, message_out, session_create, session_load, agent_start, agent_end, tool_call, tool_result, error, council_start, council_end, config_change, and cron_trigger events.

#### Scenario: Hook type covers message lifecycle
- GIVEN a message flows through the gateway
- WHEN the hook system fires hooks
- THEN the system SHALL fire `HookMessageIn` before routing and `HookMessageOut` after routing

#### Scenario: Hook type covers agent lifecycle
- GIVEN an agent processes a thought
- WHEN the agent starts and finishes
- THEN the system SHALL fire `HookAgentStart` when processing begins
- AND fire `HookAgentEnd` when processing finishes

### Requirement: Hook Handler Signature

The system SHALL implement hook functions with the signature `HookFunc func(msg *Message) error`, allowing hooks to inspect and mutate the message during processing.

#### Scenario: Hook inspects message fields
- GIVEN a hook function is registered
- WHEN the hook fires with a message
- THEN the hook SHALL be able to inspect the message's type, content, and source fields

#### Scenario: Hook mutates message
- GIVEN a hook needs to alter the message (e.g., auto-council)
- WHEN the hook fires with a message
- THEN the hook SHALL be able to modify the message type and content before routing continues

### Requirement: Hook Registry Registration

The system SHALL maintain a `HookRegistry` that allows registering multiple hook functions per hook type and fires all registered functions in registration order when the hook type is triggered.

#### Scenario: Multiple hooks per type
- GIVEN multiple hook functions are registered for `HookMessageIn`
- WHEN the hook fires
- THEN the system SHALL execute all registered functions in order

#### Scenario: Hook registration is thread-safe
- GIVEN concurrent goroutines register hooks
- WHEN they call `Register`
- THEN the system SHALL safely append the hook function under a write lock

### Requirement: Hook Firing Behavior

The system SHALL fire hooks synchronously and return the first error encountered, aborting the remaining hooks for that type on error.

#### Scenario: Hook fires without error
- GIVEN a hook type is triggered
- WHEN all registered hook functions return nil
- THEN the system SHALL return nil after all functions execute

#### Scenario: Hook error aborts execution
- GIVEN a hook type is triggered
- WHEN a registered hook function returns an error
- THEN the system SHALL return the error
- AND SHALL NOT execute remaining hooks for that type

### Requirement: Error Alert Hook

The system SHALL provide a built-in error alert hook that delivers a notification to the user's platform when a message of type `MessageTypeError` is processed.

#### Scenario: Error message triggers alert
- GIVEN the error alert hook is registered
- WHEN a message of type `MessageTypeError` is processed
- THEN the system SHALL deliver an alert to the sender's platform

#### Scenario: Non-error message ignored
- GIVEN the error alert hook is registered
- WHEN a message of a type other than error is processed
- THEN the system SHALL NOT trigger the alert

### Requirement: Auto-Council Hook

The system SHALL provide an auto-council hook that detects when a user mentions multiple agents in a single message and automatically initiates a council session with those agents.

#### Scenario: Multi-agent mention triggers council
- GIVEN a message mentions two or more valid agents
- WHEN the auto-council hook fires
- THEN the system SHALL set the message type to `MessageTypeCouncilInvite`
- AND populate the council data with the mentioned agents and the question

#### Scenario: Single agent mention does not trigger council
- GIVEN a message mentions only one agent
- WHEN the auto-council hook fires
- THEN the system SHALL NOT modify the message

---


## 01 Cron Scheduler (Expanded)

### 7.1 Cron Job Types

```go
type CronJobType string
const (
    CronJobPrompt    CronJobType = "prompt"     // send a prompt to an agent
    CronJobWebhook   CronJobType = "webhook"    // trigger a webhook
    CronJobHealth    CronJobType = "health"     // health check
    CronJobCouncil   CronJobType = "council"    // trigger a council session
    CronJobMemory    CronJobType = "memory"     // memory maintenance
)

type CronJob struct {
    ID        string
    Name      string
    Type      CronJobType
    Schedule  string        // cron expression or interval
    NextRun   time.Time
    Prompt    string        // for prompt/council jobs
    Target    SessionKey    // deliver to this session
    Platforms []string      // deliver to these platforms
    AgentID   string        // target agent
    CouncilID string        // target council session
    State     map[string]any
    Enabled   bool
}
```

### 7.2 Cron Job Examples

```yaml
# ~/.opennavi/profiles/<name>/cron.yaml
cron:
  jobs:
    - name: "morning briefing"
      type: "prompt"
      schedule: "0 8 * * 1-5"  # weekdays at 8am
      prompt: "Give me a briefing: overnight alerts, PRs awaiting review, and any issues that need attention."
      target:
        platform: "telegram"
        user: "alice"
      agent: "default"

    - name: "health check"
      type: "health"
      schedule: "*/5 * * * *"  # every 5 minutes
      target:
        platform: "webhook"
      state:
        services:
          - "https://api.example.com/health"
          - "https://db.example.com/health"

    - name: "weekly council"
      type: "council"
      schedule: "0 10 * * 1"  # Mondays at 10am
      prompt: "Review the week's progress and suggest priorities for next week."
      target:
        platform: "discord"
        channel: "work-channel"
      council:
        - "coder"
        - "researcher"
        - "reviewer"

    - name: "memory cleanup"
      type: "memory"
      schedule: "0 3 * * 0"  # Sundays at 3am
      state:
        max_memory_entries: 10000
        prune_low_score: true
        low_score_threshold: 0.1
```

### Requirement: Cron Job Types

The system SHALL support cron job types for prompt, webhook, health check, council, and memory maintenance tasks, each represented by a `CronJob` struct with identity, schedule, prompt, target, platforms, agent ID, council ID, state, and enabled flag.

#### Scenario: Cron job defined with type and schedule
- GIVEN a cron job is configured with a type and cron expression
- WHEN the cron scheduler loads it
- THEN the system SHALL create a `CronJob` with the specified type and schedule

#### Scenario: Job type determines execution behavior
- GIVEN a cron job of type `CronJobPrompt`
- WHEN the scheduler triggers it
- THEN the system SHALL send the configured prompt to the target agent
- AND a council type job SHALL trigger a council session
- AND a health type job SHALL run health checks
- AND a memory type job SHALL perform memory maintenance

### Requirement: Cron Job Scheduling

The system SHALL schedule cron jobs using standard cron expressions, tracking the next run time and executing jobs when their schedule fires.

#### Scenario: Job scheduled by cron expression
- GIVEN a cron job with `schedule: "0 8 * * 1-5"`
- WHEN the scheduler calculates the next run
- THEN the system SHALL execute the job at 8:00 AM on weekdays

#### Scenario: Job execution at scheduled time
- GIVEN a cron job is enabled
- WHEN the current time matches the job's next run time
- THEN the system SHALL execute the job and update the next run time

### Requirement: Cron Job Configuration

The system SHALL load cron jobs from a `cron.yaml` file in the profile directory, with each job specifying a name, type, schedule, prompt, target platform and user, agent, and optional council and state fields.

#### Scenario: Jobs loaded from cron.yaml
- GIVEN a `cron.yaml` exists in the profile directory
- WHEN the cron scheduler initializes
- THEN the system SHALL load all configured jobs

#### Scenario: Prompt job delivers to specified platform
- GIVEN a prompt-type cron job with a target platform and user
- WHEN the job executes
- THEN the system SHALL deliver the prompt to the specified user on the specified platform

#### Scenario: Council job specifies members
- GIVEN a council-type cron job with a list of council members
- WHEN the job executes
- THEN the system SHALL start a council session with the specified members

### Requirement: Health Check Job Execution

The system SHALL execute health check cron jobs by probing the services listed in the job's state on the job's schedule.

#### Scenario: Configured services checked on schedule
- GIVEN a health cron job with services in its state
- WHEN the job fires on its schedule
- THEN the system SHALL probe each listed service
- AND report the health results to the job's target platform

#### Scenario: Health job runs every five minutes
- GIVEN a health job with schedule `*/5 * * * *`
- WHEN the scheduler runs
- THEN the system SHALL execute the health check every five minutes

---


## 02 Auth System (Expanded)

### 8.1 Auth Layers

```go
type AuthManager struct {
    config    *AuthConfig
    pairing   *PairingManager
    allowlist *Allowlist
}

type AuthConfig struct {
    // Platform-level auth
    Platforms map[string]PlatformAuth

    // DM-only mode (no group chat access without pairing)
    DMOnly bool

    // Admin users (can access all features)
    Admins map[string][]string // platform → userIDs

    // Webhook auth
    WebhookSecret string

    // Rate limiting
    RateLimit RateLimitConfig
}

type PlatformAuth struct {
    AllowedUsers []string // platform-specific user IDs
    AllowedGroups []string // platform-specific group/channel IDs
    DMOnly       bool     // override DM-only for this platform
}
```

### 8.2 Auth Flow

```go
func (a *AuthManager) Check(source MessageSource) error {
    // 1. Check admin
    if a.isAdmin(source) {
        return nil
    }

    // 2. Check platform allowlist
    platformAuth, ok := a.config.Platforms[source.Platform]
    if !ok {
        return fmt.Errorf("platform %s not configured", source.Platform)
    }

    // 3. Check DM-only
    if a.config.DMOnly || platformAuth.DMOnly {
        if source.ChannelID != "" {
            // In a group/channel — check pairing
            if !a.pairing.IsPaired(source) {
                return fmt.Errorf("not paired in group chat")
            }
        }
    }

    // 4. Check user allowlist
    if len(platformAuth.AllowedUsers) > 0 {
        allowed := false
        for _, id := range platformAuth.AllowedUsers {
            if id == source.UserID {
                allowed = true
                break
            }
        }
        if !allowed {
            return fmt.Errorf("user not in allowlist")
        }
    }

    // 5. Check group allowlist
    if source.ChannelID != "" && len(platformAuth.AllowedGroups) > 0 {
        allowed := false
        for _, id := range platformAuth.AllowedGroups {
            if id == source.ChannelID {
                allowed = true
                break
            }
        }
        if !allowed {
            return fmt.Errorf("channel not in allowlist")
        }
    }

    return nil
}
```

### 8.3 Rate Limiting

```go
type RateLimitConfig struct {
    MessagesPerMinute int           // per-user rate limit
    MessagesPerHour   int           // per-user hourly limit
    BurstSize         int           // burst allowance
    Window            time.Duration // rate limit window
}

type RateLimiter struct {
    config RateLimitConfig
    users  map[string]*userRate
    mu     sync.RWMutex
}

type userRate struct {
    messages []time.Time
    lastReset time.Time
}

func (r *RateLimiter) Allow(source MessageSource) error {
    key := fmt.Sprintf("%s:%s", source.Platform, source.UserID)

    r.mu.RLock()
    rate, ok := r.users[key]
    r.mu.RUnlock()

    if !ok {
        rate = &userRate{lastReset: time.Now()}
        r.mu.Lock()
        r.users[key] = rate
        r.mu.Unlock()
    }

    // Clean old entries
    now := time.Now()
    cutoff := now.Add(-r.config.Window)
    i := 0
    for ; i < len(rate.messages); i++ {
        if rate.messages[i].After(cutoff) {
            break
        }
    }
    rate.messages = rate.messages[i:]

    // Check limits
    if len(rate.messages) >= r.config.MessagesPerMinute {
        return fmt.Errorf("rate limit exceeded: %d messages per minute", r.config.MessagesPerMinute)
    }
    if len(rate.messages) >= r.config.MessagesPerHour {
        return fmt.Errorf("rate limit exceeded: %d messages per hour", r.config.MessagesPerHour)
    }

    rate.messages = append(rate.messages, now)
    return nil
}
```

### Requirement: Auth Configuration Layers

The system SHALL support platform-level authentication with allowed users, allowed groups, DM-only overrides, admin users, webhook secrets, and rate limiting configuration.

#### Scenario: Platform auth configured per platform
- GIVEN an auth config with platform-specific allowed users and groups
- WHEN an auth check runs
- THEN the system SHALL apply the platform-specific rules for the message's source platform

#### Scenario: Admin users bypass checks
- GIVEN a user is listed in the admin list for their platform
- WHEN the auth check runs
- THEN the system SHALL allow the message without further checks

### Requirement: Platform Auth Overrides

The system SHALL support per-platform auth overrides, where a `PlatformAuth` entry can override the DM-only mode or restrict allowed users and groups for its platform.

#### Scenario: Platform overrides DM-only
- GIVEN a platform has `DMOnly: true` set in its `PlatformAuth`
- WHEN the auth check runs for a message on that platform
- THEN the system SHALL apply the platform-specific DM-only override

#### Scenario: Platform with empty allowlists
- GIVEN a platform has no allowed users or groups configured
- WHEN the auth check runs for a message on that platform
- THEN the system SHALL not restrict the platform by allowlist

### Requirement: Authentication Check Flow

The system SHALL check messages in order: admin bypass, platform allowlist, DM-only pairing check, user allowlist, and group allowlist.

#### Scenario: User in allowlist passes
- GIVEN a user is in the platform's allowed users list
- WHEN the auth check runs
- THEN the system SHALL return nil

#### Scenario: User not in allowlist rejected
- GIVEN a user is not in the platform's allowed users list
- WHEN the auth check runs
- THEN the system SHALL return an error stating the user is not in the allowlist

#### Scenario: Channel checked against group allowlist
- GIVEN a message comes from a channel
- WHEN the platform has an allowed groups list
- THEN the system SHALL check the channel ID against the allowed groups
- AND reject the message if the channel is not allowed

### Requirement: DM-Only Mode and Pairing

The system SHALL support a DM-only mode that restricts group chat access to paired users, where pairing is checked per user and platform.

#### Scenario: DM-only rejects unpaired group messages
- GIVEN DM-only mode is enabled
- WHEN a message arrives from a group or channel
- THEN the system SHALL check if the sender is paired for group access
- AND reject the message if the sender is not paired

#### Scenario: DM-only allows direct messages
- GIVEN DM-only mode is enabled
- WHEN a message arrives from a direct message conversation
- THEN the system SHALL allow the message without pairing check

### Requirement: Per-User Rate Limiting

The system SHALL enforce per-user rate limits with configurable messages per minute, messages per hour, and a window duration, keyed by platform and user ID.

#### Scenario: Rate limit enforced per user
- GIVEN a user sends messages within the rate limit
- WHEN each message is processed
- THEN the system SHALL allow the message and record the timestamp

#### Scenario: Rate limit exceeded
- GIVEN a user exceeds the configured messages per minute or per hour
- WHEN the rate limiter checks
- THEN the system SHALL return an error with the rate limit message

### Requirement: Rate Limit Window Management

The system SHALL maintain a sliding window of message timestamps per user, cleaning entries older than the window before checking limits.

#### Scenario: Old entries purged
- GIVEN a user has a history of messages
- WHEN the rate limiter runs
- THEN the system SHALL remove timestamps older than the rate limit window before checking

#### Scenario: New user rate recorded
- GIVEN a user sends their first message
- WHEN the rate limiter runs
- THEN the system SHALL create a new rate entry for the user
- AND record the timestamp

---


## 03 Platform Adapters — Implementation Details

### 9.1 Telegram Adapter

```go
type TelegramAdapter struct {
    bot      *tgbotapi.BotAPI
    handler  MessageHandler
    chatCache map[string]int64
    mu       sync.RWMutex
}

func (a *TelegramAdapter) Start(ctx context.Context, handler MessageHandler) error {
    a.handler = handler

    // Long-polling updates
    updateConfig := tgbotapi.NewUpdate(0)
    updateConfig.Timeout = 60
    updates := a.bot.GetUpdatesChan(updateConfig)

    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case update, ok := <-updates:
                if !ok {
                    return
                }
                if update.Message == nil {
                    continue
                }

                payload, _ := json.Marshal(update)
                a.handler(RawMessage{
                    Adapter: a,
                    Payload: payload,
                    Metadata: map[string]any{"update_id": update.UpdateID},
                })
            }
        }
    }()

    return nil
}

func (a *TelegramAdapter) Deliver(sessionKey SessionKey, msg Message) error {
    chatID := a.resolveChatID(sessionKey)

    if msg.Content.Markdown != "" {
        _, err := a.bot.Send(tgbotapi.NewHTMLMessage(chatID, msg.Content.Markdown))
        return err
    }

    return a.bot.Send(tgbotapi.NewMessage(chatID, msg.Content.Text))
}

func (a *TelegramAdapter) DeliverStreaming(sessionKey SessionKey, events <-chan StreamEvent) error {
    chatID := a.resolveChatID(sessionKey)

    // Send initial message
    msg, err := a.bot.Send(tgbotapi.NewMessage(chatID, "Thinking..."))
    if err != nil {
        return err
    }

    // Typewriter: edit message as content grows
    var fullContent strings.Builder
    editTicker := time.NewTicker(2 * time.Second)
    defer editTicker.Stop()

    for {
        select {
        case event, ok := <-events:
            if !ok {
                return nil
            }
            fullContent.WriteString(event.Content)

            // Telegram max 4096 chars — split into multiple messages if needed
            text := fullContent.String()
            if len(text) > 4096 {
                // Send accumulated content as new message
                a.bot.Send(tgbotapi.NewMessage(chatID, text[:4096]))
                fullContent.Reset()
                fullContent.WriteString(text[4096:])
            }

        case <-editTicker.C:
            text := fullContent.String()
            if text != "" {
                edit := tgbotapi.NewEditMessageText(chatID, msg.MessageID, text)
                a.bot.Send(edit)
            }
        }
    }
}
```

### 9.2 Discord Adapter

```go
type DiscordAdapter struct {
    session    *discordgo.Session
    handler    MessageHandler
    channelCache map[string]string
    mu         sync.RWMutex
}

func (a *DiscordAdapter) Start(ctx context.Context, handler MessageHandler) error {
    a.handler = handler

    a.session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
        if m.Author.Bot {
            return // ignore bot messages
        }

        payload, _ := json.Marshal(m)
        a.handler(RawMessage{
            Adapter: a,
            Payload: payload,
            Metadata: map[string]any{"message_id": m.ID, "channel_id": m.ChannelID},
        })
    })

    // Handle slash commands
    a.session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        if i.Type != discordgo.InteractionMessageCommand {
            return
        }

        payload, _ := json.Marshal(i)
        a.handler(RawMessage{
            Adapter: a,
            Payload: payload,
            Metadata: map[string]any{"interaction_id": i.ID},
        })
    })

    return a.session.Open()
}

func (a *DiscordAdapter) Deliver(sessionKey SessionKey, msg Message) error {
    channelID := a.resolveChannelID(sessionKey)

    if msg.Content.Markdown != "" {
        // Discord 2000 char limit
        for _, chunk := range splitByChunk(msg.Content.Markdown, 2000) {
            _, err := a.session.ChannelMessageSend(channelID, chunk)
            if err != nil {
                return err
            }
        }
        return nil
    }

    _, err := a.session.ChannelMessageSend(channelID, msg.Content.Text)
    return err
}

func (a *DiscordAdapter) DeliverStreaming(sessionKey SessionKey, events <-chan StreamEvent) error {
    channelID := a.resolveChannelID(sessionKey)

    // Send initial message
    msg, err := a.session.ChannelMessageSend(channelID, "...")
    if err != nil {
        return err
    }

    var fullContent strings.Builder
    editTicker := time.NewTicker(3 * time.Second)
    defer editTicker.Stop()

    for {
        select {
        case event, ok := <-events:
            if !ok {
                return nil
            }
            fullContent.WriteString(event.Content)

            text := fullContent.String()
            if len(text) > 2000 {
                // Send overflow as new message
                a.session.ChannelMessageSend(channelID, text[:2000])
                fullContent.Reset()
                fullContent.WriteString(text[2000:])
            }

        case <-editTicker.C:
            text := fullContent.String()
            if text != "" {
                a.session.ChannelMessageEdit(channelID, msg.ID, text)
            }
        }
    }
}
```

### 9.3 Slack Adapter

```go
type SlackAdapter struct {
    client   *slack.Client
    handler  MessageHandler
    wsConfig slack.WebSocketConfig
    mu       sync.RWMutex
}

func (a *SlackAdapter) Start(ctx context.Context, handler MessageHandler) error {
    a.handler = handler

    // Use websocket API for real-time messages
    wsConfig := slack.NewWebSocketConfig(
        slack.DirectSetupAppOption{},
        a.handleSlackEvent,
    )

    // Handle incoming events
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case msg := <-wsConfig.Events:
                switch e := msg.(type) {
                case slack.Message:
                    if e.BotID != "" {
                        continue // ignore bot messages
                    }

                    payload, _ := json.Marshal(e)
                    a.handler(RawMessage{
                        Adapter: a,
                        Payload: payload,
                        Metadata: map[string]any{"channel": e.Channel, "user": e.User},
                    })
                }
            }
        }
    }()

    return nil
}

func (a *SlackAdapter) handleSlackEvent(msg slack.Message) {
    // Called by slack.go library
    payload, _ := json.Marshal(msg)
    a.handler(RawMessage{
        Adapter: a,
        Payload: payload,
        Metadata: map[string]any{"channel": msg.Channel, "user": msg.User},
    })
}

func (a *SlackAdapter) Deliver(sessionKey SessionKey, msg Message) error {
    channelID := a.resolveChannelID(sessionKey)

    _, _, err := a.client.PostMessage(channelID,
        slack.MsgOptionText(msg.Content.Markdown, true), // mrkdwn
    )
    return err
}
```

### 9.4 Webhook Adapter

```go
type WebhookAdapter struct {
    server *http.Server
    secret string
    handler MessageHandler
}

func (a *WebhookAdapter) Start(ctx context.Context, handler MessageHandler) error {
    a.handler = handler

    mux := http.NewServeMux()
    mux.HandleFunc("/gateway/webhook", a.handleWebhook)

    a.server = &http.Server{
        Addr:    ":8080",
        Handler: mux,
    }

    go func() {
        if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Printf("webhook server error: %v", err)
        }
    }()

    return nil
}

func (a *WebhookAdapter) handleWebhook(w http.ResponseWriter, r *http.Request) {
    // Auth check
    if a.secret != "" {
        headerSecret := r.Header.Get("X-OpenNavi-Secret")
        if headerSecret != a.secret {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
    }

    var req struct {
        User     string `json:"user"`
        Message  string `json:"message"`
        Platform string `json:"platform"`
        Channel  string `json:"channel"`
    }

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }

    payload, _ := json.Marshal(req)
    a.handler(RawMessage{
        Adapter: a,
        Payload: payload,
        Metadata: map[string]any{"platform": req.Platform, "channel": req.Channel},
    })

    w.WriteHeader(http.StatusOK)
    w.Write([]byte("ok"))
}

func (a *WebhookAdapter) Deliver(sessionKey SessionKey, msg Message) error {
    // Webhook delivery is typically initiated by the user, not the gateway
    // Return an error to indicate this adapter doesn't support outbound delivery
    return fmt.Errorf("webhook adapter does not support outbound delivery")
}
```

### Requirement: Telegram Adapter Long-Polling and Delivery

The system SHALL implement a Telegram adapter that uses long-polling updates with a 60-second timeout to receive messages and delivers text and markdown replies via the Telegram Bot API.

#### Scenario: Telegram receives messages via long-polling
- GIVEN a Telegram adapter is started
- WHEN the adapter runs
- THEN the system SHALL poll updates from the Telegram Bot API with a 60-second timeout
- AND forward each message to the handler as a `RawMessage`

#### Scenario: Telegram delivers HTML-formatted markdown
- GIVEN a message contains markdown content
- WHEN the Telegram delivery adapter sends it
- THEN the system SHALL send the content as an HTML message

### Requirement: Telegram Streaming Delivery

The system SHALL implement a typewriter streaming pattern for Telegram: send an initial "Thinking..." message, then edit it every 2 seconds as content grows, sending overflow as new messages when content exceeds 4096 characters.

#### Scenario: Streamed content edited incrementally
- GIVEN a streaming event channel for a Telegram response
- WHEN the streaming adapter runs
- THEN the system SHALL send an initial message
- AND edit the message every 2 seconds with accumulated content

#### Scenario: Content overflow creates new messages
- GIVEN streamed content exceeds 4096 characters
- WHEN the streaming adapter sends content
- THEN the system SHALL send the first 4096 characters as a new message and reset the accumulator

### Requirement: Discord Adapter Gateway and Delivery

The system SHALL implement a Discord adapter that uses the Discord Gateway protocol to receive messages, including bot-authored message filtering and slash command handling, and delivers markdown split into 2000-character chunks.

#### Scenario: Discord receives messages via gateway
- GIVEN a Discord adapter is started
- WHEN the adapter connects
- THEN the system SHALL open the Discord Gateway connection
- AND register handlers for `MessageCreate` and `InteractionCreate` events

#### Scenario: Bot messages ignored
- GIVEN a Discord message from a bot author
- WHEN the adapter receives it
- THEN the system SHALL ignore the message

#### Scenario: Discord markdown chunked by limit
- GIVEN a markdown message exceeds 2000 characters
- WHEN the Discord delivery adapter sends it
- THEN the system SHALL split the content into 2000-character chunks
- AND send each chunk as a separate message

### Requirement: Discord Streaming Delivery

The system SHALL implement a typewriter streaming pattern for Discord: send an initial "..." message, then edit it every 3 seconds as content grows, sending overflow as new messages when content exceeds 2000 characters.

#### Scenario: Discord streamed content edited
- GIVEN a streaming event channel for a Discord response
- WHEN the streaming adapter runs
- THEN the system SHALL send an initial "..." message
- AND edit the message every 3 seconds with accumulated content

#### Scenario: Discord overflow creates new messages
- GIVEN streamed content exceeds 2000 characters
- WHEN the streaming adapter sends content
- THEN the system SHALL send the first 2000 characters as a new message and reset the accumulator

### Requirement: Slack Adapter WebSocket and Delivery

The system SHALL implement a Slack adapter that uses the Slack WebSocket API to receive real-time messages, filtering bot messages, and delivers markdown messages using Slack's mrkdwn format.

#### Scenario: Slack receives messages via WebSocket
- GIVEN a Slack adapter is started
- WHEN the adapter runs
- THEN the system SHALL connect to the Slack WebSocket API
- AND forward each message event to the handler

#### Scenario: Bot messages ignored
- GIVEN a Slack message with a bot ID
- WHEN the adapter receives it
- THEN the system SHALL ignore the message

#### Scenario: Slack delivers mrkdwn content
- GIVEN a message contains markdown content
- WHEN the Slack delivery adapter sends it
- THEN the system SHALL send the content with mrkdwn formatting enabled

### Requirement: Slack Incoming Message Filtering

The system SHALL process incoming Slack message events from the WebSocket event loop, ignoring events with a bot ID and forwarding user messages to the handler with channel and user metadata.

#### Scenario: Bot message ignored
- GIVEN a Slack message event with a non-empty `BotID`
- WHEN the adapter processes the event
- THEN the system SHALL skip the message without forwarding it

#### Scenario: User message forwarded
- GIVEN a Slack message event from a user
- WHEN the adapter processes the event
- THEN the system SHALL marshal the event into a `RawMessage`
- AND forward it to the handler with channel and user metadata

### Requirement: Webhook Adapter HTTP Receiver

The system SHALL implement a webhook adapter that starts an HTTP server at a configurable port and path, accepting JSON POST requests with user, message, platform, and channel fields, and forwarding them to the gateway handler.

#### Scenario: Webhook receives HTTP POST
- GIVEN a webhook adapter is started
- WHEN an HTTP POST arrives at `/gateway/webhook`
- THEN the system SHALL parse the JSON body into a user, message, platform, and channel
- AND forward the parsed payload to the handler

#### Scenario: Webhook adapter rejects outbound delivery
- GIVEN a message needs to be sent out via the webhook adapter
- WHEN `Deliver` is called
- THEN the system SHALL return an error indicating outbound delivery is not supported

### Requirement: Webhook Secret Authentication

The system SHALL authenticate incoming webhook requests by validating the `X-OpenNavi-Secret` header against the configured secret, returning 401 Unauthorized on mismatch.

#### Scenario: Valid secret passes
- GIVEN a webhook adapter with a configured secret
- WHEN a request arrives with the matching `X-OpenNavi-Secret` header
- THEN the system SHALL process the request normally

#### Scenario: Invalid secret rejected
- GIVEN a webhook adapter with a configured secret
- WHEN a request arrives with a mismatched or missing `X-OpenNavi-Secret` header
- THEN the system SHALL respond with HTTP 401 Unauthorized

---


## 04 Gateway Configuration

### 10.1 Gateway Settings

```yaml
# ~/.opennavi/profiles/<name>/gateway.yaml
gateway:
  # Listening
  http_port: 8080
  http_host: "0.0.0.0"

  # Enabled adapters
  adapters:
    telegram:
      enabled: true
      bot_token: "${TELEGRAM_BOT_TOKEN}"  # envvar expansion
      webhooks:
        url: "https://example.com/webhook/telegram"
        secret: "${TELEGRAM_WEBHOOK_SECRET}"

    discord:
      enabled: true
      bot_token: "${DISCORD_BOT_TOKEN}"
      application_id: "${DISCORD_APP_ID}"
      guild_id: "1234567890"  # limit to specific server
      channels:
        - "general"
        - "agent-work"

    slack:
      enabled: true
      bot_token: "${SLACK_BOT_TOKEN}"
      signing_secret: "${SLACK_SIGNING_SECRET}"
      team_id: "T123456"

    webhook:
      enabled: true
      port: 8080
      secret: "${WEBHOOK_SECRET}"

  # Auth
  auth:
    dm_only: false
    admins:
      telegram: ["123456789"]
      discord: ["987654321"]
    rate_limit:
      messages_per_minute: 30
      messages_per_hour: 500
      burst_size: 10

  # Mirror rules
  mirror:
    rules:
      - source_platform: "telegram"
        target_platforms: ["discord"]
        target_channels: ["agent-work"]

  # Cron jobs
  cron:
    enabled: true
    jobs:
      - name: "daily check"
        type: "prompt"
        schedule: "0 9 * * *"
        prompt: "Check system health and report any issues."
        target:
          platform: "telegram"
          user: "123456789"
```

---


## 05 File Structure

```
cmd/opennavi-gateway/
├── main.go               # Gateway entry point
├── internal/
│   ├── gateway/
│   │   ├── gateway.go    # GatewayRunner
│   │   ├── router.go     # MessageRouter (parse + resolve)
│   │   ├── delivery.go   # DeliveryRegistry
│   │   ├── pairing.go    # DM pairing
│   │   ├── hooks.go      # HookRegistry
│   │   ├── mirror.go     # MirrorSystem
│   │   ├── status.go     # Token locks, process tracking
│   │   └── config.go     # Gateway config loading
│   ├── adapters/
│   │   ├── adapter.go    # Adapter interface, MessageParser
│   │   ├── telegram.go   # Telegram adapter + delivery
│   │   ├── discord.go    # Discord adapter + delivery
│   │   ├── slack.go      # Slack adapter + delivery
│   │   ├── webhook.go    # Webhook adapter
│   │   ├── email.go      # Email adapter (IMAP)
│   │   └── sms.go        # SMS adapter (Twilio)
│   ├── cron/
│   │   ├── scheduler.go  # CronScheduler
│   │   ├── jobs.go       # CronJob types and execution
│   │   └── config.go     # Cron config loading
│   ├── auth/
│   │   ├── auth.go       # AuthManager
│   │   ├── allowlist.go  # Allowlist
│   │   ├── pairing.go    # DM pairing
│   │   └── ratelimit.go  # RateLimiter
│   ├── session/
│   │   ├── manager.go    # SessionManager (shared with core)
│   │   └── council.go    # CouncilState management
│   └── agent/
│       ├── pool.go       # AgentPool
│       └── routing.go    # Multi-agent routing
└── docs/
    └── gateway-config.md # Config reference
```

---


## 06 Implementation Order

### Phase 1: Gateway Foundation
1. **GatewayRunner** — core event loop, lifecycle
2. **Webhook adapter** — simplest adapter, validates architecture
3. **MessageRouter** — parse + resolve flow
4. **SessionManager integration** — shared with core
5. **DeliveryRegistry** — platform delivery abstraction
6. **HookRegistry** — lifecycle hooks
7. **Auth system** — allowlist + DM pairing + rate limiting
8. **Gateway config** — gateway.yaml loading

### Phase 2: Platform Adapters
9. **Telegram adapter** — long-polling + delivery
10. **Discord adapter** — gateway protocol + delivery
11. **Slack adapter** — WebSocket API + delivery
12. **Delivery streaming** — typewriter pattern for all platforms

### Phase 3: Multi-Agent & Council
13. **AgentPool** — agent lifecycle in gateway
14. **Multi-agent routing** — @mentions → agent routing
15. **Council mode** — StartCouncil, EndCouncil, council coordination within ChatSession
16. **Council routing** — route council messages to members
17. **Auto-council hook** — auto-start council on @mentions

### Phase 4: Advanced Features
18. **Cron scheduler** — first-class agent tasks
19. **Mirror system** — cross-session/platform sync
20. **Email adapter** — IMAP polling
21. **SMS adapter** — Twilio API
22. **Signal/WhatsApp adapters**

---


## 07 Key Design Decisions

1. **Unified Message type** — single `Message` struct for all platforms and scenarios
2. **Unified session model** — Every session is a conversation with participants. Behavior is determined by who's in it, not by a type enum. Council is a session mode, not a separate type.
3. **Platform adapters are dual-purpose** — each adapter handles both parsing (inbound) and delivery (outbound)
4. **Streaming via typewriter pattern** — send initial message, edit as content grows (respects platform limits)
5. **Envvar expansion in config** — `${VAR}` syntax for secrets
6. **Hook system for extensibility** — auto-council, memory storage, error alerts
7. **Rate limiting** — per-user, per-platform rate limits
8. **Mirror system** — cross-platform message forwarding with filters
9. **Cron as first-class** — cron jobs can trigger prompts, councils, health checks, memory maintenance
10. **Profile isolation** — each profile runs its own gateway instance with its own config

---

## Related


### Other Parts

- [Message Protocol & Gateway](./00-message-protocol-gateway.md)
- [Platform Adapters & Routing](./01-platform-adapters-routing.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S050200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |