---
status: draft
created: '2026-09-24'
updated: '2026-09-24'
tags:
  - architecture
  - runtime
  - thought
---

# Runtime

> The navi object, its collections, the thought system, and the run lifecycle.
> Decisions behind this shape: ADR [0002](../adr/0002-navi-is-a-composition-root.md),
> [0003](../adr/0003-thought-system-first-class.md),
> [0007](../adr/0007-thought-modes.md),
> [0009](../adr/0009-providers-set-with-per-thought-selection.md).

## Lifecycle: declare, freeze, run

```go
n := navi.New("name", navi.WithVersion(...), navi.WithPersonality(...))

// declaration phase — collections are mutable
n.Skills.Add(...)
n.Tools.Add(...)
n.Models.Add(...)
n.Connectors.Add(...)
n.Portals.Add(...)
n.Modes.Add(...)

// finalize: validate cross-cutting invariants, freeze, assemble, serve
if err := n.Run(ctx); err != nil { ... }
```

- `Add` does eager shape validation (non-empty metadata, well-formed schema,
  unique name within its collection).
- `Run` does cross-cutting validation: duplicate tool names across sources, port
  conflicts between portals/gateways, mode references to unknown tools,
  permission-ceiling consistency.
- After `Run` begins, collections are frozen. Nothing mutates while connectors
  and the worker pool are live.
- No `init()` registration and no global registry. Every component is an
  explicit value passed to a collection.

## The collections

| Collection | Core or added | Holds | Notes |
|---|---|---|---|
| `Thoughts` | core | the thought system | Always present; the runtime's main loop |
| `Skills` | added | compiled-in skill content | Baked into the system prompt at build time |
| `Tools` | added | agent-callable actions | Registered with full metadata; the active set is derived |
| `Models` | added | provider + model + version | One marked default; config may override the default |
| `Connectors` | added | inbound transport doors | Receive `Deps` at `Start` |
| `Portals` | added | human observe/control surfaces | Receive `Deps` at `Start`; list/schedule/steer thoughts |
| `Modes` | added | named tool/permission/model sets | Selected per thought |
| `Permissions` | core/added | compiled policy ceiling | Runtime may only tighten |
| `Extensions` | added | runtime plugin allow-list | Deferred (ADR 0005) |

## Thought system

The thought system is the navi's main loop — the queue of pending work. It is
public (`thought/`) and owned by the navi. Knowledge a navi carries across
thoughts is a separate concern; see [memory.md](./memory.md).

```go
type Thought struct {
    ID, Prompt, Context string
    Metadata     map[string]string
    Priority     int
    Status       string      // pending | running | paused | complete | failed | cancelled
    Provider, Model string   // per-thought overrides (ADR 0009)
    Mode         string      // capability profile (ADR 0007)
    DeferUntil   time.Time   // durable not-before schedule
    SessionID    string      // persisted transcript, when one exists
    Foreground   bool
    CreatedAt, UpdatedAt time.Time
    // ...
}
```

The public value carries what a *component* acts on. Scheduler
bookkeeping — slot and worker assignment, preemption requests, the park
checkpoint, log paths — stays inside the runtime and is not part of this
contract. `Mode` is persisted with the thought (the durable thought
store carries a `mode` column) and consumed by the runner, which resolves
the tool surface, permission ceiling, and optional model from it; an
empty mode selects the navi's default surface.

Handle API exposed as `n.Thoughts`:

| Operation | Purpose |
|---|---|
| `Add(prompt, opts…)` | Enqueue a thought (durable) |
| `List()` / `Get(id)` | Inspect the queue |
| `Route(id, msg)` | Lifecycle-aware delivery: queued → append, running → steer, paused → resume, terminal → reject |
| `Subscribe(fn)` | Observe queue events (portal/connector fanout) |
| `Pause` / `Resume` / `Cancel` / `Retry` / `BumpPriority` | Control |

Semantics preserved from today's queue: priority scheduling, slot management,
preemption, foreground reservation, crash recovery, park-with-checkpoint, and
memory-first persistence with a dirty-set flusher.

**Doors and granularity.** Connector messages are thought-per-message (discrete,
schedulable work). Portals attach/steer existing thoughts or schedule new ones.
No path drives the agent around the queue.

**Schedules** are a thought-system primitive, not generic config: a recurring
routine is a thought factory. Default routines may be declared by the navi
author; operators may override schedules at runtime.

## Modes

A mode is a named capability profile selected per thought:

```go
type Mode struct {
    Name        string
    Tools       []string          // active subset; empty = all
    Permissions PermissionPolicy
    Model       string            // optional
    Prompt      string            // optional fragment
}
```

The runner resolves the tool surface, permission ceiling, and model from the
thought's mode. Modes compose with per-thought provider/model overrides. A mode
can only restrict relative to the compiled policy — never escalate (ADR 0006).

## Models and providers

- The navi declares a set of models at build time; one default (ADR 0009).
- Runtime config sets the active default, endpoints, and credential references.
- A built-in `model_list` tool exposes the available set to the agent.
- A thought selects provider/model per run; unknown names fail the thought with
  a clear error (no silent fallback).
- Fallback chains and retry remain at the provider-config layer.

## Connectors and portals

Both are lifecycle components attached to a navi with a dependency contract:

```go
type Connector interface {
    Start(ctx context.Context, deps Deps) error
    Stop() error
}

type Portal interface {
    Start(ctx context.Context, deps Deps) error
    Stop() error
    Attach(identity string) error
    Steer(identity, thoughtID, message string) error
    // ... Pause / Resume / Cancel / Enqueue
}

type Deps struct {
    ThoughtSystem thought.ThoughtSystem
    Model         model.Registry
    // Observation is the bounded, cursor-ed view of queue lifecycle and
    // running-agent events (NAVI-352). portal.Deps carries it; a portal
    // renders a running thought's output from this alone.
    Observation   portal.Observer
    Log           *slog.Logger
}
```

`Deps` is a **lifecycle argument, not a constructor input**. The composition
root cannot supply it any earlier: a navi declares its collections first and
only assembles them in `Run`, so a component constructed before `Run` has
nothing to receive. Threading it through `Start` keeps constructors config-only
(`connector.Discord(connector.Config{...})`) and keeps these packages free of
any `navi` import (ADR 0004 rule 1). A portal needs the handles for the same
reason a connector does — observation arrives through `Deps.Observation`, a
bounded subscription a slow surface drains (and may fall behind) without ever
slowing the queue.

- **Connectors** bring work in and manage thought lifecycles (spawn/attach/
  route). Egress tools (`discord_send`, `websocket_send`) close over the
  connector's live client and are added **explicitly** by the author via
  `connector.SendTool()` — never auto-registered, so the capability surface
  stays visible in `main.go`.
- **Portals** are human-facing; they list, schedule, and steer, and stream the
  same queue and running-agent events a connector can observe — through
  `Deps.Observation`, whose bounded mailboxes keep a slow front-end from
  stalling the queue (NAVI-352).

The composition root package is named `navi`; connectors and portals never
import it — they depend only on the contract packages.

## Agent and tools

Each thought run resolves an agent run: mode-selected tool surface → registry
active set → bridge into the executor. The registry owns metadata, validation,
active-set membership, and dispatch; the executor owns concurrency and timeout
envelopes. Session state (messages, checkpoints) persists through the store.

## Related

- [overview.md](./overview.md) — package map and boundaries
- [security.md](./security.md) — permission ceiling and omission
- [extension-model.md](./extension-model.md) — third-party components
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones