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
| `Skills` | added | compiled-in skill content | Loaded **selectively** — generated core skills (one per declared component type, plus a component inventory) auto-load and are bounded by what the navi actually has; authored skills load by author declaration (`AutoLoad`), a thought's declared request, or the `skill_load` tool. Every path resolves against the compiled-in set only. Never baked wholesale ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)) |
| `Tools` | added | agent-callable actions | Registered with full metadata; the active set is derived |
| `Models` | added | provider + model + version | One marked default; config may override the default |
| `Connectors` | added | inbound transport doors | Receive `Deps` at `Start` |
| `Portals` | added | human observe/control surfaces | Receive `Deps` at `Start`; list/schedule/steer thoughts |
| `Modes` | added | named tool/permission/model sets | Selected per thought |
| `Datastores` | added | persistence backends (`fs` is the compiled default, `sqlite`, …) | Candidates declared at build time, chosen at runtime; every aggregate routes to the active backend by default. Backs thought state, transcripts, tool state, and memory. RAM is the source of truth, the backend is eventual ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)). `sqlite` is the single-file transactional backend: WAL plus a busy timeout for the datastore's one writer, a metadata projection (`thought_transcript_meta`) so enumerating thoughts or transcripts never reads a body, and transcript bodies encrypted when `NAVI_SESSION_KEY` is set. Every backend is proven by the shared conformance suite (`datastore/conformance`), which an implementation in its own module imports and runs |

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
    Skills       []string    // compiled-in skills to load (ADR 0013)
    DependsOn    []string    // thoughts this one waits for (ADR 0015)
    DeferUntil   time.Time   // durable not-before schedule
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

## Thought dependencies

A plan is a chain, and a chain needs an order that priority cannot express: once
more than one worker runs, the next stage is claimable the instant a slot frees.
`Thought.DependsOn` is that order ([ADR 0015](../adr/0015-thought-dependencies-order-a-plan.md)):

- **A dependency is a scheduling relation, not a lifecycle status.** The thought
  is queued immediately — its record and position are visible — but `Dequeue`
  will not claim it until every id in `DependsOn` has reached `complete`.
  Completing a dependency already emits an event that wakes the pool, so the
  dependent is claimed on the next drain; there is no release step. "Blocked" is
  *derived* from the edges for display (`blocked_by` on the tool view, `blocked`
  in the portal row), so the scheduler and the record keep one vocabulary.
- **A paused dependency is not complete.** Preempting the head of a chain parks
  the whole chain, which is the honest reading: the work it stands for is not
  done.
- **A dead dependency cannot hang its dependents.** A dependency that fails,
  is cancelled, or is removed fails its pending dependents (transitively) with
  a detail naming the dependency; `Retry` re-applies the gate, so the recovery
  is retry-the-dependency-first. A dependency that is already dead when the
  graph loads (crash recovery or external adoption) settles the same way.
- **Edges are validated at the door.** Every id must already be known (a
  successor is enqueued after its predecessor), self-dependency is rejected,
  and an edge that would close a cycle is refused. Together with unique ids that
  makes an in-process graph acyclic by construction.
- **Edges are durable.** `depends_on` persists with the thought row (migration
  15) and recovery preserves the graph, so a restart cannot silently reorder a
  plan. A dependency recovered as `complete` satisfies its dependents.

The model expresses the relation through its own deferral surface:
`thoughts_create` takes `depends_on`, and each call returns the id the next
stage names — "this continues `t1`".

## Closing a plan

A plan of thoughts is not finished when its last stage completes
([ADR 0016](../adr/0016-a-plan-closes-with-a-verification-thought.md)): it closes
with a **verification thought** that checks what the plan produced rather than
extending it. The gap was found in the first end-to-end multi-thought run, where
the model noticed an inaccuracy at the end of a stage and moved on.

- **Guidance owns the check's content.** `core_thoughts` tells the model to end a
  multi-stage plan with a verification thought — `depends_on` the stage it checks,
  metadata `role: verification` — and says what a check is: run the result, read
  it back, diff it. What "verified" means is task-specific, so only the model that
  did the work can write it.
- **The runtime owns the guarantee.** When a plan drains with no check behind it,
  the navi queues one (`WithPlanCloseCheck`, on by default; false turns the
  backstop off and leaves the guidance). The rule is mechanical: the plan is the
  dependency-connected component of the completed thought, every **stage** must be
  `complete` (a verification is a check, not a stage, so it does not count toward
  the plan's shape), a check never spawns another, and the synthesized check
  depends on the plan's **sinks** — released only once the whole plan drained,
  parallel branches included. Its id derives from those sinks, so a replayed event
  or a restart cannot queue a duplicate. A plan is already checked only when a
  verification depends on **exactly the current sinks**, so a plan staged one
  stage at a time — it drains between stages, another of the shapes the graph
  allows — is closed against the sinks it ends at and re-closed when it grows: the
  check for the earlier, smaller sink set is superseded and, if it has not
  started, cancelled first. A check that already ran is kept.
- **A check is identifiable.** `role: verification` on the thought record and a
  `V` flag in the portal row, so a plan's closing check reads as a check rather
  than as more work.
- **A failed plan is not closed.** A stage that fails fails its dependents
  (ADR 0015), and there is nothing verified to report.

## Context assembly

The thought queue **is** the context mechanism ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)). Context pressure is resolved by bounding thoughts and deferring work into follow-up thoughts, not by compacting a long conversation: a thought that approaches its budget finishes a coherent unit, writes what the next one needs into short-term memory, and queues follow-ups that declare which skills to load. A task too large for one thought becomes a plan of thoughts.

- **Assembly happens at thought creation**, not per turn. The composition root installs the assembler on its own thought queue (`Queue.SetAssembler`), so every enqueue path assembles once, at the door: a thought carries its assembled context (`Thought.Assembled`, persisted with the row) and the runner consumes it. A thought that cannot be assembled — an unknown declared skill — is rejected at `Enqueue`, not mid-run. The assembled context is truncated past the navi's budget with a reported marker, never silently.
- **The deferral trigger is a notice, not a guess.** As a run approaches the thought's context budget, the agent loop injects one notice telling it to finish a coherent unit, write the handoff with `memory_write`, and queue follow-ups with `thoughts_create` declaring their skills. There is no compaction to fall back on, so deferral is the only continuation.
- **The assembled context is recorded with the thought** as its first transcript entry (index 0), so it is readable through the same contract as the rest of the transcript ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)) and inspectable by any portal ([ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md)).
- **Everything that shapes context is a build-time declaration** — sources, auto-loaded skills, component-derived guidance. Runtime config may only narrow it. The reason is the threat model, not tidiness: if a prompt can reach the assembler, the assembler is an injection surface (see [security.md](./security.md)).
- **Skills are a menu, not a fetch.** All three loading paths can only select from the compiled-in set, so the worst case of a manipulated request is loading a skill the author already shipped.
- **Core skills ship with the component types**: authored prose for semantics plus a mechanical inventory derived from component metadata, so the model is never unaware of a capability the binary has. The generated set is bounded by the declared component types and kept compact — it is paid on every thought that loads it. Measured cost of the auto-loaded core set for a 7-tool navi: ~300 tokens (pinned by `TestInventoryTokenCost`).
- **Compaction is gone.** It was removed rather than kept as a fallback (NAVI-370); deferral replaces it.

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
    // Objects is the typed-object surface (ADR 0017): the navi's own
    // aggregates and any component types it exposes, read through the
    // descriptor, mutated through declared actions, and kept current by
    // object deltas.
    Objects       object.Surface
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

- **Connectors** are **bidirectional service integrations** ([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)). Inbound, they turn external stimuli into thoughts and manage their lifecycles (spawn/attach/route) — how a service gets the navi's attention. Outbound, they supply the tools that let the navi act on that service: egress tools (`github_issue_reply`, `discord_send`) close over the connector's live client and are added **explicitly** by the author via `connector.SendTool()` — never auto-registered, so the capability surface stays visible in `main.go`. The mechanism is contract-pure: `SendTool` builds an ordinary `tool.Tool` and references only the `tool` package, so an out-of-repo connector author inherits no implementation dependency (ADR 0020). Lifecycle gating is structural rather than a rule each author remembers — the connector publishes its client into a `connector.Live[C]` handle in `Start` and clears it in `Stop`, and a call outside that window returns `connector.ErrNotStarted` instead of dereferencing a nil client. Both halves share one connection, config, and credential. There is no generic egress payload: what is sent is service-specific. Role separation between navis is therefore **structural** — a function of which components and tools exist, not of what a prompt says. `connector/github` is the worked example — wired into the `navi-code` template, where the tool is added explicitly and the credential is an environment reference. A credential may be static (a reference resolved per call), read from the developer's `gh` login, or **minted**: `github.AppInstallation` signs an RS256 JWT with the app's private key and exchanges it for an installation token, caching and re-minting it — before the hour is up and on a 401 — so the token is never per-call (ADR 0020 decision 8). It currently ships the **egress half only**, so it is a partial connector until inbound subscription configuration lands (ADR 0014 open question 3).
- **Portals** are human-facing; they list, schedule, and steer, and stream the
  same queue and running-agent events a connector can observe — through
  `Deps.Observation`, whose bounded mailboxes keep a slow front-end from
  stalling the queue (NAVI-352). A portal also reads a thought's full
  conversation — live and historical — through `Deps.ThoughtSystem`, so attach
  and restart show what happened rather than only what happened next
  (ADR 0011). The portal path is one unified stream per portal, with access
  scoped by IAM rather than by separate read and write surfaces (ADR 0019).
- The SDK ships an HTTP **`portal/gateway`** component: a `portal.Portal`
  that frames the unified portal stream over HTTP — `GET /portal/stream` is
  the SSE downstream (the bounded opening snapshot, observation events, and
  object deltas) and `POST /portal/stream` is the upstream Surface call
  channel — and delegates authorization to the control portal (the `Hub`), so
  there is one authorization path and a remote front-end attaches through the
  same interface as every other portal. The object routes remain as a
  convenience API for non-portal clients (`GET /{type}`, `GET /{type}/{id}`,
  `GET /{type}/{id}/{detail}`, `POST /{type}/{id}/{action}`, plus `GET
  /_types`), as does `POST /portal/control` (which `navi enqueue` uses);
  `gateway.Client` is a stream client that implements both `object.Surface`
  and `portal.Stream`, so a remote portal reads the same objects an
  in-process one does. The TUI is likewise a public `portal.Portal` component
  built from `Deps`, registered by the reference navi through `n.Portals`,
  and its remote transport *is* the `gateway.Client`. The object-route wire
  framing is ADR 0018; the stream's is ADR 0019.
- **Control** is an interface capability too: `portal.Control` authorizes
  attach/control through a `portal.Authorizer` and applies the actions
  through the thought system it receives at `Start`. The gateway delegates
  to it, so a remote front-end can queue, steer, and manage thoughts in
  another process.

The composition root package is named `navi`; connectors and portals never
import it — they depend only on the component packages.

## Typed objects

A portal renders objects, not aggregates (ADR 0017). The navi exposes its own
aggregates as **types** by default — `thought` (with a bounded `transcript`
detail), `entry`, `memory` — and a component's type is registered by the navi
that wires it, reading through the same view the component was bound to (the
reference navi exposes `todo` and `note` over the stateful view its tools
share). `portal.Deps.Objects` is the `object.Surface` a portal consumes:

```
Types() ([]Type, error)           — the descriptor set (a stream can fail)
List / Get / Detail               — the read path: descriptor-enforced,
                                    RAM-first, bounded pages with a cursor
Act(type, id, action, args)       — the write path: a declared action
Watch(after) ([]Delta, int, bool, error)
                                  — the live path: deltas since a cursor
```

The surface enforces the descriptor rather than trusting the portal: fields
marked sensitive are omitted, undeclared fields are dropped, and a page is
bounded. An in-process portal and the gateway's remote binding are two
bindings of the same contract, so a portal is written once.

**Actions.** A type declares its actions — a thought steers, pauses, resumes,
cancels, bumps, and retries; a todo completes, starts, and removes; a note
removes — with optional JSON-Schema arguments. `Act` resolves the type and the
action, then **authorizes before validating** (a caller who may not act learns
nothing about the arguments), validates through the tool registry's schema
checker (an action is checked by the same machinery a tool call is), and only
then delegates to the type's registered handler, which mutates through the view
the type was bound to. Authorization is **fail-closed**: no authorizer wired
refuses every action, and the authorizer is the same `portal.Authorizer` a
control portal uses (`navi.WithObjectAuthorizer`), so a navi has one
authorization story rather than two. Until IAM lands (ADR 0012), the surface
acts as one navi-declared identity; per-client scope is that ADR's open
question.

**Deltas.** `Watch` is the live path: the surface translates the existing
observation fanout — it does not add a second one — into `object.Delta`. A
queue lifecycle transition is a thought `update` carrying the same projection a
`Get` would (a removal is a `delete`; a persist failure is not an object
change); streaming agent content and tool input/output are `append`s to the
thought's `transcript` detail; a persisted short-term-memory mutation is a
memory create/update/delete; and a mutation the surface performed publishes the
resulting state. Deltas land in a bounded, cursor-ed `object.DeltaWindow`
(shared by the in-process surface and the gateway's stream binding, so the two
cannot drift): a slow consumer drops the oldest and is told it has a gap,
never slowing a producer. A consumer is **woken** rather than polling — the
surface exposes `object.Notifier` (`Notify() <-chan struct{}`), and the
observation mailbox exposes `portal.Waker` — so an idle portal is not blind.
A portal therefore keeps its objects current without knowing queue or agent
event kinds.

**The remote binding.** `gateway.Client` opens the unified stream once
(`GET /portal/stream`) and issues Surface calls on the same endpoint's POST
channel, so a portal written against the surface runs in-process or remotely
without a second code path. Every stream is authorized on the fail-closed
`Attach` path, and actions are additionally authorized by the surface itself,
so the gateway never decides who may act. Reads are bounded (the surface's
page cap, not the caller's limit, is what bounds them), and the framing —
routes, bodies, and error mapping — is ADR 0018. The stream's opening snapshot
carries the descriptors and one bounded page per declared type, so a remote
portal renders from the first frame instead of racing an asynchronous fetch;
the delta cursor is captured before the pages are read, so a change during the
snapshot replays as a delta rather than being lost. A reconnecting client
resumes from its cursors (`from`, `events`); a reported gap forces a fresh
snapshot. `Types()` and `Watch()` return errors, so a broken stream is never
reported as "no changes" (ADR 0019 decision 8).

**Portals.** `portal/view` is the framework-agnostic half of a portal: it
resolves a type's descriptor into view models — a list's columns and
kind-formatted cells, a detail's ordered sections, a detail collection as chat
turns, a board's groups, a relation graph — and nothing in it renders, so the
same model feeds a terminal UI, a web UI, and a test. A portal implements each
view kind once, and a new type needs no new screen. The in-process TUI is the
first consumer: its list is descriptor-driven with a live overlay for state the
descriptor does not carry, `t` switches between exposed types, `enter` opens a
conversation or a detail view, and the action keys dispatch through
`Surface.Act`. Its conversation is the thought's `transcript` detail, which is
also why the remote path renders the same thing — reading the queue through the
surface replaced the old remote-only snapshot entirely.

**Transport is a separate axis.** Where a portal's state lives (in-process, a
streaming HTTP binding, a socket) is chosen once at the navi level; the CLI role
(`host` / `connect`) and the connection's identity are different questions. The
portal path is the unified stream (ADR 0019): one stream per portal carrying
the opening snapshot, observation events, object deltas, and control results,
with SSE + POST as the first remote binding and a socket only when a named
consumer needs a low-latency upstream. Both bindings **push**: the TUI's pump
(`tui.Pump`) is woken by the mailbox and hands batches to Bubble Tea's
`Program.Send`, so the 150 ms active / 1 s idle poll cadences are gone and an
idle portal is never blind. A slow UI loses only its own events (bounded,
drop-oldest mailbox) and reports a gap rather than stalling the queue.

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
- [ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md) — the portal stream and IAM scoping (superseded by 0019)
- [ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md) — context assembly, skills, and why the queue is the context limit
- [ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md) — connectors and connector-owned egress
- [ADR 0017](../adr/0017-navi-state-as-typed-objects.md) — typed objects, actions, and live deltas
- [ADR 0018](../adr/0018-object-surface-http-framing.md) — the object surface's HTTP framing
- [ADR 0019](../adr/0019-portal-transport-bindings.md) — portal transport bindings and the unified stream
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones