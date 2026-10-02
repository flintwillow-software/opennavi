---
status: draft
created: '2026-09-26'
updated: '2026-09-30'
tags:
  - architecture
  - memory
---

# Memory

> The two-tier memory model: what a navi always has in context, and what it
> searches for on demand. Recovered from the retired spec tree so the design
> stops drifting — see [Provenance](#provenance).

## What this is for

A navi needs two different relationships with its own knowledge.

- Some of it should be **in front of it at every thought** — who it is working
  for, how that person prefers to be spoken to, what the current task is.
  Making the agent ask for that is friction.
- The rest is a **knowledge base**: too large to carry, and useful only when the
  answer is not already to hand. That should be searched, not loaded.

Those are the two tiers. They are distinguished by **load timing**, not by
importance and not by storage.

## Two axes: ownership and load timing

The original design defined four **scopes** by *ownership*, then classified them
by *load timing*. Both axes hold, and conflating them is what lost the design.

| Scope | Owns | Load timing | Purpose |
|---|---|---|---|
| `session` | working state for one interaction | on resume / explicit recall | current task, step count, open questions |
| `user` | notes about a human | **always injected** | preferences, style, lessons learned |
| `navi` | notes about another navi | when interacting with that navi | how to communicate, delegate, collaborate |
| `memories` | the knowledge archive | **only when needed, searched** | facts and learned information |

The load-timing column is the tier boundary:

- **Short-term** — `user` (with `session`): always in context.
- **Long-term** — `memories` (with `navi`): searched on demand.

One rule from the original design is worth keeping in spirit:

> The agent searches this archive only when it cannot find the answer in current
> context, user memory, or session state. This keeps the always-in-context data
> minimal and focused.

## Short-term memory

Always in context. Injected into the prompt for every thought, with no tool call
required. The navi may write to it during a thought, and those writes are
visible to subsequent thoughts.

**Short-term memory is the inter-thought carrier, and that makes it
load-bearing.** Context pressure is resolved by deferring work into follow-up
thoughts rather than by compacting a long conversation
([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)): a thought
approaching its budget finishes a coherent unit, writes what the next one needs
into short-term memory, and queues follow-ups that declare which skills to load.
So short-term memory is not a convenience on top of the queue — it is the
handoff that makes the queue usable as the context mechanism, which is why
NAVI-351 sits on the critical path and requires a configured datastore.

**It is navi-internal, not a public component package.** It is the navi's own
working state rather than a capability an extension supplies, and ADR 0010's
three capability layers — [tools, connectors, portals](../adr/0010-three-capability-layers.md)
— do not include it. So it is absent from `connector.Deps` and `portal.Deps`
as a handle a component may write. When the tiers land, a portal *views* them
through the navi stream ([ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md),
proposed) rather than through a datastore handle. M1 (NAVI-345) publishes no memory
interface.

One consequence is accepted deliberately: an `internal/` package is importable
by the runtime and by in-repo `tool/`, but **not** by out-of-repo extension
modules. Third-party tools therefore cannot read or write short-term memory.
Reversing that later means publishing a public handle.

**Per-thought, not per-session.** Each thought is a fresh agent run
([runtime.md](./runtime.md)), so injection happens per thought. This is a
deliberate change from the original design, which injected at session start: per
thought is what makes a write during thought *N* visible in thought *N+1*.

### Intentional, not incidental

Short-term memory holds what the navi **deliberately tucks away**. That is what
separates it from the session record.

Sessions and thought transcripts are *history*, not a datastore: they are the
incidental artifact of a thought having run, kept for continuity, replay, and
operator inspection. A thought owns its transcript, and the runtime persists it
asynchronously through the selected datastore — RAM is the source of truth
and history is best-effort, so a hard kill may lose its tail
([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)). Nothing chooses
what lands there; it is whatever happened.

Memory is the opposite. An entry exists because the navi decided it was worth
keeping. So memory grows deliberately and slowly rather than in proportion to run
volume, and most thoughts will write nothing at all.

Both tiers **require a configured datastore** ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)):
memory is persisted through it rather than held only in RAM, and a navi that
declares memory without a datastore fails at assembly instead of silently losing
what it learned.

The natural write point is therefore **the end of a thought** — a post-thought
analysis pass, at the same lifecycle point where the runner already flushes the
session. Whether that pass is a runtime step or a tool the agent calls, and
whether the navi may also write mid-thought, is left open below.

Tracked as NAVI-351.

## Long-term memory

Searched, and never loaded wholesale. It is an agent-callable **tool** in ADR
0010's sense: the agent reaches for it when the answer is not already in
context. A human prompting recall ("what did we do about X last time?") is a
thought whose prompt asks for recall, resolved through the same tool — one
mechanism covers both entry points.

The archive is both keyword- and vector-indexed, so the tool takes an embedder
supplied by the navi author at construction. It is not a thin CRUD tool.

Tracked as NAVI-350.

### The tiers are a system, not two stores

Short-term memory may record a lightweight **pointer** that relevant information
exists in long-term, so the navi knows to go looking. That is what makes the two
tiers a system: the cheap tier knows what the expensive tier holds. Preserve it
when implementing either side.

## What memory is not

- **Not the thought system.** That is the runtime's main loop — it decides what
  runs next, not what a navi knows. It is the *queue*.
- **Not `datastore.MemoryStore`.** That is the volatile backend — the in-memory
  default that persists nothing — unrelated to the memory tiers.
- **Not the thought transcript.** Sessions record history — what happened.
  Memory records what the navi chose to keep. A portal reads the transcript
  through the thought system, not through a datastore handle, and never learns
  which backend produced it ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md)).
- **Not `note`.** [`tool/note`](../../tool/note/) is a per-navi verbatim
  scratchpad, and its own documentation calls it "deliberately distinct from
  semantic memory". Notes are written and read back exactly; memory is curated
  and searched.
- **Not the retired `internal/memory` package.** It implemented three of the
  four scopes and dropped the load-timing dimension entirely, and it was never
  wired into anything — deleted in the M7 cleanup. Its surface is not the
  interface — see [Provenance](#provenance).

## Open questions

Recorded rather than silently decided:

1. **Where the `navi` scope belongs.** It loads when interacting with a specific
   navi, so it is neither always-loaded nor purely on-demand. It could sit with
   either tier; the original design describes it as searchable, which argues for
   long-term.
2. **The write path.** Is the post-thought pass a runtime step the navi always
   performs, or a tool the agent calls? May the navi also write mid-thought, or
   only at thought end? Deliberate writes argue for one well-defined opportunity
   per thought rather than an ad-hoc surface.
3. **Retention and pruning.** The original design said "long-term with pruning"
   for both `navi` and `memories`, without defining a policy. Because short-term
   writes are deliberate rather than volume-driven, pruning can also be a
   deliberate act rather than a heuristic. Datastore history follows the same
   stance: retention defaults to unbounded and a prune mechanism ships with the
   datastore, invoked explicitly rather than by a background heuristic
   ([ADR 0011](../adr/0011-persistence-is-a-datastore-component.md), decision 11).
   The memory tiers' own retention policy is still open.

## Provenance

This document exists because the design had been lost. It originally lived in
`docs/specs/06-memory/`, retired in #308 by
[ADR 0001](../adr/0001-documentation-model.md) (docs-as-code). It is recoverable
from history:

```sh
git show f5ddc61~1:docs/specs/06-memory/00-memory-system.md
git show f5ddc61~1:docs/specs/06-memory/01-vector-knowledge-base-design.md
```

Two things went wrong, neither worth repeating:

1. **The name was taken by something else.** [runtime.md](./runtime.md) called
   the thought system the navi's "short-term memory" — the queue, which is a
   different thing. With the term occupied, the original meaning had nowhere to
   live.
2. **The tier axis never reached code.** The (now-retired) `internal/memory`
   package kept the scopes as a `Scope` type but not the load timing that makes
   them two tiers, and it was never wired into anything: zero importers, not
   reachable from any binary.

The [rewrite plan](./rewrite-plan.md) scoped the memory subsystem *rewrite* out
of the composition work. That meant "do not rewrite it", and was read as "do not
document it" — so it drifted. Capturing the model here is the fix.

## Related

- [overview.md](./overview.md) — package layout and boundaries
- [runtime.md](./runtime.md) — thought system, agent runs, lifecycle
- [extension-model.md](./extension-model.md) — what belongs on the extension surface
- [rewrite-plan.md](./rewrite-plan.md) — milestones
- [ADR 0010](../adr/0010-three-capability-layers.md) — why short-term memory is
  not one of the three capability layers, and long-term memory is a tool
- [ADR 0011](../adr/0011-persistence-is-a-datastore-component.md) — where history
  and memory are persisted, and why the backend is not a read path
- [ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md) — why the
  queue, not compaction, is how context stays bounded