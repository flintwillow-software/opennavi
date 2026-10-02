---
status: draft
created: '2026-09-24'
updated: '2026-09-30'
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
| `Skills` | added | compiled-in skill content | Loaded **selectively** into a thought's context — author-declared auto-load, thought-declared request, or agent-chosen tool. Never baked wholesale ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)) |
| `Tools` | added | agent-callable actions | Registered with full metadata; the active set is derived |
| `Models` | added | provider + model + version | One marked default; config may override the default |
| `Connectors` | added | inbound transport doors | Receive `Deps` at `Start` |
| `Portals` | added | human observe/control surfaces | Receive `Deps` at `Start`; list/schedule/steer thoughts |
| `Modes` | added | named tool/permission/model sets | Selected per thought |
| `Datastores` | added | persistence backends (`fs`, `sqlite`, …) | Candidates declared at build time, chosen at runtime; every aggregate routes to the active backend by default. Backs thought state, transcripts, tool state, and memory. RAM is the source of truth, the backend is eventual ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)) |

`Permissions` is a field (the compiled policy ceiling; runtime config may only
tighten it), not a collection. Runtime plugins are deferred (ADR 0005) and have
no collection yet.

## Thought system

The thought system is the navi's main loop — the queue of pending work. It is
public (`thought/`) and owned by the navi. Knowledge a navi carries across
thoughts is a separate concern; see [memory.md](./memory.md).

```go
type Thought struct {
    ID, Prompt, Context string
    Result       string      // completion message (empty until complete)
    Metadata     map[string]string
    Priority     int
    Status       ThoughtStatus // pending | running | paused | complete | failed | cancelled
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
interface. `Mode` is persisted with the thought (the durable thought
store carries a `mode` column) and consumed by the runner, which resolves
the tool surface, permission ceiling, and optional model from it; an
empty mode selects the navi's default surface.

Handle API exposed as `n.Thoughts`:

| Operation | Purpose |
|---|---|
| `Enqueue(t)` | Schedule a thought (durable; deferral rides the thought) |
| `GetThought(id)` / `GetThoughts()` | Inspect the queue (copies) |
| `RouteMessage(id, msg)` | Lifecycle-aware delivery: queued → append, running → steer, paused → resume, terminal → reject |
| `Transcript(id, after, limit)` | Read a thought's conversation, live and historical, bounded by cursor + limit ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)) |
| `OnEvent(fn)` | Observe queue events (portal/connector fanout) |
| `Pause` / `Resume` / `Cancel` / `Retry` / `BumpPriority` | Control |
| `Complete(id, result)` / `Fail(id, detail)` | Report a thought's outcome |

Semantics preserved from today's queue: priority scheduling, slot management,
preemption, foreground reservation, crash recovery, park-with-checkpoint, and
memory-first persistence with a dirty-set flusher.

**Doors and granularity.** Connector messages are thought-per-message (discrete,
schedulable work). Portals attach/steer existing thoughts or schedule new ones.
No path drives the agent around the queue.

**Schedules** are a thought-system primitive, not generic config: a recurring
routine is a thought factory. Default routines may be declared by the navi
author; operators may override schedules at runtime. When the connector is
constructed with `cron.WithStateStore(db)`, tick state persists
(`last_fired`/`fire_count`) and a tick that came due while the navi was down is
coalesced into one thought at `Start`; without it the connector keeps state in
memory only and a missed tick is skipped (NAVI-358).

## Context assembly

The thought queue **is** the context mechanism ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)). Context pressure is resolved by bounding thoughts and deferring work into follow-up thoughts, not by compacting a long conversation: a thought that approaches its budget finishes a coherent unit, writes what the next one needs into short-term memory, and queues follow-ups that declare which skills to load. A task too large for one thought becomes a plan of thoughts.

- **Assembly happens at thought creation**, not per turn. A queued thought carries its assembled context and the runner consumes it; what was a rendering-time pipeline is a creation-time decision.
- **The assembled context is recorded with the thought** as its first transcript entry (index 0), so it is readable through the same contract as the rest of the transcript ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)) and inspectable by any portal ([ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md)).
- **Everything that shapes context is a build-time declaration** — sources, auto-loaded skills, component-derived guidance. Runtime config may only narrow it. The reason is the threat model, not tidiness: if a prompt can reach the assembler, the assembler is an injection surface (see [security.md](./security.md)).
- **Skills are a menu, not a fetch.** All three loading paths can only select from the compiled-in set, so the worst case of a manipulated request is loading a skill the author already shipped.
- **Core skills ship with the component types**: authored prose for semantics plus a mechanical inventory derived from component metadata, so the model is never unaware of a capability the binary has.
- **Compaction is not a mechanism.** It is being removed rather than kept as a fallback; deferral replaces it.

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

Both are lifecycle components attached to a navi with a Deps interface:

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
    // ThoughtSystem is the queue handle. It also carries each thought's
    // transcript, live and historical, so a portal reads a conversation
    // without a session handle and without knowing the backend (ADR 0011).
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

- **Connectors** are **bidirectional service integrations** ([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)). Inbound, they turn external stimuli into thoughts and manage their lifecycles (spawn/attach/route) — how a service gets the navi's attention. Outbound, they supply the tools that let the navi act on that service: egress tools (`github_issue_reply`, `discord_send`) close over the connector's live client and are added **explicitly** by the author via `connector.SendTool()` — never auto-registered, so the capability surface stays visible in `main.go`. Both halves share one connection, config, and credential. There is no generic egress payload: what is sent is service-specific. Role separation between navis is therefore **structural** — a function of which components and tools exist, not of what a prompt says.
- **Portals** are human-facing; they list, schedule, and steer, and stream the
  same queue and running-agent events a connector can observe — through
  `Deps.Observation`, whose bounded mailboxes keep a slow front-end from
  stalling the queue (NAVI-352). A portal also reads a thought's full
  conversation — live and historical — through `Deps.ThoughtSystem`, so attach
  and restart show what happened rather than only what happened next
  (ADR 0011). The intended end state is one bidirectional stream a portal
  translates for its transport, with access scoped by IAM rather than by
  separate read and write surfaces (ADR 0012, proposed).
- The SDK ships an HTTP **`portal/gateway`** component: a `portal.Portal`
  that frames the bounded observation stream and control channel over HTTP
  and delegates authorization to the control portal (the `Hub`), so there is
  one authorization path and a remote front-end attaches through the same
  interface as every other portal. The TUI is likewise a public
  `portal.Portal` component built from `Deps`, registered by the reference
  navi through `n.Portals`.
- **Control** is an interface capability too: `portal.Control` authorizes
  attach/control through a `portal.Authorizer` and applies the actions
  through the thought system it receives at `Start`. The gateway delegates
  to it, so a remote front-end can queue, steer, and manage thoughts in
  another process.

The composition root package is named `navi`; connectors and portals never
import it — they depend only on the component packages.

## Agent and tools

Each thought run resolves an agent run: mode-selected tool surface → registry
active set → bridge into the executor. The registry owns metadata, validation,
active-set membership, and dispatch; the executor owns concurrency and timeout
envelopes. Session state (messages, checkpoints), stateful tool state (todo,
note), and the memory tiers all persist through the selected `Datastores` backend,
which is not on the read path: the run reads and writes RAM, and the backend
catches up asynchronously through one ordered writer (ADR 0011). Memory
requires a configured store — there is no volatile-memory mode.

## Related

- [overview.md](./overview.md) — package map and boundaries
- [security.md](./security.md) — permission ceiling and omission
- [extension-model.md](./extension-model.md) — third-party components
- [ADR 0011](../adr/0011-persistence-is-a-datastore-component.md) — datastores, RAM-first persistence
- [ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md) — the portal stream and IAM scoping
- [ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md) — context assembly, skills, and why the queue is the context limit
- [ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md) — connectors and connector-owned egress
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones