---
status: draft
created: '19'
updated: '2026-08-11'
tags:
  - spec
  - operations
priority: medium
spec-refs: [080100, 080101, 080102]
---

# Plan: Autonomous Mode & User Interaction: Daemon Lifecycle & Cron

**Scope:** Daemon lifecycle, cron scheduling, webhook event processing, multi-platform user interaction channels (Telegram, Discord, Slack, Web UI, CLI), council mode from chat platforms, health monitoring, and the complete message flow. Covers both daemon navis (with thought queue) and one-off navis.

> **Part of:** [Plan: Autonomous Mode & User Interaction](./README.md) — split into 3 parts for readability.

## 00 Overview: What is Autonomous Mode?

An **autonomous navi** runs as a long-lived daemon process (via systemd, launchd, or Docker) with the **Gateway** interface enabled. It processes external events (webhooks, cron schedules) without any human present, while simultaneously accepting user interaction through whatever channels are compiled in.

The key distinction: autonomous mode is not *either* daemon *or* interactive — it's both concurrently. The navi monitors and acts on webhook/cron triggers automatically, and users can talk to it anytime through Telegram, Discord, Slack, Web UI, or CLI.

```
┌──────────────────────────────────────────────────────────────┐
│                     Autonomous Gateway                       │
│                                                              │
│  ┌─────────────┐    ┌─────────────┐    ┌──────────────┐     │
│  │ Webhook     │    │ Cron        │    │ Platform     │     │
│  │ Receiver    │    │ Scheduler   │    │ Adapters     │     │
│  │             │    │             │    │ (Telegram,     │     │
│  │ POST events │    │ Scheduled   │    │  Discord,      │     │
│  │ from CI/CD, │    │ prompts &   │    │  Slack)        │     │
│  │ monitoring, │    │ health      │    │              │     │
│  │ IoT         │    │ checks      │    │ Users talk     │     │
│  └──────┬──────┘    └──────┬──────┘    │ here.          │     │
│         │                  │            │                │     │
│         └──────────────────┼────────────┘                │     │
│                            ▼                             │     │
│  ┌─────────────────────────────────────────────────┐      │     │
│  │           MessageRouter + SessionManager          │      │     │
│  │     (unified queue: events, cron, user messages)  │      │     │
│  └──────────────────────┬──────────────────────────┘      │     │
│                         ▼                                  │     │
│  ┌─────────────────────────────────────────────────┐      │     │
│  │              Agent Pool (main + sub-agents)       │      │     │
│  │         Processes: events, prompts, user chat     │      │     │
│  └──────────────────────┬──────────────────────────┘      │     │
│                         ▼                                  │     │
│  ┌─────────────────────────────────────────────────┐      │     │
│  │           Delivery Registry                       │      │     │
│  │   Sends responses to: Telegram, Discord, Slack,    │      │     │
│  │   Web UI, CLI, or back via webhook                │      │     │
│  └─────────────────────────────────────────────────┘      │     │
└──────────────────────────────────────────────────────────────┘
```

### Requirement: Concurrent Autonomous and Interactive Operation
The system SHALL run the navi as a long-lived daemon process that processes external events (webhooks, cron schedules) without a human present while simultaneously accepting user interaction through compiled channels.

#### Scenario: Dual-Mode Operation
- GIVEN a navi started in autonomous mode
- WHEN the daemon is running
- THEN it SHALL monitor and act on webhook and cron triggers automatically
- AND users SHALL be able to interact with it concurrently via Telegram, Discord, Slack, Web UI, or CLI

### Requirement: Unified Event Routing
The system SHALL route all event sources (webhooks, cron schedules, user messages) through a unified MessageRouter and SessionManager pipeline into the agent pool.

#### Scenario: Event Routing
- GIVEN an external event (webhook, cron, or user message)
- WHEN the event enters the gateway
- THEN it SHALL be processed by the MessageRouter and SessionManager
- AND the agent pool SHALL process it and deliver responses through the Delivery Registry

---


## 01 Daemon Lifecycle

### 2.1 Entry Points

The Gateway daemon can be started three ways:

| Method | Command | Use Case |
|--------|---------|----------|
| **Direct binary** | `./oncall-bot --profile work` | Development, manual daemon control |
| **systemd service** | `systemctl start opennavi-gateway@work` | Production Linux (auto-restart, boot start) |
| **launchd** | `launchctl load ~/Library/LaunchAgents/com.opennavi.gateway.plist` | Production macOS (auto-restart, boot start) |
| **Docker** | `docker compose --profile default up -d` | Containerized production |

### 2.2 GatewayRunner Lifecycle

For **daemon navis**, the GatewayRunner manages both the thought queue and the gateway:

```go
type GatewayRunner struct {
    profile          string
    config           *GatewayConfig
    adapters         map[string]Adapter
    router           *MessageRouter
    sessionManager   *SessionManager
    thoughtQueue     *ThoughtQueue    // for daemon navis
    oneoffHandler    *OneoffHandler   // for one-off navis
    deliveryRegistry *DeliveryRegistry
    hookRegistry     *HookRegistry
    cronScheduler    *CronScheduler
    auth             *AuthManager
    healthChecker    *HealthChecker
    stopCh           chan struct{}
    started          bool
    mu               sync.RWMutex
}
```

**Key difference from one-off navis:** Daemon navis have a persistent thought queue that survives restarts. One-off navis process pending work and exit — no queue, no persistence.

func (g *GatewayRunner) Start(ctx context.Context) error {
    // 1. Load config (gateway.yaml, agents.yaml, cron.yaml, mirror.yaml)
    g.loadConfig()

    // 2. Initialize platform adapters (Telegram, Discord, Slack, WebUI, etc.)
    for name, adapterConfig := range g.config.Adapters {
        adapter, err := g.createAdapter(name, adapterConfig)
        if err != nil {
            return fmt.Errorf("adapter %s: %w", name, err)
        }
        g.adapters[name] = adapter
    }

    // 3. Start each adapter (each begins listening for events)
    for name, adapter := range g.adapters {
        if err := adapter.Start(ctx, g.handleIncoming); err != nil {
            return fmt.Errorf("adapter %s start: %w", name, err)
        }
    }

    // 4. Start cron scheduler (periodic autonomous tasks begin immediately)
    g.cronScheduler.Start(ctx)

    // 5. Load existing sessions from SQLite (sessions survive restarts)
    if err := g.sessionManager.LoadSessions(ctx); err != nil {
        return fmt.Errorf("load sessions: %w", err)
    }

    // 6. Start health checker (periodic provider + adapter monitoring)
    g.healthChecker.Start(ctx)

    // 7. Start hook system (lifecycle event handlers)
    g.hookRegistry.Start()

    // 8. Log startup summary
    g.logStartupSummary()

    g.started = true
    return nil
}

func (g *GatewayRunner) handleShutdown(sig os.Signal) {
    log.Printf("shutting down gateway for profile %s", g.profile)

    close(g.stopCh)

    // Stop adapters gracefully (drain in-flight requests)
    for name, adapter := range g.adapters {
        if err := adapter.Stop(); err != nil {
            log.Printf("adapter %s stop error: %v", name, err)
        }
    }

    // Stop cron scheduler
    g.cronScheduler.Stop()

    // Flush all sessions to SQLite (ensure no data loss)
    g.sessionManager.FlushAll()

    // Drain remaining messages from queue
    g.router.DrainQueue()

    log.Printf("gateway shutdown complete")
}
```

### 2.3 Graceful Shutdown Sequence

```
SIGTERM received
    ↓
1. Stop accepting new adapter connections (Telegram/Discord/Slack stop polling)
2. Drain in-flight webhook requests (return 503 to incoming webhooks)
3. Wait for running agent loops to finish (up to ShutdownTimeout, default: 30s)
4. Abort any still-running agents
5. Flush all sessions and memory to SQLite
6. Close database connection
7. Exit cleanly with code 0
```

### Requirement: Multiple Entry Point Support
The system SHALL support starting the Gateway daemon via direct binary, systemd service, launchd, or Docker, with auto-restart and boot-start behavior where applicable.

#### Scenario: Daemon Startup
- GIVEN an operator on a production Linux host
- WHEN they start the daemon via `systemctl start opennavi-gateway@work`
- THEN the system SHALL register the gateway as a systemd service with auto-restart and boot-start behavior
- AND equivalent support SHALL be provided via launchd on macOS and Docker on containerized hosts

### Requirement: GatewayRunner Lifecycle
The system SHALL manage the gateway lifecycle through a GatewayRunner that loads config, initializes and starts platform adapters, starts the cron scheduler, loads sessions, starts the health checker, and starts the hook system in order.

#### Scenario: Gateway Start Sequence
- GIVEN a gateway start request
- WHEN `GatewayRunner.Start(ctx)` is invoked
- THEN the system SHALL load configuration files (gateway.yaml, agents.yaml, cron.yaml, mirror.yaml)
- AND it SHALL initialize and start each platform adapter, returning an error if any adapter fails
- AND it SHALL start the cron scheduler and health checker
- AND it SHALL load existing sessions from SQLite so sessions survive restarts
- AND daemon navis SHALL maintain a persistent thought queue that survives restarts, while one-off navis SHALL process pending work and exit without queue or persistence

### Requirement: Graceful Shutdown Sequence
The system SHALL perform a graceful shutdown on SIGTERM: stop accepting new connections, drain in-flight requests, wait for running agent loops, flush sessions, close the database, and exit with code 0.

#### Scenario: Graceful Shutdown
- GIVEN a running gateway receiving SIGTERM
- WHEN the shutdown signal is received
- THEN the system SHALL stop accepting new adapter connections and return 503 to incoming webhooks
- AND it SHALL wait up to ShutdownTimeout (default 30s) for running agent loops to finish, aborting any still-running agents
- AND it SHALL flush all sessions and memory to SQLite and close the database connection
- AND it SHALL exit cleanly with code 0

---


## 02 Cron Scheduler — Scheduled Autonomous Tasks

Cron jobs are the primary mechanism for **fully autonomous** agent behavior — no user involvement needed. The navi wakes up on schedule, runs a prompt or check, and delivers results to configured platforms.

### 3.1 Job Types

| Type | Description | Example Use Case |
|------|-------------|-----------------|
| `prompt` | Sends a prompt to an agent with delivery to specified platform(s) | Morning briefing, status reports |
| `health` | Runs health checks on external services and alerts if failures detected | Service monitoring |
| `council` | Triggers a multi-agent council discussion on schedule | Weekly planning meetings |
| `webhook` | Fires outbound webhook calls (e.g., to notify CI/CD of status) | Status reporting to Slack/Teams |
| `memory` | Performs memory maintenance tasks (pruning, compaction) | Overnight cleanup |

### 3.2 Cron Configuration

```yaml
# ~/.opennavi/profiles/<name>/cron.yaml
cron:
  jobs:
    - name: "morning-briefing"
      type: "prompt"
      schedule: "0 8 * * 1-5"          # weekdays at 8am UTC
      prompt: |
        Give me a briefing: overnight alerts, PRs awaiting review,
        and any issues that need attention. Check project board status.
      target:
        platform: "telegram"             # deliver to Telegram DM
        user_id: "${TELEGRAM_USER_ID}"   # resolved from env var
      agent: "default"
      timeout: "5m"                      # abort if still running after 5 min

    - name: "service-health-check"
      type: "health"
      schedule: "*/5 * * * *"            # every 5 minutes
      target:
        platform: "telegram"             # alert only — no normal delivery
        user_id: "${TELEGRAM_USER_ID}"
      state:
        services:
          - url: "https://api.example.com/health"
            expected_status: 200
          - url: "https://db.example.com/ping"
            expected_status: 200
            timeout: "10s"

    - name: "weekly-council"
      type: "council"
      schedule: "0 10 * * 1"             # Mondays at 10am UTC
      prompt: "Review the week's progress and suggest priorities for next week."
      target:
        platform: "discord"              # deliver council to Discord channel
        channel_id: "${DISCORD_CHANNEL_ID}"
      agent: "default"                   # main agent runs this council
      council_members: ["coder", "researcher", "reviewer"]

    - name: "nightly-memory-cleanup"
      type: "memory"
      schedule: "0 3 * * *"              # daily at 3am UTC
      state:
        max_memory_entries: 10000
        prune_low_score: true
        low_score_threshold: 0.1
```

### 3.3 Cron Job Execution Flow

For **daemon navis**, cron jobs create thoughts on the thought queue:

```go
type CronScheduler struct {
    jobs     map[string]*CronJob
    mu       sync.RWMutex
    stopCh   chan struct{}
}

func (s *CronScheduler) Execute(ctx context.Context, job *CronJob) error {
    // 1. Create a thought on the daemon navi's queue
    thought := &Thought{
        ID:       uuid.New().String(),
        Prompt:   job.Prompt,
        Priority: job.Priority,  // cron jobs default to priority 3
        Status:   ThoughtPending,
        CreatedAt: time.Now(),
        LogPath:  fmt.Sprintf("~/.opennavi/profiles/%s/thoughts/%s", job.Profile, uuid.New().String()),
    }

    // Enqueue on the daemon navi's thought queue
    if err := s.thoughtQueue.Enqueue(thought); err != nil {
        log.Printf("cron %s failed to enqueue: %v", job.Name, err)
        s.deliverError(job.Target.Platform, job.Target.UserID, err.Error())
        return err
    }

    // For one-off navis, trigger directly:
    // s.oneoffHandler.Trigger(job.Target.NaviName, thought.ID)

    return nil
}
```

**For one-off navis**, cron jobs trigger the navi directly — no queue, no persistence:**

```go
func (s *CronScheduler) executeOneOff(job *CronJob) error {
    // Trigger the one-off navi to process pending work
    cmd := exec.Command("opennavi", "run", job.Target.NaviName, "--thought", job.Prompt)
    return cmd.Run()
}
```

### 3.4 Cron Job State Persistence

Cron jobs persist their state across restarts:

```go
type CronJobState struct {
    ID         string     `json:"id"`
    Name       string     `json:"name"`
    Type       string     `json:"type"`
    Schedule   string     `json:"schedule"`
    LastRun    time.Time  `json:"last_run"`
    LastSuccess bool      `json:"last_success"`
    SuccessCount int      `json:"success_count"`
    FailCount    int      `json:"fail_count"`
    ConsecutiveFails int   `json:"consecutive_fails"` // for alert escalation
}

// Persisted to ~/.opennavi/profiles/<name>/cron_state.json
```

**Failure escalation:** After 3 consecutive failures on a health-check job, the navi can escalate by:
- Increasing delivery frequency (from every 5 min → every 1 min)
- Adding additional notification platforms (webhook → Telegram + Discord)
- Triggering an auto-council to investigate

### 3.5 Cron Job Management Commands

```bash
# List all cron jobs and their status
$ opennavi cron list

Profile: work
Jobs:
  morning-briefing     prompt       */5 * * * *    Next: 2026-07-30 08:00 UTC   ✅ Last success: today
  service-health-check health     */5 * * * *    Next: 2026-07-30 12:45 UTC   ⚠️ Last fail: 2h ago (3 consecutive)
  weekly-council       council    0 10 * * 1      Next: 2026-08-03 10:00 UTC   ✅ Last success: last Monday

# Manually trigger a job
$ opennavi cron run morning-briefing

# Pause/resume a job (without deleting)
$ opennavi cron pause service-health-check
$ opennavi cron resume service-health-check

# View job history
$ opennavi cron history service-health-check --limit 10

# Edit job config (opens in $EDITOR)
$ opennavi cron edit morning-briefing
```

### Requirement: Cron Job Types
The system SHALL support five cron job types: `prompt`, `health`, `council`, `webhook`, and `memory`, each with its defined behavior.

#### Scenario: Job Type Dispatch
- GIVEN a scheduled cron job of type `prompt`
- WHEN the job fires
- THEN the system SHALL send a prompt to the configured agent with delivery to the specified platform(s)
- AND `health` jobs SHALL run health checks on external services and alert on failure
- AND `council` jobs SHALL trigger a multi-agent council discussion
- AND `webhook` jobs SHALL fire outbound webhook calls
- AND `memory` jobs SHALL perform memory maintenance tasks (pruning, compaction)

### Requirement: Cron Job Execution
The system SHALL execute cron jobs by creating thoughts on the daemon navi's thought queue, or by triggering one-off navis directly when no queue exists.

#### Scenario: Daemon Navi Cron Execution
- GIVEN a daemon navi and a scheduled cron job
- WHEN the job fires
- THEN the system SHALL create a thought with a unique ID, the job's prompt, priority (default 3), and a log path
- AND it SHALL enqueue the thought on the daemon navi's thought queue
- AND it SHALL deliver an error to the configured target if enqueueing fails
- AND for one-off navis, it SHALL trigger the navi directly without queue or persistence

### Requirement: Cron Job State Persistence
The system SHALL persist cron job state (last run, success/failure counts, consecutive failures) to `cron_state.json` across restarts.

#### Scenario: State Persistence and Failure Escalation
- GIVEN a cron job that has run
- WHEN its state is recorded
- THEN the system SHALL persist last run time, last success, success count, fail count, and consecutive failure count to `~/.opennavi/profiles/<name>/cron_state.json`
- AND after 3 consecutive failures on a health-check job, the system SHALL escalate by increasing delivery frequency, adding additional notification platforms, or triggering an auto-council to investigate

### Requirement: Cron Job Management Commands
The system SHALL provide CLI commands to list, manually trigger, pause, resume, view history, and edit cron jobs.

#### Scenario: Cron Job Management
- GIVEN an operator using the CLI
- WHEN they run `opennavi cron list`
- THEN the system SHALL display each job with its profile, type, schedule, next run time, and last success/failure status
- AND the system SHALL support `cron run`, `cron pause`, `cron resume`, `cron history`, and `cron edit` commands
- AND pause/resume SHALL toggle a job without deleting it

---


---

## Related


### Other Parts

- [Webhooks & User Channels](./03-webhooks-user-channels.md)
- [Council, Health & Implementation](./04-council-health-impl.md)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |