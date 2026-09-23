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

# Solution

> Design sketch — the shapes and seams, not yet a spec. d3-specs (Step 2) turns these into spec-tree updates.

## 1. One queue, two front doors (execution model)

The central revision: **the thought queue is the navi's main loop in every mode.** The daemon/one-off split in `00-thought-queue.md` becomes a *front-door* distinction, not an execution-model split:

- **Daemon front door** (existing): `navi daemon` — always-on process; thoughts arrive from cron, gateway, enqueue; the queue schedules them against bounded slots.
- **Interactive front door** (new): the TUI session runs the same queue *in-process* with a foreground slot. A user message enqueues a thought (or continues a conversation-thought); the TUI subscribes to its events instead of calling `agent.Prompt()` directly. When the queue empties and the user quits, the process exits — same durability semantics (park-with-checkpoint on interrupt), no daemon required.

Concretely: the existing "one-off" path collapses into the queue path with a `foreground` slot policy; the REPL-style direct `Prompt()` drive becomes an implementation detail behind the interface layer rather than an execution model.

## 2. The TUI as portal

The TUI is a view over the queue, not a chat transcript with a scrollbar:

- **Queue view (home)**: the navi's thoughts — id, status (pending/running/paused/…), priority, age, checkpoint presence, session-log pointer. Backed by the same SQLite state the daemon serves and the `QueueEvent` fanout for live updates.
- **Per-thought actions**: attach a message to a queued thought (it lands in the thought's context when it runs), bump priority, pause/resume/cancel, retry a failed thought.
- **Live view (hop into the running thought)**: subscribe to the running thought's agent events — content deltas, tool calls and results, lifecycle transitions (all already emitted on the agent event bus and the queue fanout). The monitor is read-only observation; steering is the write path.
- **Steering**: "add a message" on a running thought routes into the agent's existing steering queue (turn-boundary injection, the mechanism `runLoop` already honors), acknowledged as a queue-level event so the TUI can show "delivered to thought X". Steering a paused thought resumes it.

### Message granularity (operator decision, 2026-09-23)

**Thought-per-message for connector-sourced messages; attach/steer for TUI interaction.** Concretely:

- **Connectors** (gateway adapters, cron firings, CLI `navi enqueue`) → **one thought per message** — consistent with existing cron enqueue/coalescing behavior; each inbound request is discrete, schedulable work.
- **TUI** → the user either (a) creates/schedules a new thought (foreground or queued) or (b) attaches to an existing one: a message on a **queued** thought appends to its `Context` for when it runs; a message on a **running** thought injects via steering; a message on a **paused** thought resumes it with the message attached.

This keeps discrete request semantics for machines, and conversational continuity for the human who is watching.

## 3. Remote attachment (daemon mode)

In daemon mode the same TUI connects to the running daemon and manages it remotely — the mental model is identical, only the transport changes:

- The daemon exposes an **attachment surface** on its gateway interface (S000400): a stream of queue + thought events (the same events the local TUI consumes) and a control channel (attach message, steer, pause/resume/cancel, enqueue).
- The TUI client is transport-agnostic: in-process subscription locally, gateway connection remotely. One view, one code path.
- Remote attachment is the first consumer of the gateway's event-stream surface — the wire protocol details belong to 05-messaging; trust/identity belongs to 04-multi-agent (S040000-family).

## 4. Interface composition (S000400 alignment)

The interface registry's CLI/TUI/Gateway set stops forking the runtime:

| Interface | Role |
|-----------|------|
| TUI | portal: queue view, live thought view, steering (local in-process or remote via gateway) |
| Gateway | daemon's event/control transport for remote TUIs (and later, other message sources) |
| CLI | one-shot commands (enqueue, health) — thin, queue-first |

Interactive TUI sessions and daemon runs converge on: **queue → pool → AgentRunner → governed tools**, with the TUI attached as observer/controller.

## 5. What stays as-is

- The thought lifecycle, slots, preemption, park-with-checkpoint, crash recovery — all already built and unchanged.
- The per-thought tool surface (registry active set via `tools/bridge`) — unchanged.
- Metrics (`/metrics`) and logging seams — the TUI is another event consumer, not a new instrumentation path.

## Open Design Points (for d3-specs)

1. ~~Foreground thought semantics~~ — **decided (2026-09-23)**: connector messages are thought-per-message; TUI interaction attaches/steers existing thoughts or schedules new ones (see §2).
2. **Steering delivery guarantees**: at-turn-boundary injection confirmed for running thoughts; open question is whether a queued-thought message lands verbatim in `Context` or as a structured annotation, and the event-ack shape for "delivered".
3. **TUI event protocol**: reuse `QueueEvent` + agent events as-is over the gateway, or define a portal-specific envelope (05-messaging decision)?
4. **TUI implementation dependency** (e.g. Bubble Tea vs. hand-rolled) vs. the repo's no-new-deps convention for the core runtime.
5. **Authorization for remote control**: which queue actions require what trust level (S040200 alignment).