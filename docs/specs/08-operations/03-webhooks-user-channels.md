---
status: draft
created: '19'
updated: '2026-08-11'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080300, 080301, 080302]
---

# Plan: Autonomous Mode & User Interaction: Webhooks & User Channels

**Scope:** Daemon lifecycle, cron scheduling, webhook event processing, multi-platform user interaction channels (Telegram, Discord, Slack, Web UI, CLI), council mode from chat platforms, health monitoring, and the complete message flow. Covers both daemon navis (with thought queue) and one-off navis.

> **Part of:** [Plan: Autonomous Mode & User Interaction](./README.md) — split into 3 parts for readability.

## 00 Webhook Events — External System Triggers

Webhooks are HTTP POST requests from external systems (CI/CD, monitoring, IoT). The Gateway receives them and routes to the appropriate agent session for processing.

### 4.1 Webhook Reception Flow

```go
type WebhookHandler struct {
    router     *MessageRouter
    auth       *AuthManager
    hookRegistry *HookRegistry
}

func (h *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
    // 1. Verify HMAC signature (security)
    if !h.verifySignature(r) {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }

    // 2. Parse payload into unified Message format
    body, _ := io.ReadAll(r.Body)
    msg, err := h.router.ParseWebhook(body, r.Header)
    if err != nil {
        http.Error(w, "Invalid webhook", http.StatusBadRequest)
        return
    }

    // 3. Authenticate sender (optional — some webhooks are unauthenticated system events)
    if err := h.auth.Check(msg.From); err != nil && !h.isSystemWebhook(r) {
        http.Error(w, "Unauthorized", http.StatusForbidden)
        return
    }

    // 4. Fire webhook_in hook (before processing)
    h.hookRegistry.Fire(HookWebhookIn, msg)

    // 5. Route based on target navi type
    targetNavi := h.router.ResolveNavi(msg)
    if targetNavi.HasGateway() {
        // Daemon navi: enqueue on thought queue
        thought := &Thought{
            ID:       uuid.New().String(),
            Prompt:   msg.Content.Text,
            Priority: h.estimatePriority(msg),
            Status:   ThoughtPending,
            CreatedAt: time.Now(),
        }
        if err := targetNavi.Queue.Enqueue(thought); err != nil {
            http.Error(w, "Processing error", http.StatusInternalServerError)
            return
        }
        w.WriteHeader(http.StatusAccepted)
        fmt.Fprintf(w, `{"status": "accepted", "thought_id": "%s"}`, thought.ID)
    } else {
        // One-off navi: trigger directly
        h.oneoffHandler.Trigger(targetNavi.Name, msg.Content.Text)
        w.WriteHeader(http.StatusAccepted)
        fmt.Fprint(w, `{"status": "accepted"}`)
    }
}
```

### 4.2 Webhook Payload Examples

**GitHub push event → project management navi:**
```json
{
    "event_type": "github_push",
    "repo": "flintwillow-software/engineering-workspace",
    "branch": "feature/openapi-spec",
    "author": "alice",
    "commit_message": "Add autonomous mode interaction spec (19-autonomous-mode-interaction.md)",
    "files_changed": 3,
    "timestamp": "2026-07-30T12:30:00Z"
}
```

**Grafana alert → oncall navi:**
```json
{
    "event_type": "grafana_alert",
    "alert_name": "HighErrorRate",
    "severity": "critical",
    "service": "payment-api",
    "message": "Error rate exceeded 5% for 5 minutes on payment-api",
    "dashboard_url": "https://grafana.internal/d/payment-api",
    "timestamp": "2026-07-30T12:45:00Z"
}
```

**IoT sensor → home assistant navi:**
```json
{
    "event_type": "home_assistant_event",
    "entity_id": "binary_sensor.front_door",
    "state": "on",
    "attributes": {
        "friendly_name": "Front Door",
        "battery_level": 85
    },
    "timestamp": "2026-07-30T19:00:00Z"
}
```

### 4.3 Webhook Response Patterns

| Pattern | HTTP Status | Description | Use Case |
|---------|-------------|-------------|----------|
| **Sync** | `200 OK` with JSON body | Wait for agent to finish, return response inline | CI/CD status checks |
| **Async** | `202 Accepted` | Process in background, deliver via platform later | Long-running analysis |
| **Fire-and-forget** | `204 No Content` | Acknowledge receipt, no delivery configured | Internal health pings |

### 4.4 Webhook Security

```yaml
# ~/.opennavi/profiles/<name>/gateway.yaml
gateway:
  webhook:
    enabled: true
    path: "/webhook"              # HTTP endpoint path
    port: 8080                    # listening port (or use HTTPS port)
    tls: true                     # require TLS in production
    hmac_secret: "${WEBHOOK_SECRET}"   # verify incoming signatures
    allowed_origins:             # CORS origins for browser-initiated webhooks
      - "https://ci.example.com"
      - "https://monitoring.example.com"

    # Per-source override config (optional)
    sources:
      github:
        hmac_secret: "${GITHUB_WEBHOOK_SECRET}"
        events: ["push", "pull_request"]
      grafana:
        hmac_secret: "${GRAFANA_ALERT_SECRET}"
        events: ["alert_firing", "alert_resolved"]

    # Rate limiting for webhooks (separate from user rate limits)
    rate_limit:
      requests_per_minute: 100
      burst_size: 50
```

### Requirement: Webhook Reception with Verification
The system SHALL accept webhook HTTP POST requests from external systems and verify the HMAC signature before processing, rejecting unverified requests with HTTP 401.

#### Scenario: Webhook Signature Verification
- GIVEN an incoming webhook request
- WHEN it arrives at the webhook endpoint
- THEN the system SHALL verify the HMAC signature using the configured secret
- AND it SHALL respond HTTP 401 Unauthorized if verification fails
- AND it SHALL parse the payload into a unified `Message` format, responding HTTP 400 for invalid payloads
- AND it SHALL authenticate the sender unless the webhook is a recognized unauthenticated system event, responding HTTP 403 when unauthorized

### Requirement: Webhook Routing by Navi Type
The system SHALL route webhook messages based on the target navi type: enqueue a thought on the daemon navi's thought queue, or trigger a one-off navi directly.

#### Scenario: Webhook to Daemon Navi
- GIVEN a webhook message targeting a daemon navi
- WHEN it is processed
- THEN the system SHALL create a `Thought` with unique ID, prompt, estimated priority, and pending status, and enqueue it on the navi's thought queue
- AND it SHALL respond HTTP 202 Accepted with the `thought_id` in the JSON body
- AND when the message targets a one-off navi, it SHALL trigger the navi directly and respond HTTP 202 Accepted

### Requirement: Webhook Response Patterns
The system SHALL support three webhook response patterns: synchronous (200 with response body), asynchronous (202, deliver later via platform), and fire-and-forget (204, no delivery).

#### Scenario: Response Pattern Selection
- GIVEN a webhook caller with a defined expectation
- WHEN the webhook is processed
- THEN a caller expecting a synchronous result SHALL receive HTTP 200 with the agent's response inline
- AND a long-running analysis SHALL return HTTP 202 and deliver the result later via the user's platform
- AND a fire-and-forget caller SHALL receive HTTP 204 with no delivery configured

### Requirement: Webhook Security Configuration
The system SHALL support per-source webhook security configuration: TLS, global and per-source HMAC secrets, allowed CORS origins, event filters, and rate limiting separate from user rate limits.

#### Scenario: Webhook Security Setup
- GIVEN a gateway with webhooks enabled
- WHEN its `gateway.yaml` is loaded
- THEN the system SHALL honor `enabled`, `path`, `port`, `tls`, `hmac_secret`, and `allowed_origins` settings
- AND it SHALL honor per-source overrides (e.g., github, grafana) with their own HMAC secrets and allowed events
- AND it SHALL enforce the configured rate limit (default 100 requests/minute with burst 50) independent of user rate limits

---


## 01 User Interaction Channels

Users interact with the autonomous navi through any of these channels simultaneously. Each platform is compiled as a separate interface adapter and routes messages into the same unified message flow.

### 5.1 Channel Summary

| Channel | Type | When Used | Compilation Size |
|---------|------|-----------|-----------------|
| **Telegram** | Bot adapter (polling/webhook) | User DMs or channel messages | ~300KB |
| **Discord** | Bot adapter (WebSocket + REST) | Server channels, @mentions, slash commands | ~800KB |
| **Slack** | Bot adapter (Socket mode / webhook) | Slack channels, DMs, slash commands | ~400KB |
| **Web UI** | HTTP server + WebSocket | Browser-based remote access | ~500KB |
| **CLI/TUI** | Interactive terminal | Direct manual interaction alongside daemon | ~2MB (largest single interface) |

### 5.2 Telegram Interaction

Users message the navi's bot directly:

```
User: @oncall_bot check the payment-api health dashboard
    ↓
Telegram Adapter receives message via long-polling/webhook
    ↓
Parsed into unified Message, routed to user's session
    ↓
Agent processes (queries monitoring tools)
    ↓
Response streamed back to Telegram as edited messages
```

**Capabilities:**
- Direct messages for one-on-one conversations (persistent session per user)
- Channel/group chat with @mention routing
- Slash commands: `/status`, `/sessions`, `/compact`
- File/image attachment support (user can send screenshots for analysis)
- Streaming responses (message is edited in-place as agent thinks)

**Telegram delivery limitations:**
- Max 4096 characters per message — long responses split into multiple messages
- Typewriter pattern: initial "Thinking..." message, then edit in-place every 2 seconds
- MarkdownV2 or HTML formatting only

### 5.3 Discord Interaction

Users interact through configured channels on the bot's server:

```
User (in #incidents): /triage this alert from Grafana
    ↓
Discord Adapter receives via WebSocket/REST, parses interaction
    ↓
Parsed into unified Message with command metadata
    ↓
Command handler routes to agent session
    ↓
Agent processes and delivers response to channel
```

**Capabilities:**
- Channel-based conversations (each Discord channel = separate conversation context)
- Per-channel persona overrides via config:
  ```yaml
  channels:
    - id: "123456789"
      name: "incidents"
      persona: "On-call engineer. Calm, precise, action-oriented."
    - id: "987654321"
      name: "general"
      # uses default persona from agents.yaml
  ```
- Slash commands: `/triage`, `/status`, `/council coder researcher`
- @mention routing to specific sub-agents (e.g., `@coder implement this`)
- File attachment support with drag-and-drop

**Discord delivery limitations:**
- Max 2000 characters per message — long responses split into chunks
- Streaming uses message editing (up to ~5 edits/min before rate limit)
- Discord markdown subset only (no tables, limited formatting)

### 5.4 Slack Interaction

Users interact through configured Slack app:

```
User (in #agent-work): /triage new alert from monitoring service
    ↓
Slack Adapter receives via Socket mode/webhook
    ↓
Parsed into unified Message with slash command metadata
    ↓
Agent processes and delivers response to channel
```

**Capabilities:**
- Slash commands defined in config:
  ```yaml
  slack:
    enabled: true
    token: "${SLACK_BOT_TOKEN}"
    signing_secret: "${SLACK_SIGNING_SECRET}"
    commands:
      - name: "triage"
        prompt: "Classify this alert and provide triage advice."
      - name: "status"
        prompt: "Show current on-call status and recent alerts."
  ```
- Direct messages for one-on-one conversations (persistent session per user)
- Thread-based replies for incident tracking
- Rich Slack formatting support

**Slack delivery limitations:**
- Max 30,000 characters per message (much higher than Telegram/Discord)
- Socket mode preferred over webhook for real-time interaction
- mrkdwn format for messages (Slack markdown subset)

### 5.5 Web UI Interaction

Users access the navi through a browser:

```
User → http://localhost:8080 → HTTP GET / (serves embedded frontend)
    ↓
WebSocket connection established: ws://localhost:8080/ws
    ↓
User types message in chat interface
    ↓
Message sent over WebSocket to Gateway
    ↓
Agent processes, events streamed back via WebSocket
    ↓
Streaming updates rendered in browser chat view
```

**Capabilities:**
- Full chat interface with markdown rendering
- Session management (list, switch, fork sessions)
- Model selector dropdown
- Settings panel (temperature, max tokens, system prompt override)
- File attachment via drag-and-drop or paste
- Cross-platform access from any device on the network

**Web UI configuration:**
```yaml
webui:
  enabled: true
  port: 8080
  addr: "0.0.0.0"              # bind address (use specific IP for security)
  auth:
    type: "token"                # "basic", "token", or "none" (development only)
    token: "${WEBUI_TOKEN}"      # Bearer token for API access
```

### 5.6 CLI/TUI Interaction (Hybrid Mode)

The CLI interface is available even when running as a daemon — useful for developers and operators who want to interact manually alongside autonomous processing:

```bash
# Run the navi in interactive mode alongside its webhook/cron handlers
$ ./oncall-bot --profile work --interactive

# Or start daemon, then attach via CLI
$ opennavi gateway start --profile work    # background daemon
$ opennavi chat                            # connect to running gateway's TUI
```

**Capabilities:**
- Full TUI with streaming output, collapsible tool results, session branching
- Works when daemon is also processing webhooks/cron in the same process
- `--interactive` flag selects only CLI interface (no network listeners) for pure interactive mode
- Session switching via `/sessions`, branching via keyboard shortcuts

**Hybrid use case:** An oncall navi receives Grafana alerts via webhook autonomously, but the on-call engineer can hop into TUI to have planning conversations: "What's been happening overnight?" while webhooks keep firing in the background.

### Requirement: Multi-Platform Channel Support
The system SHALL support simultaneous user interaction through Telegram, Discord, Slack, Web UI, and CLI/TUI adapters, each compiled as a separate interface adapter routing messages into the unified message flow.

#### Scenario: Channel Interaction
- GIVEN a user on any supported platform (Telegram, Discord, Slack, Web UI, CLI)
- WHEN they send a message to the navi
- THEN the corresponding adapter SHALL receive and parse the message
- AND the message SHALL enter the same unified message flow as all other channels
- AND the response SHALL be delivered back to that platform with its native delivery mechanism

### Requirement: Platform Capabilities and Delivery Constraints
The system SHALL provide per-platform capabilities (direct messages, channel/group chat, slash commands, @mention routing, attachments, streaming) and SHALL respect each platform's delivery limits.

#### Scenario: Platform Delivery Limits
- GIVEN a long response for a specific platform
- WHEN it is delivered
- THEN Telegram responses SHALL be split when exceeding 4096 characters and streamed via the typewriter edit pattern
- AND Discord responses SHALL be split when exceeding 2000 characters with edits limited to ~5 per minute
- AND Slack SHALL use `chat.update` (up to 30,000 characters) with thread replies for incident context
- AND Web UI SHALL stream via WebSocket in real time with full markdown rendering
- AND CLI/TUI SHALL render per-content-delta events with collapsible tool output blocks

---


## 02 Unified Message Flow

All interaction channels converge at a single point — every message, regardless of origin, becomes a unified `Message` type and flows through the same processing pipeline.

### 6.1 Complete Message Lifecycle

```
                    ┌─────────────────────────────┐
                    │   External Event Sources    │
                    ├─────────────────────────────┤
                    │ Telegram │ Discord │ Slack  │ User (CLI/WebUI)
                    │ Webhook │ Cron Job │ Agent
                    └──────────┬──────────────────┘
                               │
                               ▼
                    ┌─────────────────────────────┐
                    │   Platform Adapter          │
                    │   (parse raw → ParsedMsg)  │
                    └──────────┬──────────────────┘
                               │ RawMessage {adapter, payload}
                               ▼
                    ┌─────────────────────────────┐
                    │   MessageRouter.Parse()     │
                    │   Platform format →         │
                    │   unified Message           │
                    └──────────┬──────────────────┘
                               │ Message{Type, From, To, Content}
                               ▼
                    ┌─────────────────────────────┐
                    │   AuthManager.Check()       │
                    │   (allowlist, pairing,      │
                    │    rate limit)              │
                    └──────────┬──────────────────┘
                               │ ✓ authorized
                               ▼
                    ┌─────────────────────────────┐
                    │   HookSystem.Fire(HookMsgIn)│
                    │   (auto-council, etc.)      │
                    └──────────┬──────────────────┘
                               ▼
                    ┌─────────────────────────────┐
                    │   MessageRouter.Resolve()   │
                    │   Find/create session       │
                    │   for this user/platform    │
                    └──────────┬──────────────────┘
                               │ SessionKey{platform, user, channel}
                               ▼
                    ┌─────────────────────────────┐
                    │   SessionManager            │
                    │   GetOrCreate(sessionKey)   │
                    └──────────┬──────────────────┘
                               │ ChatSession (loaded or new)
                               ▼
                    ┌─────────────────────────────┐
                    │   AgentPool.RouteToAgent()  │
                    │   If agent running → steer  │
                    │   Queue as follow-up        │
                    │   Else → run agent loop     │
                    └──────────┬──────────────────┘
                               │
                    ┌────────────┴────────────────┐
                    ▼                             ▼
            Agent Running?              Agent Idle?
                │                           │
                ▼                           ▼
        ┌───────────────┐          ┌───────────────┐
        │ SteeringQueue │          │ Prompt(agent) │
        │ (add msg as   │          │ agent loop    │
        │  follow-up)   │          │ runs, streams │
        └───────────────┘          │ events back   │
                                   └───────┬───────┘
                                           ▼
                    ┌─────────────────────────────┐
                    │   Event Subscription →      │
                    │   DeliveryRegistry.Deliver()│
                    │   (platform-specific)       │
                    └──────────┬──────────────────┘
                               │
                    ┌────────────┴────────────────┐
                    ▼                             ▼
            Telegram                Discord / Slack / WebUI
        "Thinking..." →           Message edit,
        Typewriter edit           multi-chunk send

            (user gets streaming response on their platform)
```

### 6.2 Platform-Specific Streaming Behavior

| Platform | Streaming Method | Edit Frequency | Max Per-Message | Notes |
|----------|-----------------|----------------|-----------------|-------|
| Telegram | Typewriter edit of initial msg | Every 2s | 4096 chars | Split into new messages when exceeded |
| Discord | Message edit on original msg | Every 3s | 2000 chars | Rate limit: ~5 edits/min per channel |
| Slack | `chat.update` API call | Every 3s | 30,000 chars | Thread reply for incident context |
| Web UI | WebSocket SSE events → DOM | Real-time (16ms) | Unlimited | Full markdown rendering |
| CLI/TUI | Event stream → component render | Per-content-delta | Terminal width | Collapsible tool output blocks |

### 6.3 Steering During Autonomous Processing

When a user sends a message while the agent is already processing another event (webhook or cron):

```go
func (pool *AgentPool) RouteToAgent(sessionID string, msg *Message) error {
    session, _ := pool.GetOrCreate(sessionID)

    if session.Status == StatusRunning {
        // Agent is busy — queue as steering message
        session.Agent.Steer(msg.Content.Text)
        log.Printf("steering: user %s/%s queued while agent running on %s",
            msg.From.Platform, msg.From.UserID, msg.Metadata["context"])

        // User gets immediate feedback that their message was received
        pool.deliverToPlatform(msg.To.Platform, msg.To.SessionID,
            MessageContent{Text: "Got it — I'm working on something right now."})
        return nil
    }

    // Agent is idle — run immediately
    session.Status = StatusRunning
    go func() {
        defer func() { session.Status = StatusIdle }()
        output, err := session.Agent.Prompt(msg.Content.Text)
        if err != nil {
            pool.deliverError(msg.To.Platform, msg.To.SessionID, err)
            return
        }
        pool.deliverResponse(msg.To.Platform, msg.To.SessionID, output)
    }()

    return nil
}
```

### Requirement: Unified Message Pipeline
The system SHALL convert every message from every origin (platform adapter, webhook, cron, agent) into a unified `Message` type and route it through the same pipeline: parse, auth check, hook fire, session resolution, and agent routing.

#### Scenario: Message Lifecycle
- GIVEN a raw message from any source
- WHEN it enters the gateway
- THEN the platform adapter SHALL parse it into a `RawMessage`
- AND the `MessageRouter.Parse` SHALL convert it to a unified `Message`
- AND the `AuthManager` SHALL check authorization (allowlist, pairing, rate limit)
- AND the hook system SHALL fire `HookMsgIn`
- AND the router SHALL resolve or create the user's session via `SessionManager.GetOrCreate`
- AND the `AgentPool.RouteToAgent` SHALL run the agent or steer it if busy

### Requirement: Steering During Autonomous Processing
The system SHALL queue a user message as a steering message when the agent is already processing another event, delivering immediate feedback that the message was received.

#### Scenario: Steering While Busy
- GIVEN an agent currently processing a webhook or cron event
- WHEN the user sends a new message to that session
- THEN the system SHALL add the message to the agent's steering queue with context metadata
- AND it SHALL deliver immediate feedback to the user ("Got it — I'm working on something right now.")
- AND when the agent is idle, it SHALL run the prompt immediately, delivering the response or error back to the platform

---


---

## Related


### Other Parts

- [Daemon Lifecycle & Cron](./01-daemon-cron.md)
- [Council, Health & Implementation](./04-council-health-impl.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080300 | 0.1.0 | Migrated from docs/spec/ to spec-tree |