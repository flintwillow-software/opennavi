---
status: proposed
date: '2026-10-27'
supersedes: []
tags:
  - adr
  - thought
  - scheduling
  - priority
---

# 0035 — Priority is urgency; strict preemption; time breaks ties

## Context

The queue is a priority queue with preemption, but the rules that govern it are
weaker than they look, and one of them is not a rule at all.

**What exists today:**

- `sortPending` orders by `EffectivePriority()` ascending (`thought/queue.go:210`),
  so a lower number is more urgent.
- `HighPriorityThreshold = 3`: thoughts at priority ≤3 may use reserved slots
  and preempt running thoughts (`preemptCandidate`, `queue.go:811`). A thought
  at priority 4+ cannot preempt anything, however much lower the victim is.
- Preemption is cooperative and correct: the queue sets `PreemptRequested`,
  the pool cancels the victim's run context (`thought/worker.go:77`), the
  runner snapshots its checkpoint, and the worker parks the thought
  (`PauseWithCheckpoint`, a durability-critical synchronous write). `Resume`
  returns it to pending **at its own priority**, with the checkpoint intact, so
  a parked thought continues rather than restarts.
- Self-scheduled work is clamped to the conservative band
  (`EffectiveSelfPriority` → ≥5, `SelfScheduledHighBound`) so a navi cannot
  schedule itself ahead of external work.

**What is not true:**

1. **"Any higher priority preempts" is not the rule.** The threshold gate means
   a p5 arriving while a p7 runs is ignored, even though p5 is strictly more
   urgent. The gate encodes "only genuinely urgent things interrupt", which was
   a reasonable default when a navi was a single-purpose worker; it is the wrong
   rule for a navi juggling a chaotic inbound stream, where *relative* order is
   what keeps the work sane.
2. **"Equal priority is oldest-first" is an accident.** `sortPending` uses
   `sort.SliceStable`, so insertion order survives among equals — in memory. But
   the guarantee does not hold across the paths that matter:
   - crash recovery loads via `LoadThoughts`, which orders by
     `ORDER BY priority, updated_at` (`datastore/sqlite/thought_store.go:105`) —
     the secondary key is *last activity*, not arrival, so a thought that was
     touched recently sorts behind a younger untouched one;
   - `Resume` re-appends to `pending` and re-sorts stably, so a **parked**
     thought rejoins at the *end* of its priority band, behind everything that
     arrived while it was parked — though it was in the queue first.

   A scheduler whose ordering is an emergent property of a sort implementation
   and two storage queries cannot be reasoned about, and the operator's model
   depends on time being the tiebreak.

**The operator's model**, stated plainly: a navi receives a chaotic stream of
notifications and must stay focused. Priority expresses *urgency*, time
expresses *arrival*, and a strictly more urgent thing always gets to interrupt.

## Decision

1. **Priority is urgency, and 0 is the highest.** The scale is 0–10, ascending
   in urgency as the number falls. Three bands are named because they carry
   distinct *behaviour*, not merely distinct rank:

   | Priority | Meaning | Behaviour |
   |---|---|---|
   | **0** | urgent | interrupts: park the running thought, come back to it |
   | **1** | "respond to me" | parks anything 2-or-lower, then addresses it |
   | **2+** | planned work | yields to anything strictly higher |

   The bands are documentation and convention, not a hard-coded table: the
   scheduler implements the *rule* in decision 2, and the bands are how the
   connectors and the scout use it.

2. **Preemption is strictly-higher-wins; the fixed threshold is removed.**
   `HighPriorityThreshold` no longer gates preemption. A pending thought may
   preempt a running thought whenever its priority is **strictly lower**
   (more urgent) than the victim's — the existing "strictly lower victim"
   comparison in `preemptCandidate` becomes the whole rule, and the
   `EffectivePriority() > HighPriorityThreshold` early return goes away.

   The threshold constant is retained only if it still marks something real;
   otherwise it is deleted rather than left as a misleading vestige.

3. **Time breaks ties, explicitly, in every path.** A thought's position among
   equals is `(priority, created_at)` — arrival order, not last-activity order
   and not insertion order. Concretely:
   - the in-memory comparator becomes `(priority, created_at)` rather than
     relying on `SliceStable`;
   - `LoadThoughts` orders by `ORDER BY priority, created_at`;
   - a **resumed** thought keeps its original arrival position within its
     priority band, because it was in the queue before the work that arrived
     while it was parked.

   This is the one place the decision is not a pure refactor: it changes
   observable order after a restart and after a resume. That is the point —
   the order becomes a stated rule instead of an artifact.

4. **The self-scheduled clamp stays.** `EffectiveSelfPriority` continues to
   clamp origin-tagged work to ≥5, so a navi that schedules its own follow-ups
   cannot preempt external work. A navi assigned a low-priority issue can decide
   it is genuinely low priority and let it wait while it finishes what it is
   doing — which is exactly what the clamp makes safe.

   The one thing the clamp cannot express is a **scout** scheduling work on a
   connector's behalf at connector-level urgency. That is ADR 0036's subject,
   and it is deliberately a separate decision with its own authority model
   rather than a hole punched in this one.

5. **Connectors declare a priority hint on the thoughts they spawn.** A
   connector is closer to the event than the queue is, so it states the urgency
   it knows: a Huly issue comment at 1 (the user is waiting for feedback), a new
   assignment at 2 (it has a place to fall that does not interrupt the current
   interaction), an urgent issue at 0. `huly.Subscription.Priority` already
   exists for this and is currently unused by `navi-bastilli`; the same field
   is expected of every connector's inbound declaration.

   A hint is a starting position, not a verdict — the scout (ADR 0036) may
   revise it once it has read the event and seen the queue.

6. **Preemption remains cooperative, and that is stated rather than implied.**
   A p0 does not stop an in-flight provider call; it parks the thought at its
   next checkpoint. "Interrupt" means *the victim yields at a natural stopping
   point and resumes later*, which is what the operator asked for and what the
   existing `EventPreempted` → context-cancel → checkpoint → `Pause` path
   delivers. Nothing here makes it preemptive in the hard sense.

## Consequences

- **The queue becomes explainable.** "Why did this run before that" has an
  answer: urgency first, arrival second, both stated. The operator can reason
  about a chaotic stream without reading the sort implementation.
- **A navi stays responsive under load.** A p1 arriving while a p4 plan runs
  parks the plan and answers the p1, instead of waiting for the plan to drain.
  That is the behaviour the operator specified and the reason for the change.
- **More parking churn, and it costs durability writes.** Every strictly-higher
  arrival now parks a victim, and `PauseWithCheckpoint` is a synchronous write
  to disk. Where the threshold previously suppressed marginal preemptions
  (p5 vs p7), they now happen. This is the accepted price of strict ordering;
  if it proves expensive in practice, the answer is a floor on *checkpoint*
  frequency rather than a return to the threshold.
- **Parking a dependency's head parks its chain** (ADR 0015 decision 3): a
  paused dependency is not `complete`, so a preemption mid-plan pauses that plan
  until the preemptor finishes. Correct, but worth knowing: a busy p0 stream can
  starve a long plan by repeatedly parking it. The strict rule makes this more
  likely than the threshold did.
- **Restart and resume order changes**, in the direction of correctness. A
  restored queue now drains oldest-first within a band rather than
  most-recently-touched-first, and a resumed thought no longer goes to the back
  of its band.
- **`HighPriorityThreshold` is either removed or re-scoped**, so the code no
  longer advertises a rule the scheduler does not follow.

## Alternatives considered

- **Keep the fixed threshold and map the operator's model onto it.** Rejected:
  the model is explicitly relative ("this should apply to all prios"), and a
  fixed gate cannot express "strictly higher wins" at p7. It also makes the
  named bands a lie, since p2 and p3 would behave identically to p0.
- **Make preemption hard (kill the run, restart it later).** Rejected: it
  discards in-flight work and partial output, and the operator asked for a
  natural stopping point. Cooperative parking already delivers that.
- **Keep stable-sort tie-breaking and fix only the store query.** Rejected:
  three paths must agree, and leaving two of them implicit is how they drift.
  Making the comparator explicit is a one-line change that removes the class.
- **Let a resumed thought go to the back of its band.** Rejected: it was in the
  queue first. Re-appending it behind newer work inverts arrival order exactly
  where the operator wants it respected.
- **Relax the self-scheduled clamp so a navi can prioritise its own work.**
  Rejected here — it is a security-relevant change (a manipulated navi could
  starve external work) and it belongs to ADR 0036, scoped to scouts with a
  stated authority model, rather than opened for all thoughts.
- **A separate scheduler for connector ingress.** Rejected: one queue, one set
  of rules. Two schedulers would need their own consistency story, and ADR 0013
  already established the queue as *the* context mechanism.

## Open questions

1. **A floor on checkpoint churn.** Whether to suppress a preemption whose
   priority delta is marginal, and whether that is even expressible without
   reintroducing the threshold this ADR removes.
2. **Plan starvation.** A repeatedly-parked plan may never drain under a steady
   p0/p1 stream. Whether a plan should be protected (age-based promotion, or a
   cap on how often one plan may be parked) is undecided.
3. **Resume position vs. fresh arrival.** This ADR places a resumed thought at
   its original arrival time. Whether a *long*-parked thought should keep that
   precedence indefinitely is not settled.
4. **Band boundaries as data.** Whether 0/1/2+ should be configurable per navi,
   or stay a convention the connectors and skills agree on.
5. **What the hint is worth.** Whether a connector hint should ever be
   authoritative rather than advisory once ADR 0036's scout exists.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the thought queue is the context mechanism
- [ADR 0015](./0015-thought-dependencies-order-a-plan.md) — dependencies order a plan; a paused dependency blocks its chain
- [ADR 0016](./0016-a-plan-closes-with-a-verification-thought.md) — how a plan closes
- [ADR 0036](./0036-scouts-on-ingress-and-scheduling-authority.md) — scouts, connector hints, and the scheduling authority a scout needs
- [runtime.md](../architecture/runtime.md) — the queue, slots, and preemption
