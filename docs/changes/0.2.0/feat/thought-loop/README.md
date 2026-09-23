---
status: draft
created: '2026-09-23'
updated: '2026-09-23'
tags:
  - change-log
  - agent-runtime
  - interfaces
spec-refs: [010003, 010104, 010312, 000406, 000407]
---

# 0.2.0 — feat: thought loop (house model, TUI portal, remote attachment)

> Derived from the spec diff of S010003, S010104, S000406, S000407, S010312 and informed by the codebase analysis (2026-09-23). Groups the code changes implied by the thought-loop spec update, grounded in actual project structure.

## Summary

The spec establishes the thought queue as the navi runtime's main loop in every run mode (house model) and specifies the TUI as a portal over the queue plus remote attachment via the gateway. In code terms: the interactive path stops driving `agent.Prompt()` directly and runs the same in-process queue with a foreground slot; the queue gains thought-routed messaging with delivery acknowledgments; a portal observation API streams queue/thought events to clients; and a TUI renders it all — locally or over the gateway.

## Changes

- **`internal/thought` (message routing, foreground slot)** — queue-level message routing by thought id with lifecycle-dependent delivery (queued → Context append, running → steering injection, paused → resume-with-message, terminal → rejection) and a new `QueueEventKind` for delivery acknowledgments; foreground-slot policy on the pool so a binary run's interactive thought occupies a slot under the same priority/preemption rules
  - `internal/thought/queue.go` — `RouteMessage(id, msg)` + ack event emission (never re-enters the queue while `q.mu` is held — delta/seed discipline per the metrics work)
  - `internal/thought/queue.go` — foreground slot reservation in `Config`/slot accounting
  - unit tests per S010312 delivery scenarios
- **`internal/daemon` (binary-run assembly)** — a binary-run mode that assembles the runtime in-process (queue, pool with foreground slot, registry, tools) without daemon lifecycle; shared `Assemble` gains params for foreground mode
  - `internal/daemon/daemon.go` — `RuntimeParams` foreground option; runner already binds the registry (NAVI-272 wiring) — reuse as-is
  - `internal/daemon/runner.go` — bridge routed messages into `agent.Steering` (the runner owns the per-thought agent)
- **`internal/interfaces` (portal observation API)** — an event-stream surface exposing the same `QueueEvent` + agent event stream a local TUI consumes, transport-agnostic so the gateway can serve it remotely
  - `internal/interfaces/interfaces.go` — portal observer contract (subscribe queue events + per-thought agent events; control channel: attach/steer/pause/resume/cancel/enqueue)
- **`internal/interfaces/tui` (new; the portal client)** — TUI as thought-queue portal: queue home view, per-thought actions, live view (hop into the running thought), steering input; binary-run in-process attach or remote attach via gateway
  - new package; TUI framework dependency decision required (Bubble Tea vs. hand-rolled — Open Design Point #4)
- **`internal/daemon` (gateway attachment surface)** — daemon serves the portal event stream + control channel on its gateway port, with IAM-aligned authorization stub (S040200 alignment)
  - `internal/daemon/daemon.go` / gateway interface wiring; graceful shutdown for the serve path (pattern: `--metrics-addr` serve path in #280)
- **`cmd/navi` (binary-run entry)** — interactive run mode wiring the TUI to the in-process queue (complement to `navi daemon`)
  - `cmd/navi/main.go` + new subcommand wiring

## Spec Refs

S010003, S010104, S000406, S000407, S010312

## Code Context

- **Project structure**: Go module `github.com/flintwillow-software/opennavi`; public SDK at top level (`agent/`, `tools/`, `store/`, `llm/`, `sdk/`), runtime wiring under `internal/` (`daemon/`, `thought/`, `interfaces/`, `session/`, `ops/`), CLI at `cmd/navi/`. CI gates: `just ci` (build, vet, gofmt, `-race`), CGO_ENABLED=0, changelog check on every PR.
- **Existing patterns**: the runtime is assembled in `internal/daemon.Assemble` (config → store → provider chain → queue → pool → AgentRunner); interfaces register through `internal/interfaces`; queue observability rides `OnEvent` fanout (WireLogging, WireCron, metrics collector); steering exists at the agent layer (`agent.Steering`, turn-boundary injection); the registry→executor bridge (`tools/bridge.Bind`) is how thought runs get their tool surface.
- **Dependencies**: none required for the runtime work (stdlib); the TUI framework is the one open dependency decision.

## Notes

- **Step 4 grouping**: one epic — NAVI-316 (parent NAVI-50, "Thought loop — one house, many doors") with sub-issues in dependency order — NAVI-317 queue message routing + foreground slot (blocks 2–4), NAVI-318 portal observation API + gateway attachment surface, NAVI-319 TUI portal client, NAVI-320 binary-run entry + steering bridge. S010312 delivery-ack events are part of NAVI-317.
- **Already partially built** (call out in issue bodies to avoid re-work): the agent runner binds the registry-governed tool set per thought run; park-with-checkpoint, crash recovery, and the event fanout all exist; `QueueEvent` kinds and the store-backed queue are ready for routing to build on.
- **Deferred by spec**: messaging wire protocol (05-messaging owns it); council/ChatSession untouched.

## Tracked

- Epic: Thought loop — one house, many doors — NAVI-316 [priority: high]
  - Issue: Queue message routing + foreground slot — NAVI-317 [priority: high, blocks: NAVI-318/319/320]
  - Issue: Portal observation API + gateway attachment surface — NAVI-318 [priority: medium, blocked-by: NAVI-317, blocks: NAVI-319]
  - Issue: TUI portal client — NAVI-319 [priority: medium, blocked-by: NAVI-317/318]
  - Issue: Binary-run entry + steering bridge — NAVI-320 [priority: medium, blocked-by: NAVI-317]