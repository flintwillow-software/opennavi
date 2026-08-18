---
status: draft
created: '19'
updated: '2026-08-11'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080400, 080401, 080402, 080403, 080404, 080405, 080406]
---

# Plan: Autonomous Mode & User Interaction: Council, Health & Implementation

**Scope:** Daemon lifecycle, cron scheduling, webhook event processing, multi-platform user interaction channels (Telegram, Discord, Slack, Web UI, CLI), council mode from chat platforms, health monitoring, and the complete message flow. Covers both daemon navis (with thought queue) and one-off navis.

> **Part of:** [Plan: Autonomous Mode & User Interaction](./README.md) — split into 3 parts for readability.

## 00 Council Mode from Chat Platforms

Users can initiate multi-agent council sessions directly from their preferred chat platform. The council session coordinates sub-agents and delivers the synthesized result back to wherever the user is chatting.

### 7.1 Initiating a Council

**From Telegram:** `@oncall_bot @coder @researcher analyze this PR`
- The main agent detects multiple @mentions and starts council mode on that session
- Sub-agents (coder, researcher) process the request concurrently
- Results delivered back to Telegram as threaded messages or edited reply

**From Discord:** `/council coder researcher Review the architecture for X`
- Slash command explicitly triggers council mode
- Council member responses appear in-channel with @mentions
- Main agent synthesizes and delivers final summary

### 7.2 Council Message Flow

```
User: /council coder reviewer "Is this implementation correct?"
    ↓
Discord Adapter → ParsedMessage (command + args)
    ↓
MessageRouter resolves to session, starts council mode
    ↓
CouncilState created with coder + reviewer as participants
    ↓
Main agent sends question to each member:
  @coder: "Is this implementation correct?"
  @reviewer: "Review the code for correctness."
    ↓
@coder responds (via sub-agent loop): [analysis of implementation]
    ↓
@reviewer responds (via sub-agent loop): [code review feedback]
    ↓
Main agent synthesizes: "Implementation is functionally correct but..."
    ↓
Delivered to Discord channel as final council summary
```

### 7.3 Council Delivery Config

Council sessions deliver responses based on where they were initiated, or can be configured to cross-deliver:

```yaml
# ~/.opennavi/profiles/<name>/council.yaml
council:
  # Default delivery: back to where council was started
  default_delivery: "origin"

  # Optionally also deliver to other platforms
  mirror_to:
    - platform: "telegram"
      user_id: "${ADMIN_TELEGRAM_ID}"       # admin gets copies of all councils
    - platform: "discord"
      channel_id: "${AUDIT_CHANNEL_ID}"     # audit log channel

  # Timeout for council rounds (each member has this long to respond)
  round_timeout: "10m"

  # Max rounds before auto-synthesis
  max_rounds: 5
```

### Requirement: Council Initiation from Chat Platforms
The system SHALL allow users to initiate multi-agent council sessions from chat platforms via multiple @mentions (e.g., `@oncall_bot @coder @researcher`) or slash commands (e.g., `/council coder researcher`).

#### Scenario: Council Initiation
- GIVEN a user message on a chat platform
- WHEN the message contains multiple @mentions of sub-agents or a `/council` slash command
- THEN the system SHALL detect the council request and start council mode on that session
- AND it SHALL create a `CouncilState` with the mentioned agents as participants
- AND the main agent SHALL send the question to each member for concurrent processing
- AND results SHALL be delivered back to the originating platform, synthesized into a final summary

### Requirement: Council Delivery Configuration
The system SHALL deliver council responses to the origin platform by default and support mirroring to other platforms, with configurable round timeouts and maximum rounds before auto-synthesis.

#### Scenario: Council Delivery
- GIVEN a council.yaml configuration
- WHEN a council session completes
- THEN the system SHALL deliver the final summary to the origin platform by default (`default_delivery: origin`)
- AND it SHALL mirror copies to configured `mirror_to` platforms (e.g., admin Telegram ID, audit Discord channel)
- AND it SHALL give each member `round_timeout` (default 10m) to respond
- AND it SHALL auto-synthesize after `max_rounds` (default 5)

---


## 01 Health & Operational Monitoring

The autonomous gateway exposes multiple ways to check its health and operational status.

### 8.1 CLI Status Commands

```bash
# Full health report (adapters, sessions, agents, providers)
$ opennavi health

Profile: work
Status: running
Uptime: 3d 14h 22m

Adapters:
  ✓ Telegram    connected, bot: @oncall_bot
  ✓ Discord     connected, guild: My Server
  ✗ Slack       disconnected: token expired (since 2h ago)
  ✓ Webhook     listening on :8080

Sessions:
  Active: 12    Idle: 3    Total today: 47

Agents:
  default (primary): running
  coder (sub): idle
  researcher (sub): idle
  reviewer (sub): idle

Cron Jobs:
  morning-briefing     ✅ success (last: today 08:00 UTC)
  service-health-check ⚠️ 3 consecutive failures

Provider Health:
  Anthropic  ✓ (last check: 2m ago)
  OpenRouter ✓ (last check: 2m ago)
```

```bash
# Service-level status (PID, memory, resources)
$ opennavi status

Service: opennavi-gateway@work
PID: 12345
Uptime: 3d 14h 22m
Memory: 128MB / 512MB limit
CPU: 12% / 100% limit

Version: opennavi v1.2.0 (commit abc1234, built 2026-07-29)
Update available: v1.3.0

Adapters:
  Telegram: connected    Discord: connected    Webhook: :8080
```

### 8.2 HTTP Health Endpoint

For Docker/systemd health checks and external monitoring:

```bash
$ curl http://localhost:8080/health

{
    "healthy": true,
    "version": "opennavi v1.2.0",
    "uptime": "3d 14h 22m",
    "memory_usage_bytes": 134217728,
    "session_count": 15,
    "adapters": {
        "telegram": "connected",
        "discord": "connected",
        "slack": "disconnected"
    },
    "providers": {
        "anthropic": "healthy",
        "openrouter": "healthy"
    }
}
```

### 8.3 Health Check Configuration

```yaml
# ~/.opennavi/profiles/<name>/gateway.yaml
health:
  endpoint: "/health"          # HTTP path for health checks
  providers_interval: "60s"    # how often to check LLM provider health
  adapters_interval: "30s"     # how often to verify adapter connectivity
  failover_threshold: 5        # consecutive failures before marking unhealthy
```

### 8.4 Provider Failover in Autonomous Mode

When a primary LLM provider fails during autonomous processing (e.g., cron job running), the gateway automatically falls back:

```go
func (m *ProviderManager) Stream(ctx context.Context, messages []llm.Message, tools []llm.Tool) (<-chan llm.StreamEvent, error) {
    // Try primary provider first
    events, err := m.primary.Stream(ctx, messages, tools)
    if err == nil {
        return events, nil
    }

    // Check if retryable (rate limit, transient error)
    if isRetryable(err) && m.primary.Retries < 3 {
        time.Sleep(backoff(m.primary.Retries))
        events, err = m.primary.Stream(ctx, messages, tools)
        if err == nil {
            return events, nil
        }
    }

    // Fall back to secondary provider if configured
    if m.fallback != nil && !m.fallback.IsUnhealthy() {
        log.Printf("primary %s failed (%v), falling back to %s",
            m.primary.Name(), err, m.fallback.Name())
        return m.fallback.Stream(ctx, messages, tools)
    }

    // No fallback available — error propagates up
    // (cron job will be marked as failed, alert sent to user if configured)
    return nil, fmt.Errorf("all providers unavailable: %w", err)
}
```

### Requirement: CLI Status Commands
The system SHALL provide `opennavi health` and `opennavi status` CLI commands reporting profile, status, uptime, adapters, sessions, agents, cron jobs, provider health, PID, memory, CPU, and version information.

#### Scenario: Health Report
- GIVEN an operator running `opennavi health`
- WHEN the command executes
- THEN the system SHALL report profile, status, uptime, and per-adapter connection state (connected/disconnected with reason)
- AND it SHALL report active/idle session counts and per-agent status
- AND it SHALL report cron job outcomes including consecutive failures and provider health with last check time
- AND `opennavi status` SHALL additionally report service PID, memory/CPU usage versus limits, version, and available updates

### Requirement: HTTP Health Endpoint
The system SHALL expose an HTTP health endpoint (default `/health`) for Docker/systemd health checks returning JSON with healthy flag, version, uptime, memory usage, session count, and per-adapter and per-provider status.

#### Scenario: Health Check Request
- GIVEN an external monitoring system
- WHEN it requests the health endpoint
- THEN the system SHALL return JSON including `healthy`, `version`, `uptime`, `memory_usage_bytes`, `session_count`, `adapters`, and `providers` fields

### Requirement: Health Check Configuration
The system SHALL support configurable health checking: endpoint path, provider check interval (default 60s), adapter connectivity interval (default 30s), and a failover threshold (default 5 consecutive failures) before marking unhealthy.

#### Scenario: Health Check Configuration
- GIVEN a health configuration in gateway.yaml
- WHEN the health checker runs
- THEN it SHALL check LLM provider health at `providers_interval`
- AND it SHALL verify adapter connectivity at `adapters_interval`
- AND it SHALL mark a provider unhealthy after `failover_threshold` consecutive failures

### Requirement: Provider Failover in Autonomous Mode
The system SHALL automatically fall back to a secondary LLM provider during autonomous processing (e.g., cron jobs) when the primary fails, retrying retryable errors with backoff first.

#### Scenario: Autonomous Provider Failover
- GIVEN a cron job or webhook triggering autonomous processing
- WHEN the primary provider fails
- THEN the system SHALL retry the primary provider with backoff for retryable errors (up to 3 retries)
- AND it SHALL fall back to the secondary provider if configured and not unhealthy
- AND when all providers are unavailable, the error SHALL propagate so the cron job is marked failed and an alert is sent if configured

---


## 02 Auth & Access Control in Autonomous Mode

### 9.1 Platform-Level Authentication

Users must be authenticated before they can interact with the autonomous navi:

```yaml
# ~/.opennavi/profiles/<name>/gateway.yaml
auth:
  dm_only: false                     # allow group/channel chat (default)
  
  platforms:
    telegram:
      allowed_users:                 # Telegram user IDs who can use the bot
        - "123456789"                # alice
        - "987654321"                # bob
      allowed_groups:                # group/channel IDs with access
        - "-1001234567890"           # #work channel
    discord:
      allowed_users: ["discord-user-id"]
      allowed_groups:                # server IDs or channel IDs
        - "server-id-123"
    slack:
      signing_secret: "${SLACK_SIGNING_SECRET}"  # HMAC verify Slack events
```

### 9.2 Webhook Authentication

External systems sending webhooks must be authenticated separately from user auth:

```yaml
gateway:
  webhook:
    hmac_secret: "${WEBHOOK_SECRET}"     # global HMAC for all webhooks
    sources:
      github:
        hmac_secret: "${GITHUB_WEBHOOK_SECRET}"   # per-source override
      grafana:
        hmac_secret: "${GRAFANA_ALERT_SECRET}"
```

### 9.3 Rate Limiting

Each user (regardless of platform) is rate-limited to prevent abuse:

```yaml
auth:
  rate_limit:
    messages_per_minute: 30              # per-user limit across all platforms
    messages_per_hour: 500               # hourly rolling window
    burst_size: 10                       # allow short bursts above per-minute limit
```

### Requirement: Platform-Level Authentication
The system SHALL authenticate users before they interact with the autonomous navi, supporting per-platform allowlists for users and groups/channels, plus platform signing secrets.

#### Scenario: Platform Authentication
- GIVEN a message from a user on a platform
- WHEN it is processed
- THEN the system SHALL allow the message only if the user is in the platform's `allowed_users` list or the group/channel is in `allowed_groups`
- AND it SHALL honor the `dm_only` setting (default false, allowing group/channel chat)
- AND for Slack it SHALL verify events using the `signing_secret` via HMAC

### Requirement: Webhook Authentication
The system SHALL authenticate external webhook systems separately from user auth, using a global HMAC secret with optional per-source overrides.

#### Scenario: Webhook Authentication
- GIVEN a webhook request from an external system
- WHEN it is processed
- THEN the system SHALL verify it against the global `hmac_secret`
- AND it SHALL apply the per-source override secret when the source is configured (e.g., github, grafana)

### Requirement: Rate Limiting
The system SHALL rate-limit each user across all platforms: 30 messages per minute, 500 messages per hour in a rolling window, with a burst allowance of 10 above the per-minute limit.

#### Scenario: User Rate Limiting
- GIVEN a user sending messages from any platform
- WHEN they exceed the configured rate limit
- THEN the system SHALL enforce `messages_per_minute` (default 30) and `messages_per_hour` (default 500, rolling window)
- AND it SHALL allow short bursts up to `burst_size` (default 10) above the per-minute limit

---


## 03 Multi-Profile Gateway

A single machine can run multiple gateway instances, each with its own profile:

```bash
# List all running gateways
$ opennavi gateway list

Profile    Status   PID     Uptime      Port     Adapters
work       running  12345   3d 14h      :8080    telegram, discord, webhook
personal   running  12400   2d 6h       :8081    telegram

# Each profile has its own:
# - Session database (SQLite)
# - Agent pool
# - Cron scheduler state
# - Config files
# - Log output
```

**Port conflict prevention:** When `http_port: 0` is set, the OS assigns a free port and writes it to a `.port` file:

```yaml
gateway:
  http_port: 0                    # let OS assign
  port_file: "~/.opennavi/profiles/work/gateway.port"
```

### Requirement: Multi-Profile Gateway Instances
The system SHALL support running multiple gateway instances on one machine, each with its own profile, session database, agent pool, cron scheduler state, config files, and log output, manageable via `opennavi gateway list`.

#### Scenario: Multiple Gateways
- GIVEN multiple profiles (e.g., work, personal)
- WHEN the operator starts gateways for them
- THEN the system SHALL run each as an independent instance with its own SQLite session database, agent pool, cron state, config, and logs
- AND `opennavi gateway list` SHALL display each profile with status, PID, uptime, port, and adapters

### Requirement: Port Conflict Prevention
The system SHALL let the OS assign a free port when `http_port: 0` is set and write the assigned port to the configured `.port` file.

#### Scenario: Dynamic Port Assignment
- GIVEN a profile with `http_port: 0` and a `port_file` configured
- WHEN the gateway starts
- THEN the system SHALL bind to an OS-assigned free port
- AND it SHALL write the assigned port to the configured port file

---


## 04 File Structure

```
internal/gateway/
├── runner.go          # GatewayRunner, Start/Stop lifecycle
├── config.go          # GatewayConfig loading and validation
├── router.go          # MessageRouter: parse → resolve → create Message
├── adapter.go         # Adapter interface (Start/Stop/Deliver)
├── telegram.go        # TelegramAdapter + TelegramDelivery
├── discord.go         # DiscordAdapter + DiscordDelivery
├── slack.go           # SlackAdapter + SlackDelivery
├── webui.go           # WebUI adapter (HTTP server + WebSocket hub)
├── webhook.go         # Webhook receiver handler
├── delivery.go        # DeliveryRegistry: platform → send message
├── agent_pool.go      # AgentPool: create, route, manage agents per session
├── cron.go            # CronScheduler: job types, execution, state persistence
├── auth.go            # AuthManager: allowlist, pairing, rate limiting
├── health.go          # HealthChecker: provider + adapter monitoring
├── mirror.go          # MirrorSystem: cross-platform message duplication
├── hooks.go           # HookRegistry: lifecycle event handlers
└── types.go           # Message, SessionKey, RawMessage, ParsedMessage

cmd/opennavi/
├── gateway.go         # `opennavi gateway start/stop/restart/status/list`
├── chat.go            # `opennavi chat` — attach to running daemon's TUI
└── health.go          # `opennavi health`, `opennavi status`
```

---


## 05 Implementation Order

### Phase 1: Core Gateway Daemon
1. **GatewayRunner** — Start/Stop lifecycle, adapter initialization
2. **MessageRouter.Parse** — Platform-specific raw message → unified Message conversion
3. **AuthManager** — Allowlist, rate limiting (basic)
4. **SessionManager.GetOrCreate** — Per-user-per-platform session resolution

### Phase 2: Primary Interaction Channels
5. **TelegramAdapter** + TelegramDelivery (polling mode first, then webhook)
6. **DiscordAdapter** + DiscordDelivery (WebSocket client, slash commands)
7. **WebhookHandler** — HTTP receiver with HMAC verification
8. **Streaming delivery** — Typewriter/edit patterns per platform

### Phase 3: Cron & Autonomous Processing
9. **CronScheduler** — Job types, cron expression parsing, state persistence
10. **AgentPool.RouteToAgent** — Run agent in goroutine, stream events to platform
11. **Webhook response patterns** — Sync (200), async (202), fire-and-forget (204)

### Phase 4: Advanced Features
12. **Council mode from chat platforms** — @mentions and slash commands trigger council
13. **Mirror system** — Cross-platform message duplication rules
14. **Hook system** — Auto-council, memory storage, error alerting hooks
15. **Provider failover** — Automatic fallback when primary LLM provider fails

### Phase 5: Operations & Hardening
16. **Health checker** — Provider/adapter monitoring with health endpoint
17. **Graceful shutdown** — Drain in-flight requests, flush sessions
18. **Multi-profile management** — `opennavi gateway list/start/stop` per profile
19. **Resource limits** — Memory/CPU quotas per profile
20. **Logging & rotation** — Structured logs with file rotation

---


## 06 Key Design Decisions

1. **Unified message flow** — Every interaction channel (Telegram, Discord, Slack, webhook, cron, CLI) funnels through the same `Message` type and `AgentPool.RouteToAgent`. No separate code paths for different origins.
2. **Concurrency model** — Daemon runs adapters + cron in parallel goroutines; each user message spawns its own agent loop goroutine within an AgentPool. Steering queue handles messages that arrive while an agent is busy.
3. **Streaming per platform constraints** — Each platform has different limits (Telegram 4096 chars, Discord 2000 chars/edit rate). Delivery adapters handle splitting and timing internally; the rest of the system just pushes events through.
4. **Cron jobs are first-class autonomous actors** — Not an afterthought. Cron jobs create their own sessions, run on schedule, timeout gracefully, persist state across restarts, and can escalate on repeated failures.
5. **Webhooks support sync and async patterns** — CI/CD callbacks get synchronous 200 responses; long-running analysis gets async 202 + platform delivery. Same handler, different path based on caller expectation.
6. **Council mode is triggered from chat platforms** — Users don't need a special CLI command to start multi-agent coordination. A @mention or slash command in their preferred chat app kicks off the council and delivers results back there.
7. **Auth at two layers** — User auth (who can talk to me) is separate from webhook auth (which external systems can call my endpoint). Both are configurable per-platform/per-source.
8. **Provider failover preserves autonomy** — When a cron job or webhook fires and the primary LLM provider is down, the system doesn't just error out — it transparently tries the fallback before reporting failure to the user.

---

## Related


### Other Parts

- [Daemon Lifecycle & Cron](./01-daemon-cron.md)
- [Webhooks & User Channels](./03-webhooks-user-channels.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080400 | 0.1.0 | Migrated from docs/spec/ to spec-tree |