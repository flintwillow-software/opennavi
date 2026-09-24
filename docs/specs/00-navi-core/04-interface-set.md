---
status: draft
created: '2026-07-29'
updated: '2026-09-23'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000400, 000401, 000402, 000403, 000404, 000405, 000406, 000407]
---

# Interface Set

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Overview

Interfaces are **delivery modes** that get compiled into the navi binary. A navi can have any combination of interfaces enabled — only selected ones are included in the final executable. This controls binary size, attack surface, and runtime behavior.

The interface set is defined in the navi definition:

```go
interfaces: InterfaceSet{
    CLI:     true,
    WebUI:   false,
    Gateway: InterfaceConfig{Enabled: true, HTTPPort: 443},
    Cron:    InterfaceConfig{Enabled: true},
}
```

### Requirement: Delivery Mode Compilation
The system SHALL compile only the interfaces selected in the navi definition into the final executable.

#### Scenario: Enabling a subset of interfaces
- **GIVEN** a navi definition enables only a subset of available interfaces
- **WHEN** the builder compiles the navi
- **THEN** only the selected interfaces SHALL be included in the final executable
- **AND** the binary size and attack surface SHALL reflect the selected interfaces only

---

## 01 Available Interfaces

### 1. CLI (Interactive TUI)

**What it does:** Provides an interactive terminal chat interface with differential rendering, component system, and keyboard shortcuts.

**When to use:** When users need to manually interact with the navi — triage alerts, plan projects, have conversations.

**Compiled components:**
- Terminal I/O (raw mode, bracketed paste, synchronized output)
- TUI framework (component system, overlays, rendering engine)
- Chat view (message display, markdown rendering)
- Editor widget (for multi-line input)
- Status bar (token count, cost, model info)
- Modal overlays (model selector, settings panel)

**Binary size impact:** ~2MB (largest single interface due to TUI framework)

```go
interfaces: InterfaceSet{
    CLI: true,  // enables full TUI mode
    
    // Optional: TUI-specific config
    CLIConfig: InterfaceConfig{
        Theme: "dark",           // default theme
        FontSize: 14,            // terminal font size hint
        MaxMessageWidth: 80,     // wrap messages at N chars
    },
}
```

**User experience:** The TUI is a **portal over the thought queue** (S000406), not a chat transcript: the queue view is the home screen; the live view hops into the running thought (streaming content, tool calls); messages attach to, steer, or schedule thoughts. `opennavi` (binary run) launches the TUI attached to the navi's in-process queue; the same TUI attached remotely (S000407) shows the identical view of a navi running elsewhere. Tools execute in background with collapsible output. Session branching via keyboard shortcuts.

---

### 2. Web UI

**What it does:** HTTP server serving a web-based chat interface with WebSocket for real-time updates.

**When to use:** Remote access, multi-user scenarios, or when TUI isn't available (e.g., on headless servers).

**Compiled components:**
- Go HTTP server (from stdlib)
- WebSocket hub for real-time message streaming
- REST API endpoints (session management, settings)
- Embedded static assets (HTML/CSS/JS frontend)
- Authentication (basic or token-based)

**Binary size impact:** ~500KB (embedded web assets)

```go
interfaces: InterfaceSet{
    WebUI: InterfaceConfig{
        Enabled: true,
        Port: 8080,
        Addr: "0.0.0.0",  // or specific interface
        Auth: AuthConfig{
            Type: "token",  // "basic" or "token"
            Token: "${WEBUI_TOKEN}",
        },
    },
}
```

**User experience:** Navigate to `http://localhost:8080` in a browser. Chat interface with session management, model selection, and settings panel. Same functionality as TUI but web-based.

---

### 3. Gateway (Webhook Daemon)

**What it does:** Long-running daemon that receives webhooks, routes messages to the agent, and delivers responses via configured platforms.

**When to use:** Event-driven automation — monitoring alerts, CI/CD triggers, IoT events, or any external system sending HTTP POST requests.

**Compiled components:**
- HTTP server (from stdlib)
- TLS/mTLS handling (for secure webhook endpoints)
- Webhook parser (HMAC verification, payload validation)
- Message router (parse → resolve → deliver)
- Platform adapter base (Telegram/Discord/Slack delivery)
- Hook system (lifecycle event handlers)

**Binary size impact:** ~1.5MB (TLS + message routing)

```go
interfaces: InterfaceSet{
    Gateway: InterfaceConfig{
        Enabled:       true,
        HTTPPort:      443,
        HTTPS:         true,  // enable TLS
        
        TLSCert: "/etc/ssl/certs/gateway.pem",
        TLSKey:  "/etc/ssl/private/gateway.key",
        
        WebhookSecret: "${WEBHOOK_SECRET}",  // HMAC verification key
        
        // Platform delivery (optional — which platforms to send responses to)
        DeliveryPlatforms: []string{"telegram", "discord"},
    },
}
```

**User experience:** Navi runs as a daemon (`./oncall-bot` or systemd service). Exposes webhook endpoint at `https://your-server/webhook`. External systems POST events → navi processes them → delivers responses to Telegram/Discord/etc. Cron scheduler also runs in this mode for periodic tasks.

---

### 4. Cron (Scheduled Tasks)

**What it does:** Periodic task execution within the Gateway or standalone mode. Runs agent sessions on a schedule.

**When to use:** Health checks, periodic monitoring, scheduled reporting, or any recurring autonomous workflow.

**Compiled components:**
- Scheduler (cron expression parsing, timer management)
- Task registry (scheduled jobs with handlers)
- Session manager integration (create isolated sessions per run)

**Binary size impact:** ~50KB (tiny — just scheduler logic)

```go
interfaces: InterfaceSet{
    Cron: InterfaceConfig{
        Enabled: true,
        
        // Tasks defined in navi code or config
        Tasks: []CronTask{
            {
                Name: "health-check",
                Schedule: "*/5 * * * *",  // every 5 minutes
                AgentPrompt: "Check service health. Query monitoring tools for uptime, error rates, and resource usage.",
                DeliveryPlatforms: ["telegram"],  // send results to Telegram
            },
        },
    },
}

// Or defined in code:
func init() {
    sdk.RegisterCronTask(CronTask{
        Name: "health-check",
        Schedule: "*/5 * * * *",
        Handler: func(ctx context.Context) (*AgentResponse, error) {
            // Custom handler logic
        },
    })
}
```

**User experience:** Navi runs as daemon with Gateway interface. Cron tasks execute on schedule, agent processes them, responses delivered to configured platforms. No user interaction needed.

---

### 5. Telegram (Bot Adapter)

**What it does:** Telegram bot adapter for receiving messages and delivering responses via the Telegram API.

**When to use:** When users want to interact with the navi through Telegram. Often combined with Gateway interface for autonomous event handling.

**Compiled components:**
- Telegram Bot API client
- Long-polling or webhook mode
- Message formatting (markdown support)
- Command handler (/start, /help, etc.)

**Binary size impact:** ~300KB

```go
interfaces: InterfaceSet{
    Telegram: InterfaceConfig{
        Enabled: true,
        Token: "${TELEGRAM_BOT_TOKEN}",
        Mode: "webhook",  // or "polling"
        
        WebhookURL: "https://your-server/telegram/webhook",
        WebhookSecret: "${TELEGRAM_WEBHOOK_SECRET}",
    },
}
```

**User experience:** Users message the bot on Telegram. Navi responds with streaming output. Commands like `/status`, `/sessions` available if configured. Can receive webhook events from other systems and relay to Telegram users.

---

### 6. Discord (Bot Adapter)

**What it does:** Discord bot adapter for receiving messages and delivering responses via the Discord Gateway or REST API.

**When to use:** Team collaboration, incident response channels, project management discussions.

**Compiled components:**
- Discord Gateway WebSocket client (for real-time messaging)
- Discord REST API client (for commands, file uploads)
- Message formatting (Discord markdown subset)
- Permission handling (role-based access)

**Binary size impact:** ~800KB (WebSocket client is larger than HTTP clients)

```go
interfaces: InterfaceSet{
    Discord: InterfaceConfig{
        Enabled: true,
        Token: "${DISCORD_BOT_TOKEN}",
        
        // Channel-specific config (optional)
        Channels: []ChannelConfig{
            {ID: "123456789", Name: "incidents", Prompt: "You are an on-call bot. Classify alert severity and provide triage advice."},
            {ID: "987654321", Name: "general",  Prompt: ""},  // uses default personality
        },
    },
}
```

**User experience:** Navi runs as a Discord bot. Users message in configured channels. Agent responds with streaming output. Can be triggered by @mentions for specific tasks (e.g., `/triage`, `/status`).

---

### 7. Slack (Bot Adapter)

**What it does:** Slack bot adapter for receiving messages and delivering responses via the Slack API.

**When to use:** Enterprise integration, Slack-based incident response, project management workflows.

**Compiled components:**
- Slack Web API client
- Socket mode or webhook mode
- Message formatting (Slack markdown)
- Slash command handler (/oncall-triage, etc.)

**Binary size impact:** ~400KB

```go
interfaces: InterfaceSet{
    Slack: InterfaceConfig{
        Enabled: true,
        Token: "${SLACK_BOT_TOKEN}",
        SigningSecret: "${SLACK_SIGNING_SECRET}",  // for HMAC verification
        
        // Slash commands
        Commands: []Command{
            {Name: "triage", Prompt: "Classify this alert and provide triage advice."},
            {Name: "status", Prompt: "Show current on-call status."},
        },
    },
}
```

**User experience:** Navi runs as a Slack app. Users interact via slash commands or direct messages. Responses stream in real-time. Can be triggered by webhook events (CI/CD failures, monitoring alerts).

### Requirement: CLI Interface
The system SHALL provide an interactive terminal chat interface with differential rendering, a component system, and keyboard shortcuts when the CLI interface is compiled in.

#### Scenario: Interacting via TUI
- **GIVEN** a navi binary with the CLI interface compiled in
- **WHEN** the user launches the navi
- **THEN** the navi SHALL present an interactive terminal chat interface
- **AND** the user SHALL be able to type prompts, receive streaming responses, and use keyboard shortcuts for session branching

### Requirement: Web UI Interface
The system SHALL provide a web-based chat interface served over HTTP with WebSocket for real-time updates and authentication when the WebUI interface is compiled in.

#### Scenario: Accessing the web interface
- **GIVEN** a navi binary with the WebUI interface compiled in
- **WHEN** the user navigates to the configured address and port
- **THEN** the navi SHALL serve the web chat interface
- **AND** the interface SHALL support session management, model selection, and settings
- **AND** the interface SHALL require authentication when configured

### Requirement: Gateway Webhook Daemon
The system SHALL provide a long-running webhook daemon that receives webhooks, validates them via HMAC, routes messages to the agent, and delivers responses to configured platforms when the Gateway interface is compiled in.

#### Scenario: Receiving a webhook event
- **GIVEN** a navi runs as a daemon with the Gateway interface enabled
- **WHEN** an external system POSTs an event to the webhook endpoint
- **THEN** the navi SHALL validate the webhook payload, route the message to the agent, and deliver the response to the configured delivery platforms

### Requirement: Cron Scheduled Tasks
The system SHALL execute agent sessions on a schedule defined by cron expressions when the Cron interface is compiled in.

#### Scenario: Running a scheduled task
- **GIVEN** a navi definition registers a cron task with a schedule
- **WHEN** the schedule triggers
- **THEN** the navi SHALL run the agent session for the task
- **AND** the navi SHALL deliver the response to the task's configured platforms

### Requirement: Telegram Bot Adapter
The system SHALL provide a Telegram bot adapter supporting long-polling or webhook mode and message formatting when the Telegram interface is compiled in.

#### Scenario: Interacting via Telegram
- **GIVEN** a navi binary with the Telegram interface compiled in and a bot token configured
- **WHEN** a user sends a message to the bot on Telegram
- **THEN** the navi SHALL respond with streaming output
- **AND** the navi SHALL support configured commands such as `/status` and `/sessions`

### Requirement: Discord Bot Adapter
The system SHALL provide a Discord bot adapter with real-time messaging, permission handling, and channel-specific configuration when the Discord interface is compiled in.

#### Scenario: Interacting via Discord
- **GIVEN** a navi binary with the Discord interface compiled in and a bot token configured
- **WHEN** a user messages in a configured channel
- **THEN** the navi SHALL respond with streaming output
- **AND** the navi SHALL apply channel-specific prompts when configured

### Requirement: Slack Bot Adapter
The system SHALL provide a Slack bot adapter with socket or webhook mode, signing secret verification, and slash command handling when the Slack interface is compiled in.

#### Scenario: Interacting via Slack slash command
- **GIVEN** a navi binary with the Slack interface compiled in and a signing secret configured
- **WHEN** a user invokes a configured slash command
- **THEN** the navi SHALL verify the request signature
- **AND** the navi SHALL execute the command's prompt and stream the response

---

## 02 Interface Combinations

Not all interfaces need to be enabled together. Common combinations:

### Interactive Only
```go
interfaces: InterfaceSet{
    CLI: true,  // TUI mode only
}
// Binary: ~2MB, runs as ./oncall-bot (interactive)
```

### Daemon Only (Event-Driven)
```go
interfaces: InterfaceSet{
    Gateway: InterfaceConfig{Enabled: true},
    Cron:    InterfaceConfig{Enabled: true},
}
// Binary: ~1.5MB, runs as daemon with webhook + scheduler
```

### Hybrid (Interactive + Daemon)
```go
interfaces: InterfaceSet{
    CLI:     true,  // manual interaction available
    Gateway: InterfaceConfig{Enabled: true},
    Cron:    InterfaceConfig{Enabled: true},
}
// Binary: ~3MB, can be run interactively or as daemon
```

### Multi-Platform Delivery
```go
interfaces: InterfaceSet{
    Gateway:  InterfaceConfig{Enabled: true},  // webhook receiver
    Telegram: InterfaceConfig{Enabled: true},  // deliver to Telegram
    Discord:  InterfaceConfig{Enabled: true},  // deliver to Discord
}
// Binary: ~2.5MB, receives webhooks and delivers to multiple platforms
```

### Requirement: Multiple Interface Combinations
The system SHALL support any combination of interfaces, including interactive-only, daemon-only, hybrid interactive+daemon, and multi-platform delivery combinations.

#### Scenario: Hybrid interactive and daemon mode
- **GIVEN** a navi definition enables CLI, Gateway, and Cron interfaces
- **WHEN** the navi is built
- **THEN** the navi SHALL be runnable interactively and as a daemon
- **AND** the navi SHALL receive webhooks and execute scheduled tasks

#### Scenario: Multi-platform delivery
- **GIVEN** a navi definition enables Gateway, Telegram, and Discord interfaces
- **WHEN** the navi receives a webhook
- **THEN** the navi SHALL deliver responses to Telegram and Discord

---

## 03 Interface Dependencies

Some interfaces have implicit dependencies:

| Enabled | Automatically Includes |
|---------|----------------------|
| Gateway + HTTPS | TLS/mTLS handling |
| WebUI | HTTP server (from stdlib) |
| Telegram/Discord/Slack | Message router (for multi-platform delivery) |

The builder resolves these automatically. You don't need to explicitly enable dependencies — they're pulled in when needed.

### Requirement: Automatic Dependency Resolution
The system SHALL automatically include implicit interface dependencies, such as TLS/mTLS handling for Gateway with HTTPS and the message router for platform adapters.

#### Scenario: Enabling Gateway with HTTPS
- **GIVEN** a navi definition enables Gateway with HTTPS
- **WHEN** the builder compiles the navi
- **THEN** the builder SHALL automatically include TLS/mTLS handling

#### Scenario: Enabling a platform adapter
- **GIVEN** a navi definition enables Telegram, Discord, or Slack
- **WHEN** the builder compiles the navi
- **THEN** the builder SHALL automatically include the message router for multi-platform delivery

---

## 04 Interface Configuration Reference

```go
type InterfaceSet struct {
    CLI      bool          // Interactive TUI mode
    WebUI    interfaceConfig  // Web chat interface
    Gateway  interfaceConfig  // Webhook daemon
    Cron     interfaceConfig  // Scheduled tasks
    Telegram interfaceConfig  // Telegram bot adapter
    Discord  interfaceConfig  // Discord bot adapter
    Slack    interfaceConfig  // Slack bot adapter
}

type interfaceConfig struct {
    Enabled       bool
    Port          int      // HTTP port (Gateway, WebUI)
    Addr          string   // Bind address (default "0.0.0.0")
    
    // TLS configuration
    HTTPS         bool     // Enable HTTPS (Gateway)
    TLSCert       string   // Path to certificate file
    TLSKey        string   // Path to private key file
    
    // Authentication
    Auth          authConfig  // WebUI authentication
    
    // Platform-specific config
    Token         string      // Bot tokens (Telegram, Discord, Slack)
    WebhookURL    string      // Webhook URL (Telegram, Slack)
    SigningSecret string      // HMAC secret (Slack)
    
    // Delivery platforms
    DeliveryPlatforms []string  // Where to send responses
}
```

### Requirement: Interface Configuration Schema
The system SHALL support the interface configuration fields defined in the reference, including enablement, port, bind address, TLS, authentication, platform tokens, webhook URLs, signing secrets, and delivery platforms.

#### Scenario: Configuring a webhook daemon
- **GIVEN** a navi definition configures Gateway with port, HTTPS, TLS certificate paths, and a webhook secret
- **WHEN** the navi is built and run
- **THEN** the Gateway SHALL listen on the configured port
- **AND** the Gateway SHALL use TLS and verify webhook HMAC signatures with the configured secret

#### Scenario: Configuring a bind address
- **GIVEN** a navi definition sets a bind address for an interface
- **WHEN** the navi starts
- **THEN** the interface SHALL bind to the configured address
- **AND** the interface SHALL default to `0.0.0.0` when no address is set

---

## 06 TUI as Thought-Queue Portal (S000406)

The TUI is a **door into the house** (S010000): a portal over the thought queue, not a chat REPL with an agent behind it.

> S000406 delta (operator decision, 2026-09-23, NAVI-330): the "not a chat REPL" statement refers to the **queue home view** — the TUI is not a general-purpose chat terminal. The **live thought view** (hopping into a running thought) IS now a chat-window **presentation** of the conversation: the thought's prompt as the opening user turn, assistant deltas accumulated into one assistant turn, tool calls as indented blocks, lifecycle transitions as system lines, and steering deliveries as user turns at their injection point. Presentation only — the portal event stream stays the source of truth.

> S000406 delta (CLI channel, 2026-09-24, NAVI-337): `navi enqueue` is a portal **control-channel client** — with a known portal endpoint (`--portal` flag, then `NAVI_PORTAL` env; resolution order documented in the CLI help) it POSTs the enqueue control action (identity `cli`), falling back to the DB mailbox (durable, NAVI-336 reconcile adopts it) with a visible note when the navi is unreachable or the channel fails closed (enqueue is an authorized action; the 403-until-IAM posture keeps that fail-closed).

### Requirement: Queue View as Home

The TUI SHALL present the navi's thought queue as its home view: each thought with id, status, priority, age, checkpoint presence, and session-log pointer, updated live from the queue event fanout.

#### Scenario: Live queue updates

GIVEN the TUI is attached to a navi (in-process binary run or via gateway)
WHEN a thought transitions lifecycle (enqueued, started, paused, completed, …)
THEN the queue view SHALL reflect the transition within one event delivery.

### Requirement: Per-Thought Actions

The TUI SHALL allow the user to act on any thought in the queue: attach a message, bump priority, pause, resume, cancel, and retry a failed thought.

#### Scenario: Message on a queued thought

GIVEN a thought in pending state
WHEN the user attaches a message to it
THEN the system SHALL append the message to the thought's Context for when it runs.

### Requirement: Live Thought View

The TUI SHALL let the user hop into the running thought and observe it live: streamed content deltas, tool calls and results, and lifecycle transitions. Observation is read-only; steering is the write path.

#### Scenario: Hop into the running thought

GIVEN a thought is running
WHEN the user opens the live view
THEN the TUI SHALL stream the thought's agent events (content deltas, tool calls, transitions) as they fire.

### Requirement: Steering (Write Path)

The TUI SHALL inject messages into a running thought through the agent's steering queue (turn-boundary injection), with a queue-level delivery acknowledgment. Steering a paused thought SHALL resume it with the message attached.

#### Scenario: Steer the running thought

GIVEN the live view of a running thought
WHEN the user sends a message
THEN the system SHALL inject it at the next turn boundary and emit a delivery-ack event the TUI displays as "delivered to thought X".

### Requirement: Message Granularity

Connector-sourced messages (gateway, cron, bot adapters, CLI enqueue) SHALL enqueue one thought per message. TUI messages SHALL attach to existing thoughts (context append when queued; steering when running; resume-with-message when paused) or schedule new thoughts — never spawn implicit per-message thoughts.

#### Scenario: Connector message is discrete work

GIVEN a gateway adapter receives an inbound request
WHEN the adapter delivers it
THEN the system SHALL enqueue it as its own thought.

## 07 Remote Portal Attachment (S000407)

The gateway interface SHALL expose a portal-attachment surface so the same TUI manages a navi running elsewhere: an event stream (queue + thought events — the same events a locally-attached TUI consumes in-process) and a control channel (attach message, steer, pause/resume/cancel, enqueue).

### Requirement: Same Portal, Different Transport

The TUI SHALL be transport-agnostic: the identical portal view and actions work in-process (binary run) and over the gateway (remote attachment to a navi running elsewhere).

#### Scenario: Remote TUI mirrors the queue

GIVEN a daemon navi running on another host with the gateway enabled
WHEN a TUI attaches remotely
THEN the TUI SHALL receive the same queue/thought event stream and present the same queue and live views as a local run.

#### Scenario: Remote control acts on the queue

GIVEN a remotely attached TUI
WHEN the user steers, pauses, resumes, cancels, or enqueues
THEN the system SHALL apply the action to the daemon's queue and emit the corresponding queue event back to the attachment.

### Requirement: Attachment Authorization

Remote portal attachments SHALL be subject to the trust and authorization model: which attachment actions a client may perform SHALL align with the IAM access levels (S040200).

#### Scenario: Unauthorized control attempt is refused

GIVEN a portal client without the required trust level
WHEN it attempts a control action (e.g. cancel a thought)
THEN the gateway SHALL refuse the action and the TUI SHALL surface the refusal.

> Wire-protocol details (framing, encoding) are owned by 05-messaging; this subfeature specifies the interface-level capability.

## 08 Next Steps

- **[01-navi-definition.md](./01-navi-definition.md)** — How interfaces fit into the navi definition
- **[02-builder-compiler.md](./02-builder-compiler.md)** — How interface selection affects binary compilation
- **[04-trust-signing-model.md](./04-trust-signing-model.md)** — Security considerations for network-facing interfaces


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000400 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-23 | S000406, S000407 | 0.2.0 | TUI re-spec'd as thought-queue portal (queue home view, per-thought actions, live view, steering, message granularity); gateway gains remote portal attachment with authorization (thought-loop case study) |