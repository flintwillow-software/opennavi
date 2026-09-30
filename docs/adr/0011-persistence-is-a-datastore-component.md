---
status: accepted
date: '2026-09-30'
supersedes: []
tags:
  - adr
  - persistence
  - storage
  - runtime
---

# 0011 — Persistence is a datastore component; RAM is the source of truth

## Context

A running navi persists through handles that accumulated one at a time:
`navi.WithThoughtStore` (thought rows), `navi.WithSessionStore` (transcripts),
`connector/cron`'s `WithStateStore` (tick state), and the stateful tool store
(todos, notes). In practice `cmd/navi` opens one SQLite file and hands it to
three separate typed constructors.

Two properties follow from that shape, and both are wrong for what a navi is:

1. **The store is on the runtime read path.** `AgentRunner` loads the thought's
   session from the store before every run (`sessionFor` →
   `store.Store.LoadSession`) and writes the whole conversation when the run
   ends. Disk latency rides a thought run, and the durable transcript lags
   completion — a partially complete conversation is not visible anywhere.
2. **History is unreachable from the component surface.** `portal.Deps` carries
   no transcript handle; `navi.SessionStore()` and `navi.SessionIDFor()` exist
   but importing `navi` from a portal violates ADR 0004 rule 1. So every portal
   is live-only: attach and render from attach-time onward, restart and lose
   the visible conversation, no replay of a completed thought (NAVI-353).

Nothing states the intended shape, so the design drifts: "RAM is the source of
truth" is written down for thought-state mutations (`thought/flusher.go`,
NAVI-338) but not for history, and RAM growth over a long-lived navi is
unspecified.

## Decision

1. **RAM is the source of truth at runtime.** A running navi's state — thoughts,
   their transcripts, memory tiers — lives in memory. The datastore is an
   **eventual-consistency persistence layer**, never a synchronous read path for
   the runtime and never a handle a component reaches for directly.

2. **The datastore is a component type, and it is called `datastore`.** A new
   collection (`n.Datastores`) holds datastore values, alongside tools,
   connectors, and portals. Implementations are ordinary components in their own
   packages. The package is renamed from `store`, and the old session-CRUD
   interface (`store.Store`, `LoadSession`/`SaveSession`/`ListSessions`) is
   **deleted rather than carried forward** — the datastore replaces it, so
   leaving a second meaning on the name would only invite the wrong one. The
   shipped set is **`fs` (markdown + frontmatter) first, then `sqlite`**;
   `postgres` is expected to be cheap to add, and community backends (`redis`,
   object storage, …) are the point of making it a component.

3. **Build-time set, runtime selection.** A navi declares its **candidate**
   backends at build time (which implementations are compiled in, and the
   default); which one is *active*, and its parameters, come from runtime config.
   This is the providers shape (ADR 0009) and the config merge chain: compiled
   set and default, overridable at runtime.

4. **Handles are views, not backends.** `.Add()` declares a candidate and the
   collection yields a stable typed handle — a `Datastore`-shaped view bound to
   whichever backend becomes active. A tool factory takes that handle at
   construction (`todo.Tools(naviID, ds.Stateful())`), so the dependency is
   explicit in `main.go`, but the handle does not name a concrete backend.
   Registration validates shape; **assembly validates binding** — a handle with
   no active backend to resolve to fails at `Run`, with the other cross-cutting
   invariants. This is what keeps the backend swappable at runtime without
   hiding the dependency in configuration.

5. **Metadata eagerly, detail lazily.** Cold start loads thought and transcript
   **metadata** only — enough to answer "what thoughts exist, what state, how
   long is the transcript". Transcript detail loads on access, and only while
   access keeps it alive: the intended mechanism is Go's `weak` package (1.24+,
   the module is on 1.26), so detail the runtime is no longer touching is
   collectable and re-loadable from the datastore rather than pinned for the
   life of the process. A write to lazily-loaded detail materializes it in RAM,
   marks it dirty, and flushes asynchronously.

6. **Transcript writes are asynchronous, per message.** Writing a transcript
   entry never blocks a run, a transition, or a read. The existing memory-first
   flusher is the pattern to extend (`thought/flusher.go`): mark dirty in
   memory, persist outside any lock. Unlike thought-state mutations, transcripts
   get **no synchronous-exception list** — park/checkpoint must be on disk,
   history need not be. The consequence is stated honestly: a hard kill loses
   the unflushed tail of a transcript.

7. **One writer, globally ordered, batched on flush.** Every asynchronous write
   — thought state, transcripts, tool state, memory — drains through a single
   serialized writer over one ordered queue, with per-aggregate high-water marks
   collapsing a burst into one write (ten content deltas become one append).
   Global ordering is chosen deliberately: on a crash it makes the persisted
   state a **prefix** of what happened, so the most recent recoverable point is
   a clean cut rather than a per-aggregate patchwork. Reads never consult the
   queue, and a failed write is surfaced as an event rather than by blocking a
   producer.

8. **The thought is the persistence aggregate.** A thought owns its transcript.
   The contract reaches it through the thought system — the thought's
   transcript, keyed by thought id — not through a session handle. A portal
   reads live and historical conversation from the same place, and never learns
   which backend produced it. (Branching, labels, and fork position in the
   retired `store.Session` model were never used by a thought transcript; the
   model is dropped with the session-CRUD interface.)

9. **Tools and memory are datastore consumers, not special cases.** The
   datastore exposes typed views over one store: the thought/transcript
   aggregate, stateful tool state, and the memory tiers. A tool binds a handle
   the way `registry.RegisterStateful` already binds one, so the todo and note
   tools become ordinary consumers rather than a parallel persistence path.
   **Short- and long-term memory require a configured datastore** — a navi that
   declares memory without one fails at assembly rather than silently keeping
   memory volatile.

10. **The filesystem backend is a per-navi runtime storage tree**, rooted in the
    application's runtime storage location:

    ```
    ~/.opennavi/<naviname>/thoughts/<thoughtid>/thought.md
    ```

    Frontmatter carries metadata (status, priority, timestamps, model, mode);
    the body carries the transcript. The layout is the `fs` backend's concern and
    is not part of the contract.

## Consequences

- **The datastore leaves the critical path.** No disk latency inside a run, a
  lifecycle transition, or a component read. A navi with a slow or unreachable
  backend keeps working; it just persists more slowly.
- **Portals get history through a neutral entrypoint.** Any portal — TUI,
  gateway, WebUI — can show a thought's full conversation, live and historical,
  without importing `navi` or `internal/` and without knowing the backend
  (NAVI-353's gap closes).
- **Durability is explicitly two-tiered** and documented: thought state is
  memory-first with a short loss window and synchronous checkpoints; history is
  best-effort, async, and may lose its tail on a hard kill — but what survives
  survives in order.
- **RAM is bounded by access, not by navi age.** Cold start is cheap; unaccessed
  detail is collectable.
- **`datastore` becomes a component package**, joining the stdlib-only set.
- **One datastore backs the whole navi.** A navi author selects one backend and
  gets thought state, transcripts, tool state, and memory. The todo/note tools
  stop carrying their own store path, and the memory tiers gain a real
  durability story instead of an implied one.
- **Memory needs a datastore.** A navi that wants short- or long-term memory
  must configure one; there is no memory-without-persistence mode.
- **One handle replaces several options** (`WithThoughtStore`,
  `WithSessionStore`, and the cron/stateful wiring).
- **The public surface may be reshaped freely.** The repository is pre-1.0 with
  no external adopters (ADR 0001) and this is an MVP, so options, interfaces,
  and package layout can break without compatibility shims or deprecation
  cycles.
- **ADR 0010 is not superseded.** Its three layers classify capabilities by their
  relationship to the thought system (tools run *inside* a thought, connectors
  *create* one, portals observe/steer). A datastore sits *beneath* the thought
  system and has no thought relationship, so it is a new component kind and a
  new collection rather than a fourth layer.

## Swapping a datastore

Because handles are views (decision 4), both swap points work:

| Change | How | Rebuild? |
|---|---|---|
| Backend **parameters** (path, DSN) | Runtime config | No |
| Which compiled-in backend is **active** | Runtime config (`datastore.backend`) | No |
| Add a **compiled-in candidate** | `.Add()` in `main.go` | Yes |
| Use a new **community backend** | `import` + `.Add()` in `main.go` | Yes |

A navi that wants runtime switching must declare more than one candidate at
build time; a navi that declares one gets a helpful assembly error if config
names another. Handing a tool factory the concrete backend instead of a view
would make the middle row impossible — that is the constraint this decision
exists to preserve.

## Alternatives considered

- **Keep the store on the read path** (read-through session load per run). It
  works today and needs no new abstraction, but it puts disk in a run, lags the
  durable transcript, and contradicts RAM-first. Rejected.
- **Eager full hydration at startup.** Simple and makes every read a RAM read,
  but RAM grows with navi age and history volume without bound. Rejected in
  favour of metadata-eager/detail-lazy.
- **Datastore as a private runtime detail** (a `navi` option, not a component).
  Less public surface, but community backends become forks and a navi author
  cannot swap storage without touching the SDK. Rejected.
- **Synchronous transcript writes.** Trivially correct, and unacceptable: the
  application must not wait on the backend. Rejected.
- **Keep `store` as the name and delete only the interface.** Least churn, but
  leaves a name that already means "the session database" in this repo's
  history, its docs, and its readers' heads. Rejected: the rename is cheap while
  there are no external adopters.
- **Hand the concrete backend to the tool factory.** Simplest handle, and it
  makes runtime selection impossible — the backend kind would freeze at build
  time. Rejected.
- **Per-aggregate writes with no global order.** Higher throughput and fewer
  shared structures, but a crash can leave a thought `complete` with a truncated
  transcript, and recovery has to reconcile aggregates instead of resuming from
  a prefix. Rejected in favour of one ordered writer.

## Open questions

Recorded rather than silently decided:

1. **Retention and pruning.** Metadata is retained; detail is GC-eligible. That
   is a bounded *cache*, not a retention *policy* — nothing yet decides when a
   transcript is pruned, archived, or dropped, which is also memory.md's open
   question 3.
2. **One active datastore or many.** The collection is shaped as a candidate
   set with one active backend. Whether a navi may split aggregates across
   several active backends (transcripts on `fs`, memory on `postgres`) is not
   decided, and would change the handle shape.
3. **Where the ordered writer lives.** Whether the single writer is the
   thought-system flusher extended to carry every aggregate, or a separate
   datastore-level writer the flusher feeds.

## Related

- [runtime.md](../architecture/runtime.md) — collections, Deps, thought system
- [memory.md](../architecture/memory.md) — short/long-term tiers, retention
- [ADR 0004](./0004-component-packages-public-implementations-private.md) — component packaging
- [ADR 0005](./0005-build-time-modules-are-the-extension-path.md) / [0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — build-time vs runtime
- [ADR 0009](./0009-providers-set-with-per-thought-selection.md) — the same set/selection shape
- [ADR 0010](./0010-three-capability-layers.md) — why this is a new component kind, not a fourth layer
