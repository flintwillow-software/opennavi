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

# Context

## Problem

The thought queue was designed as the navi's main loop: when a navi runs — manually or as a daemon — it should be running through the thought queue. The current implementation only half-delivers that intent:

1. **Two execution models instead of one.** Daemon navis run exclusively through the queue (everything becomes a durable, prioritized, preemptible thought). But interactive navis bypass it: the CLI/REPL drives `agent.Prompt()` directly, and the spec explicitly blesses this split ("the thought queue is for daemon navis… one-off navis use a simpler model"). The consequence is two code paths, two lifecycles, and two persistence stories for what is conceptually the same activity: the navi thinking about work.

2. **No portal.** There is no human-facing surface that shows what the navi is thinking. An interactive session is a conventional REPL transcript; a daemon is a background process you can only interrogate through its logs. The queue state — statuses, priorities, progress, checkpoints — is all in SQLite and event-emitting, but nothing renders it or routes input back into it.

3. **No way to talk to a thought.** Users cannot attach a message to queued work, watch the running thought stream (content deltas, tool calls), or steer it mid-flight. The agent layer has a steering queue and an event bus; the queue layer has none of that exposed.

4. **Remote management is manual.** A daemon navi can only be inspected via `navi health`, logs, and (now) `/metrics`. There is no way to attach to a running daemon from elsewhere and manage its queue — view, steer, pause/resume, enqueue — without shelling into the host.

## Stakeholders

- **The operator (primary human user)** — wants to *see* the navi thinking: the queue as the home screen, the running thought as the live view, and messages as the way to redirect it — whether the navi runs locally in a terminal or as a background daemon on another machine.
- **Navi authors** — need one execution model to reason about, not two; the interface set (CLI/TUI/Gateway) should compose rather than fork the runtime.
- **The navi itself** — benefits from uniform durability: interactive work gets the same park-with-checkpoint, crash recovery, priority, and session-log treatment as scheduled work.

## Constraints

- **No daemon-only privilege**: the queue must not require a daemon process — interactive mode runs the same queue in-process (a foreground slot), so a one-off navi still exits when done.
- **Existing control-plane pieces are the substrate**: the agent `Steering` queue, the agent event bus, `QueueEvent` fanout, and the gateway interface (planned, S000400) are the seams — not new parallel mechanisms.
- **CGO_ENABLED=0, no new dependencies** for the core runtime; the TUI is expected to follow the repo's dependency conventions (decision point for d3-specs).
- **Security**: remote attachment is a control channel into a running navi — it inherits the trust/identity model (S040000-family) and must not bypass IAM access levels on tools.

## Out of Scope

- Council / multi-agent delegation flows (S000400 council mode, ChatSession) — they may *run on* the queue but their orchestration design is separate (04-multi-agent).
- Grafana/Alloy dashboards and other ops-side observability (covered by NAVI-258 follow-ups).
- Gateway protocol details beyond what remote attachment requires (05-messaging owns the wire protocol).

## Success Looks Like

- A navi run in interactive mode behaves identically to a daemon run from the queue's perspective: durable thoughts, same lifecycle, same recovery.
- The TUI's home view *is* the queue; the running thought is one keypress away; messages attach to thoughts and visibly change their course.
- The same TUI attached locally or remotely shows the same mental model, so "managing my navi" does not depend on where it runs.