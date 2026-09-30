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

# 0011 — Persistence is a backend component; RAM is the source of truth

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
   the visible conversation, no replay of a completed thought
   (NAVI-353).

Nothing states the intended shape, so the design drifts: "RAM is the source of
truth" is written down for thought-state mutations (`thought/flusher.go`,
NAVI-338) but not for history, and RAM growth over a long-lived navi is
unspecified.

## Decision

1. **RAM is the source of truth at runtime.** A running navi's state — thoughts,
   their transcripts, memory tiers — lives in memory. The backend store is an
   **eventual-consistency persistence layer**, never a synchronous read path for
   the runtime and never a handle a component reaches for directly.

2. **The backend store is a component type.** A new collection
   (`n.Stores`) holds backend values, alongside tools, connectors, and portals.
   Implementations are ordinary components in their own packages; a navi author
   selects one. The shipped set is **`fs` (markdown + frontmatter) first,
   then `sqlite`**; `postgres` is expected to be cheap to add, and community
   backends (`redis`, object storage, …) are the point of making it a component.

3. **Build-time set, runtime selection.** Which backends are compiled in is a
   build-time decision (ADR 0005/0006); *which* one is used, and its
   parameters, are runtime config with build-time defaults. This is the same
   shape as providers (ADR 0009) and the config merge chain: compiled default,
   overridable at runtime.

4. **Metadata eagerly, detail lazily.** Cold start loads thought and transcript
   **metadata** only — enough to answer "what thoughts exist, what state, how
   long is the transcript". Transcript detail loads on access, and only while
   access keeps it alive: the intended mechanism is Go's `weak` package (1.24+,
   the module is on 1.26), so detail the runtime is no longer touching is
   collectable and re-loadable from the backend rather than pinned for the life
   of the process. A write to lazily-loaded detail materializes it in RAM, marks
   it dirty, and flushes asynchronously.

5. **Transcript writes are asynchronous, per message.** Writing a transcript
   entry never blocks a run, a transition, or a read. The existing memory-first
   flusher is the pattern to extend (`thought/flusher.go`): mark dirty in
   memory, persist outside any lock. Unlike thought-state mutations, transcripts
   get **no synchronous-exception list** — park/checkpoint must be on disk,
   history need not be. The consequence is stated honestly: a hard kill loses
   the unflushed tail of a transcript. History is best-effort; thought-state
   durability is unchanged.

6. **The thought is the persistence aggregate.** A thought owns its transcript.
   The contract reaches it through the thought system — the thought's
   transcript, keyed by thought id — not through a session handle. A portal
   reads live and historical conversation from the same place, and never learns
   which backend produced it. (Branching, labels, and fork position in the
   retired `store.Session` model were never used by a thought transcript; the
   model either reduces to a storage encoding or is dropped.)

7. **The filesystem backend is a per-navi runtime storage tree**, rooted in the
   application's runtime storage location:

   ```
   ~/.opennavi/<naviname>/thoughts/<thoughtid>/thought.md
   ```

   Frontmatter carries metadata (status, priority, timestamps, model, mode);
   the body carries the transcript. The layout is the `fs` backend's concern and
   is not part of the contract.

## Consequences

- **The store leaves the critical path.** No disk latency inside a run, a
  lifecycle transition, or a component read. A navi with a slow or unreachable
  backend keeps working; it just persists more slowly.
- **Portals get history through a neutral entrypoint.** Any portal — TUI,
  gateway, WebUI — can show a thought's full conversation, live and historical,
  without importing `navi` or `internal/` and without knowing the backend
  (NAVI-353's gap closes).
- **Durability is explicitly two-tiered** and documented: thought state is
  memory-first with a short loss window and synchronous checkpoints; history is
  best-effort, async, and may lose its tail on a hard kill.
- **RAM is bounded by access, not by navi age.** Cold start is cheap; unaccessed
  detail is collectable.
- **`store` becomes a component package.** Its public surface changes from
  session CRUD to the backend/aggregate contract; `MemoryStore` becomes the
  volatile default. `store` joins the stdlib-only component packages.
- **One backend handle replaces several options** (`WithThoughtStore`,
  `WithSessionStore`, and the cron/stateful wiring) — a breaking option change,
  deliberate.
- **ADR 0010 is not superseded.** Its three layers classify capabilities by their
  relationship to the thought system (tools run *inside* a thought, connectors
  *create* one, portals observe/steer). A backend store sits *beneath* the
  thought system and has no thought relationship, so it is a new component kind
  and a new collection rather than a fourth layer.

## Alternatives considered

- **Keep the store on the read path** (read-through session load per run). It
  works today and needs no new abstraction, but it puts disk in a run, lags the
  durable transcript, and contradicts RAM-first. Rejected.
- **Eager full hydration at startup.** Simple and makes every read a RAM read,
  but RAM grows with navi age and history volume without bound. Rejected in
  favour of metadata-eager/detail-lazy.
- **Backend as a private runtime detail** (a `navi` option, not a component).
  Less public surface, but community backends become forks and a navi author
  cannot swap storage without touching the SDK. Rejected.
- **Synchronous transcript writes.** Trivially correct, and unacceptable: the
  application must not wait on the backend. Rejected.
- **One `Session` model shared with branching.** Reuses existing types, but the
  branching/label/fork machinery was never exercised by thought transcripts and
  would leak storage-shaped concepts onto the contract. Rejected.

## Open questions

Recorded rather than silently decided:

1. **Retention and pruning.** Metadata is retained; detail is GC-eligible. That
   is a bounded *cache*, not a retention *policy* — nothing yet decides when a
   transcript is pruned, archived, or dropped, which is also memory.md's open
   question 3.
2. **Scope of one backend.** Whether stateful tool state (todo/note) and cron
   tick state move under the same backend contract or stay separate typed stores
   sharing one backend instance.
3. **Naming.** Whether the component package keeps the name `store` (with
   `store/fs`, `store/sqlite` under it) or is renamed (`backend`, `storage`) to
   distinguish it from the retired session-store surface.
4. **Write ordering across aggregates.** With async writes, whether a
   per-thought high-water mark (append entries after the last flushed index) is
   sufficient, or cross-aggregate ordering needs a sequence.

## Related

- [runtime.md](../architecture/runtime.md) — collections, Deps, thought system
- [memory.md](../architecture/memory.md) — short/long-term tiers, retention
- [ADR 0004](./0004-component-packages-public-implementations-private.md) — component packaging
- [ADR 0005](./0005-build-time-modules-are-the-extension-path.md) / [0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — build-time vs runtime
- [ADR 0009](./0009-providers-set-with-per-thought-selection.md) — the same set/selection shape
- [ADR 0010](./0010-three-capability-layers.md) — why this is a new component kind, not a fourth layer
