---
status: accepted
date: '2026-10-03'
supersedes: []
tags:
  - adr
  - thought
  - scheduling
  - context
---

# 0015 — Thought dependencies order a plan

## Context

[ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) decision 1 says
that a task too large for one thought becomes **a plan of thoughts**: a running
thought finishes a coherent unit, writes what the next one needs into short-term
memory, and queues follow-ups. It does not say how a plan keeps its order, and
the first end-to-end multi-thought run showed the gap concretely.

A thought deferred work into several follow-ups through `thoughts_create`. They
all ran, but nothing ordered them. Two mechanisms looked available and neither
works:

- **Priority.** `thoughts_create` accepts a priority, but the queue clamps
  origin-tagged (self-scheduled) work to the conservative band
  (`EffectiveSelfPriority` → `>= 5`) so a navi can never preempt external work.
  The whole self-scheduled band collapses to one value, and even an unclamped
  priority only decides *which* pending thought is claimed first — with more
  than one worker, a later stage is claimable the instant a slot frees, so it
  starts alongside, not after, its predecessor.
- **Arrival order.** The queue sorts by priority, not by insertion, and workers
  drain it concurrently. Arrival order is not a promise at all.

What the run needed was "do not start B until A's output exists". That is a
relation between specific thoughts, not a rank.

## Decision

1. **A thought may declare the thoughts it waits for.** `thought.Thought`
   gains `DependsOn []string`, the ids this thought waits on. It is persisted
   with the row (`depends_on`, migration 15) and copied by `Clone`, because the
   queue hands clones to every reader.

2. **The scheduler honours the relation, and it is not a lifecycle status.**
   `Queue.Dequeue` skips a pending thought until every id in `DependsOn` has
   reached `complete`. The thought is still enqueued immediately, is visible,
   occupies its priority position, and transitions normally once released —
   completing a dependency already emits an event that wakes the pool, so the
   dependent is claimed on the next drain with no release step and no new
   status. **"Blocked" is derived for display** from the unresolved edges
   (`thought.UnmetDependencies`, surfaced as `blocked_by` on the tool view and
   `blocked` in the portal row). One vocabulary, one owner.

3. **Only completion satisfies a dependency.** A paused, failed, cancelled, or
   running dependency leaves the dependent blocked. In particular, preempting
   the head of a chain parks the whole chain, which is the honest reading: the
   work it stands for is not done.

4. **A dead dependency must not hang its dependents.** When a thought fails or
   is cancelled, or its record is removed, its pending dependents are failed
   transitively, each with an `EventFailed` detail naming the dependency. This
   is why `pending → failed` joins the lifecycle matrix: a dependent blocked by
   a dead dependency is *retryable* work, whereas cancelling it would be a
   decision never to do it. `Retry` re-applies the gate, so the documented
   recovery is retry-the-dependency-first. A dependency that is already dead
   when the graph loads — crash recovery or external adoption — settles the same
   way, so a restart cannot leave dependents blocked forever.

5. **The door validates the graph.** `Enqueue` requires every id in `DependsOn`
   to already exist (a successor is enqueued after its predecessor), rejects
   self-dependency and unknown ids with an error naming the id, and refuses an
   edge that would close a cycle. Together with unique thought ids, the
   "dependency already exists" rule makes an in-process graph acyclic by
   construction; the cycle guard exists for graphs loaded from an external
   writer, which bypass the door.

6. **The model expresses it through the surface it already uses.**
   `thoughts_create` takes `depends_on`, and its result carries the created id,
   so an agent chains a plan by naming the ids it just created — "this continues
   `t1`". Dependencies are set at creation only: fixing them there keeps the
   graph acyclic by construction and leaves no repair path to design.

## Consequences

- A plan is trustworthy: with any number of workers, a stage cannot observe a
  world its predecessor has not produced yet. This is the mechanism a "chain of
  thoughts" rests on.
- The relation is durable and inspectable. A restarted navi keeps the graph, and
  a portal or the model can read why a thought is not running.
- A dead dependency surfaces as failed dependents rather than a silently stuck
  chain, and one retry of the dependency can revive it.
- Deletion became a chain-affecting action: removing a pending predecessor fails
  its dependents, because a dependent blocked on a record that no longer exists
  would be stuck with no way to notice.
- **Ordering is not inheritance.** A dependency orders execution; it does not
  copy context. A successor still receives only what short-term memory carries,
  which is ADR 0013's open question 5, untouched here.
- `pending → failed` is an ABI-visible addition to the transition matrix. It is
  additive: no existing transition changed.

## Alternatives considered

- **A self-scheduling priority band that still respects external work.**
  Rejected: priority is a rank, not a relation. It cannot express "after" under
  concurrent workers, and widening the band trades the guarantee that
  self-directed work never preempts external work.
- **Sequence numbers within a plan.** Rejected: a plan can be extended by a
  later thought that was not present when the sequence was written, and
  numbers across plans collide. Ids are already the stable handle.
- **A `blocked` lifecycle status.** Rejected: blockedness is a function of other
  thoughts, not of this one. Storing it would need invalidation on every
  dependency transition and would be wrong after a crash or an external write.
  Deriving it from the edges keeps one source of truth.
- **Failing dependents with `pending → cancelled`.** Rejected: a dependent of a
  failed dependency is recoverable work; the failure record should say so, and
  `Retry` exists.
- **Letting `thoughts_update` rewrite the edges.** Deferred rather than
  rejected: it would need cycle repair and re-scheduling rules. Not needed for
  the plan-in-one-direction case this solves.

## Open questions

1. **Cross-thought context.** Whether a dependency should also imply that the
   dependent inherits any part of the predecessor's output beyond short-term
   memory (ADR 0013 open question 5).
2. **A join's release semantics.** Multiple dependencies release on the last to
   complete; whether a partially-satisfied join should ever start is settled
   (no), but whether a dependent may *observe* predecessor results directly is
   not.
3. **Fan-out ergonomics.** A model that wants N parallel stages and a join must
   create each stage and pass the returned ids. A shorthand (a plan object) is
   not designed.
4. **Loaded cycles.** The door refuses new cycles; a cyclic graph written
   directly to the store is detected by the guard only when an edge is added
   through `Enqueue`. Whether recovery should fail cycle members is open.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the thought queue is the context mechanism; this ADR orders a plan of thoughts
- [ADR 0003](./0003-thought-system-first-class.md) — the thought system is first-class
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — the thought as the persistence aggregate
- [runtime.md](../architecture/runtime.md) — thought dependencies
