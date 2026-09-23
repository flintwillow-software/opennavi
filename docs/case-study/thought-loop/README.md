---
status: draft
created: '2026-09-23'
updated: '2026-09-23'
tags:
  - case-study
  - agent-runtime
  - thought-queue
  - interfaces
priority: high
---

# Thought Loop — One Queue, Two Front Doors

> Make the thought queue the universal main loop for every navi execution mode: interactive (TUI) sessions run through the queue exactly like daemon work, and the TUI is a portal into the running thought process — view the queue, monitor the in-flight thought, and inject messages into any thought — locally or connected to a remote daemon.

## Summary

The original design intent: **when a navi runs — manually or as a daemon — it is running through the thought queue.** The thought queue is not a daemon-only scheduling device; it is the navi's main loop. The TUI is not a REPL that happens to drive an agent; it is a **portal** over the thought system:

- It presents the thought queue (statuses, priorities, progress).
- The user can **attach a message to any thought** — queued or running.
- The user can **hop into the current (running) thought** and monitor it live — streaming content, tool calls, lifecycle transitions — while steering it with injected messages.
- In daemon mode, the same TUI **connects to the running daemon remotely** (through the gateway) and manages its queue: view, steer, pause/resume/cancel, enqueue.

This unifies the currently split execution models (interactive `Prompt()` path vs. daemon queue path) into one: one queue, two front doors (local TUI session and remote daemon attachment).

## Quick Reference

- **Case study folder**: `docs/case-study/thought-loop/`
- **Status**: Draft
- **Target spec components**: `01-agent-runtime` (thought queue §00 — execution-model statement), `00-navi-core` / interfaces (TUI + remote attachment), touches `03-session-context` (steering/observation mapping)
- **Downstream**: d3-specs → d3-changes → d3-issues

## Ownership Map

| Module | Role | Related case study files |
|--------|------|--------------------------|
| `internal/thought` (existing) | queue stays the single scheduler; gains observation/steering affordances | SOLUTION.md §queue |
| `internal/daemon` (existing) | runtime attachment surface (remote TUI via gateway) | SOLUTION.md §remote |
| `internal/interfaces` (existing) | CLI/TUI/Gateway registry — TUI becomes a first-class interface | SOLUTION.md §tui |
| `agent/` (existing) | steering queue, event bus — per-thought message injection path | SOLUTION.md §steering |
| `tools/*` (existing) | unchanged — thought runs already use the governed registry surface | — |
| `store/sqlite` (existing) | queue state the TUI renders; no schema change expected | — |

## Status History

| Date | Change |
|------|--------|
| 2026-09-23 | Drafted from operator design discussion (thought queue as main loop; TUI as portal; remote daemon attachment) |
| 2026-09-23 | Message-granularity decision recorded: connector messages are thought-per-message; TUI interaction attaches/steers existing thoughts or schedules new ones |

## Related

- `docs/specs/01-agent-runtime/00-thought-queue.md` — current "daemon-only" execution-model statement this case study revises
- `docs/specs/01-agent-runtime/01-workers-navi-comm.md` — interface→execution-model mapping table
- `docs/case-study/agent-utility-tools/` — the tool surface thought runs execute with