---
status: accepted
date: '2026-10-27'
supersedes: []
tags:
  - adr
  - memory
  - context
  - skills
---

# 0037 — Memory is two tiers: the runtime loads, tools manage

## Context

[memory.md](../architecture/memory.md) describes a **two-tier** model — knowledge
that is always in front of the navi, and a knowledge base it searches. The tiers
are distinguished by **load timing**, not by importance and not by storage:

| Scope | Owns | Load timing | Purpose |
|---|---|---|---|
| `session` | working state for one interaction | always injected | current task, open questions |
| `user` | notes about a human | always injected | preferences, style, lessons |
| `navi` | notes about another navi | when interacting with it | how to communicate, delegate |
| `memories` | the knowledge archive | searched on demand | facts and learned information |

**The load-timing axis never reached code.** The document says so itself:

> The (now-retired) `internal/memory` package kept the scopes as a `Scope` type
> but not the load timing that makes them two tiers.

What exists today is the **short-term half only**:

- `internal/memory` is, in its own words, "the always-loaded short-term memory
  tier". It hydrates `alwaysLoaded = []datastore.Scope{ScopeUser, ScopeSession}`
  once at assembly, injects those notes into every thought's system prompt
  without a datastore read on the run path, and exposes `memory_write`.
- `memory_write` supports add, replace, and remove — but its `scope` enum is
  exactly `["user", "session"]`. So **`ScopeNavi` and `ScopeMemories` exist in
  the datastore and are reachable through no tool at all.** A navi cannot write
  long-term memory, cannot write its own notes, and cannot read either.
- There is no long-term *read* surface, no embedder, no pointer mechanism, and
  no retention policy. The datastore primitive exists — `datastore.Memory.Recall`
  (`datastore/memory.go:83`) already selects records "most relevant first" — but
  no tool reaches it, so the capability is unreachable from a thought.

So the navi has a working handoff between thoughts and **no knowledge base**,
while the documentation describes both. The gap is documented rather than
unknown — the doc's own open questions are the write path, where the `navi`
scope belongs, retention and pruning, and (unstated) what a long-term search
returns into a bounded context.

**Why this is load-bearing, not a nice-to-have.** Short-term memory is the
inter-thought carrier ([ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md)):
a thought approaching its budget finishes a coherent unit, writes what the next
needs into short-term memory, and queues follow-ups. Deferral *is* the context
mechanism, so short-term memory is what makes the queue usable rather than a
convenience on top of it. A navi in a chaotic environment
([ADR 0035](./0035-priority-classes-and-strict-preemption.md),
[ADR 0036](./0036-a-connector-event-triages-itself.md)) also needs
somewhere to *park* a conclusion it cannot act on now — which is long-term
memory, and today there is nowhere.

## Decision

1. **Loading is a runtime responsibility; management is a tool responsibility.**
   These are different jobs and they are owned by different layers:

   - **The runtime loads.** Which scopes are always-injected is a **compiled
     declaration** (ADR 0006/0013 decision 4) applied per thought. The navi does
     not ask for its own working state; it is in front of it. Injection happens
     per thought, not per session, which is what makes a write during thought *N*
     visible in thought *N+1*.
   - **Tools manage.** Adding, replacing, and removing entries in **any** tier is
     an agent-callable tool, so a navi curates its own memory deliberately. This
     is the same shape as ADR 0010's tool layer: the capability is a tool, and
     the agent reaches for it when it has something worth keeping.

   The split is the point: **a runtime step cannot be skipped and cannot be
   reasoned about by the agent; a tool can be both used and ignored.** Loading
   must be guaranteed (a thought that lost its working state would be
   incoherent), while curation must be deliberate (memory grows because the navi
   decided something was worth keeping — the doc's own rule: *most thoughts will
   write nothing at all*).

2. **Reading long-term memory is a tool call, never a runtime load.** The
   archive is too large to carry, so it is searched when the answer is not
   already to hand. There is no path by which long-term memory is injected
   wholesale — that is what keeps the always-in-context set minimal, and it is
   the tier boundary rather than an optimisation.

3. **A search returns a bounded result into a bounded context.** This is the
   question the architecture doc leaves open and the one that decides whether the
   archive is usable. A query returns a **bounded, ranked** set of entries with
   enough of each to judge relevance, and the bound is explicit rather than
   whatever the index happens to produce. A tool that can return an unbounded
   result into a thought with a fixed budget is a way to fail a thought, not a
   way to inform it. The contract is the same shape as
   [ADR 0036](./0036-a-connector-event-triages-itself.md)'s bounded
   queue read: a bounded window into something larger.

4. **The two tiers are a system, through pointers.** Short-term memory may record
   a **lightweight pointer** that relevant information exists in long-term, so
   the navi knows to go looking. Without this the tiers are two unrelated stores
   and the navi must guess whether the archive holds anything; with it, the cheap
   tier knows what the expensive tier contains. This is preserved from the
   original design deliberately.

5. **The `navi` scope is long-term.** The doc leaves it open. It loads "when
   interacting with a specific navi", which is a *condition*, not a load time —
   and a navi does not know in advance that it will interact with a given peer,
   so it cannot be always-injected without paying context for every peer it might
   ever meet. It is therefore searched like `memories`, and the pointer mechanism
   is what tells a navi to look.

6. **The always-injected tier is bounded; the searched archive is not.**
   Short-term memory costs context on **every** thought, so its growth is a cost
   paid by all future work; long-term memory costs context only when searched, so
   it may grow. This is the asymmetry that makes the tiers different in kind, and
   it is a policy the runtime enforces rather than guidance the navi is trusted
   to follow.

7. **Retention is deliberate, not heuristic.** Following
   [ADR 0011](./0011-persistence-is-a-datastore-component.md) decision 11's
   stance on datastore history — unbounded by default, pruned by an explicit act
   rather than a background sweep — memory pruning is something the navi or the
   operator *does*, and the tool surface must expose it. Because short-term
   writes are deliberate rather than volume-driven, a prune is a considered act
   rather than garbage collection.

8. **Every tier requires a datastore, and the tiers are visible.** A navi that
   declares memory without a datastore fails at assembly rather than silently
   losing what it learned (ADR 0011). Memory records are ordinary objects a
   portal views through the navi stream (ADR 0012/0017) — a navi's memory is
   inspectable, which is what makes "why did it think that" answerable.

9. **The write path is the tool surface, and there is no separate post-thought
   pass.** The doc leans toward a runtime pass at thought end. This decision
   takes the other branch: curation is a tool the agent calls, mid-thought or at
   the end, because a runtime pass would have to *decide what to keep* — and that
   decision needs the reasoning the thought just did. A pass that summarises
   mechanically produces the incidental artifact the doc explicitly distinguishes
   memory from. The handoff that deferral depends on is therefore a **skill and
   tool** obligation, stated in the core memory guidance, not a runtime
   guarantee.

   This is the one decision here that trades a guarantee for judgement, and it is
   taken deliberately: the operator's model is that a navi *manages* its memory
   tiers, which is a tool-shaped job.

10. **A thought writes as it goes, not only at the end.** Guidance encourages
    writing what matters **while the thought can**, rather than saving the
    handoff for a wrap-up. The reason is
    [ADR 0035](./0035-priority-classes-and-strict-preemption.md) decision 3: a
    p0 preemption **cancels** a thought and gives it no chance to write a final
    note. A runtime-synthesised floor covers that case (ADR 0035), but it is
    mechanical — the thought's own writes are the real handoff, and a thought
    that deferred them may lose them entirely.

    The context-churn cost of more frequent writes is accepted deliberately.
    Short-term memory is the inter-thought carrier, and losing a handoff costs
    far more than carrying a note.

## Consequences

- **The navi can park and retrieve.** A conclusion it cannot act on now goes
  into long-term memory and is found later by search — the thing a navi in a
  chaotic environment most needs and today cannot do at all.
- **The tier boundary becomes real.** Load timing stops being a table in a draft
  document and becomes the difference between what is injected and what is
  queried, which is what the design was always about.
- **`ScopeNavi` and `ScopeMemories` become reachable**, closing the gap where
  four scopes existed in the datastore and two were usable.
- **The resume guarantee does not depend on the model choosing to write.** A
  p0 preemption cancels the thought and gives it no opportunity to write a final
  note (ADR 0035 decision 3), so the **runtime** synthesises a mechanical handoff
  at park time — no model call, so p0 keeps its immediacy. That note is a floor,
  not the mechanism: a thought's own writes, made as it goes, are the ceiling and
  are what actually make a resume good. Guidance therefore encourages writing
  mid-thought rather than deferring to a wrap-up.
- **The handoff depends on the model choosing to write.** With no runtime pass,
  a thought that does not call the tool leaves nothing for its successor. The
  queue's deferral idiom is only as good as the skill that teaches it, which
  makes the memory skill load-bearing rather than decorative — and means the
  failure mode is silent (a follow-up that starts with no context) rather than
  loud. Accepted deliberately; the mitigation is guidance plus the fact that a
  scout ([ADR 0036](./0036-a-connector-event-triages-itself.md)) is
  the natural place to write it.
- **Short-term memory has a size policy to design and enforce.** Bounded means a
  number, and a navi that cannot add a note because the tier is full needs a
  defined behaviour (refuse, evict oldest, or require consolidation).
- **A search surface is a context-cost surface.** Its bound is a design parameter
  that every thought using it pays.
- **Memory is inspectable**, so a navi's retained knowledge is reviewable rather
  than private — consistent with the object surface and with "why did it do that"
  being answerable from the datastore.

## Alternatives considered

- **A runtime post-thought memory pass.** Rejected: it must decide what to keep
  without the reasoning the thought just did, which produces exactly the
  incidental artifact the design distinguishes memory from. It also makes
  retention a function of run volume rather than of judgement, which the doc's
  own rule (*memory grows deliberately and slowly*) rules out. The doc leaned
  this way; this ADR takes the other branch and says so.
- **Both a runtime pass and tools.** Rejected as the primary shape: two writers
  with different criteria for what is worth keeping is how a memory system
  becomes untrustworthy. A tool-only surface has one answer to "why is this
  here".
- **Inject long-term memory wholesale at a high level of summary.** Rejected:
  the tier boundary is load timing, and summarising the archive into every
  thought reintroduces exactly the context cost the two-tier model exists to
  avoid.
- **Put `navi` scope in short-term.** Rejected: a navi would pay context for
  every peer it might ever interact with, to save a search in the minority of
  thoughts that interact with one.
- **Unbounded short-term memory.** Rejected: it is a cost paid by every future
  thought, so growth compounds.
- **Heuristic pruning (age or count).** Rejected: short-term writes are
  deliberate, so pruning can be too; a heuristic would silently discard something
  the navi chose to keep.
- **A public memory handle for connectors and portals.** Rejected here, as the
  doc already argues: memory is the navi's own working state, not a capability a
  component supplies (ADR 0010). Portals view it through the navi stream.

## Open questions

1. **The short-term bound.** What the limit is, and what happens when it is
   reached — refuse, evict, or force consolidation.
2. **Search result shape.** How many entries, how much of each, and how the
   ranker trades recency against relevance.
3. **The embedder.** The doc requires one supplied by the navi author at
   construction; whether a navi without an embedder gets keyword-only search or
   no long-term tier at all is undecided.
4. **Pointer lifecycle.** How a short-term pointer is invalidated when its
   long-term target is pruned, and whether pointers expire.
5. ~~**Mid-thought vs. end-of-thought writes.**~~ **Settled: write as you go.**
   Guidance encourages mid-thought writes rather than deferring everything to a
   wrap-up. The reason is [ADR 0035](./0035-priority-classes-and-strict-preemption.md)
   decision 3: a p0 preemption **cancels** the thought and does not give it the
   chance to write a final note, so a thought that saved its handoff for the end
   may lose it. A runtime-synthesised floor exists for exactly that case, but it
   is mechanical and worse than what the thought itself would write — so the
   thought's own writes, made while it still can, are the real handoff. The
   context-churn cost is accepted: short-term memory is the inter-thought
   carrier, and losing a handoff costs far more than carrying a note.
6. **Prune tooling.** Whether pruning is one tool with a filter, or explicit
   per-entry removal, and whether the operator has a separate surface.
7. **Cross-thought context.** [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md)
   open question 5 and [ADR 0015](./0015-thought-dependencies-order-a-plan.md)
   open question 1 both ask whether a dependent inherits any part of its
   predecessor's context by default. Memory is the current answer (it inherits
   only what was written), and this ADR does not change that.

## Related

- [ADR 0010](./0010-three-capability-layers.md) — why memory is not a capability layer, and long-term memory is a tool
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — where memory is persisted; retention stance
- [ADR 0012](./0012-portals-attach-to-one-navi-stream.md) — how a portal views memory
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the queue is the context mechanism; memory is the inter-thought carrier
- [ADR 0015](./0015-thought-dependencies-order-a-plan.md) — ordering is not inheritance
- [ADR 0017](./0017-navi-state-as-typed-objects.md) — memory as inspectable objects
- [ADR 0036](./0036-a-connector-event-triages-itself.md) — the scout, and the bounded queue read this ADR mirrors
- [memory.md](../architecture/memory.md) — the two-tier model this ADR implements
