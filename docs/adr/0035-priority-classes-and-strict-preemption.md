---
status: accepted
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
     the secondary key is *last activity*, not arrival. **Activity is the wrong
     signal entirely.** A restart is not a continuation of what the navi was
     last touching; it is a fresh start, and the queue should come back **from
     the top** — most urgent first, oldest first within a band. A thought that
     happened to be modified recently (a status change, a retry, a metadata
     write) must not be promoted over one that was queued earlier and never
     touched;
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

   | Priority | Meaning | How the running thought yields |
   |---|---|---|
   | **0** | urgent | **cancelled and parked** — Ctrl-C, then park where it stood |
   | **1** | "respond to me" | **steered to park** — told to put the work down, at a point it chooses |
   | **2+** | planned work | **steered to park**, same as 1, for anything strictly more urgent |

   The bands are documentation and convention, not a hard-coded table: the
   scheduler implements the rules in decisions 2 and 3, and the bands are how
   the connectors and the triaging thought use them.

2. **Preemption is strictly-higher-wins; the fixed threshold is removed.**
   `HighPriorityThreshold` no longer gates preemption. A pending thought may
   preempt a running thought whenever its priority is **strictly lower**
   (more urgent) than the victim's — the existing "strictly lower victim"
   comparison in `preemptCandidate` becomes the whole rule, and the
   `EffectivePriority() > HighPriorityThreshold` early return goes away.

3. **Two ways to yield, and the difference is whether the victim gets to think.**
   This is the operator's distinction, and it is the sharper half of the rule:
   *a p0 is Ctrl-C and then park; anything else is steer to park.*

   - **A p0 cancels.** The victim is stopped **immediately** — `EventPreempted`
     → context cancel → the runner snapshots its checkpoint → `Pause`. It does
     not reason about it and does not get to choose. This is for work that
     cannot wait: an outage. The cost is an abrupt checkpoint that may land
     mid-step, leaving the resumed thought with less context than a deliberate
     wrap-up would have produced. That cost is accepted, because nothing the
     victim is doing matters as much as stopping.
   - **Any other higher priority steers.** The victim is **told** to put its
     work down (`RouteMessage` → `Agent.Steer`, `internal/agent/agent.go:225`),
     and it does so **at a point it chooses** — the turn boundary. It can finish
     the step it is in, write what the next thought needs into memory, and park
     with a stated reason. The result is a **better handoff** than a forced
     cancellation produces, which is the whole reason to prefer steering when
     there is time for it.

   Both mechanisms already exist; what changes is *when each is used*. Today
   `EventPreempted` is the only preemption path, so every preemption is a forced
   cancellation.

   **A steered yield is bounded by one turn, not instantaneous.** `Steer`
   injects at the turn boundary, so a p1 arrives when the current provider turn
   ends. That is the correct granularity — it is what makes the stopping point
   *natural* — but it means p1 latency is bounded by a turn rather than by
   nothing, and no caller should expect otherwise.

   **A steering instruction must be actionable.** The victim has to understand
   that it is being asked to park, and why, in terms it can act on: put the work
   down, record what the next thought needs, and park. That is a **guidance**
   obligation carried by the core thoughts skill, not something each connector
   invents — the same instruction has to work for every running thought.

   The threshold constant is retained only if it still marks something real;
   otherwise it is deleted rather than left as a misleading vestige.

4. **Time breaks ties, explicitly, in every path.** A thought's position among
   equals is `(priority, created_at)` — arrival order, not last-activity order
   and not insertion order. Concretely:
   - the in-memory comparator becomes `(priority, created_at)` rather than
     relying on `SliceStable`;
   - `LoadThoughts` orders by `ORDER BY priority, created_at` — so a restart
     resumes from the top of the queue rather than from whatever was last
     touched;
   - a **resumed** thought keeps its original arrival position within its
     priority band, because it was in the queue before the work that arrived
     while it was parked.

   This is the one place the decision is not a pure refactor: it changes
   observable order after a restart and after a resume. That is the point —
   the order becomes a stated rule instead of an artifact.

5. **The self-scheduled clamp stays.** `EffectiveSelfPriority` continues to
   clamp origin-tagged work to ≥5, so a navi that schedules its own follow-ups
   cannot preempt external work. A navi assigned a low-priority issue can decide
   it is genuinely low priority and let it wait while it finishes what it is
   doing — which is exactly what the clamp makes safe.

   The one thing the clamp cannot express is a **scout** scheduling work on a
   connector's behalf at connector-level urgency. That is ADR 0036's subject,
   and it is deliberately a separate decision with its own authority model
   rather than a hole punched in this one.

6. **Connectors declare a priority hint on the thoughts they spawn.** A
   connector is closer to the event than the queue is, so it states the urgency
   it knows: a Huly issue comment at 1 (the user is waiting for feedback), a new
   assignment at 2 (it has a place to fall that does not interrupt the current
   interaction), an urgent issue at 0. `huly.Subscription.Priority` already
   exists for this and is currently unused by `navi-bastilli`; the same field
   is expected of every connector's inbound declaration.

   A hint is a starting position, not a verdict — the scout (ADR 0036) may
   revise it once it has read the event and seen the queue.

7. **Preemption remains cooperative, and that is stated rather than implied.**
   Even a p0 does not stop an in-flight provider call; it parks the thought at
   its next checkpoint. "Interrupt" means *the victim yields and resumes later*,
   which is what the existing `EventPreempted` → context-cancel → checkpoint →
   `Pause` path delivers for p0 and what steering delivers for everything else.
   Nothing here makes it preemptive in the hard sense — there is no mechanism to
   interrupt a provider mid-call, and a p0's Ctrl-C lands at the same checkpoint
   boundary as any other cancellation, just without the victim's cooperation.

8. **A preempted thought resumes automatically when its preemptor finishes.**
   This is the half that makes an interrupt tolerable, and it does **not exist
   today**: every `Resume` call site is operator-driven (a portal action, a
   `thoughts_update`, an explicit API call), `Dequeue` walks only `q.pending`,
   and `Complete` clears the victim's `PreemptRequested` flag but resumes
   nothing. So a preempted thought is parked **permanently** until a human
   resumes it, and "I was interrupted, I will come back to that" is not a
   behaviour the queue has.

   The intended cycle is: a higher-priority thought arrives → the running
   thought parks with a checkpoint → the preemptor runs → the preemptor
   **finishes** → the victim is resumed from its checkpoint. A preemption is
   therefore only acceptable because the preemptor is expected to be *short*:
   the interruption costs the victim a context switch, not its progress.

   **Resumption is automatic only for a preemption.** A thought parked by an
   operator, by a `thoughts_update`, or by a plan deciding to shelve work is
   paused **deliberately** and stays paused until something resumes it — the
   queue must not resurrect work a human or the navi chose to stop. The queue
   therefore has to distinguish "paused because it was preempted" from "paused
   because someone decided to pause it", which today it does not: `Pause`
   (`thought/queue.go:549`) and `PauseWithCheckpoint` are one path, and a
   preemption is recorded only as a transient `PreemptRequested` flag that
   `Complete` clears.

   A victim whose preemptor *fails* or is *cancelled* is also resumed: the
   reason for the interruption is gone, and leaving the victim parked would make
   a failed high-priority thought silently destroy the work it interrupted.

   Interaction with ADR 0015: resuming the head of a chain releases the chain,
   so the plan continues where it left off.

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
- **A steered yield produces a better handoff than a cancelled one.** The
  victim writes what the next thought needs and parks with a stated reason,
  rather than being snapshotted wherever it happened to be. This is a real
  quality difference, not just a timing one: a p1 leaves the resumed thought in
  a better position than a p0 does, because the victim cooperated.
- **A p0 buys immediacy at the cost of context.** It is the only path that does
  not let the victim reason, so it may park mid-step with a thinner checkpoint.
  That is the intended trade — for an outage, stopping matters more than the
  quality of the resume — but it means p0 is not simply "a p1 that is faster",
  and a caller should not reach for 0 when 1 would do.
- **Steering needs guidance, which is a new obligation on the core thoughts
  skill.** The instruction must be actionable in terms any running thought can
  act on (put the work down, record what the next needs, park). Without it, a
  steered thought receives a message it does not know how to obey — which is
  worse than a cancellation, because it keeps running and ignores the message.
- **An interrupt costs a context switch, not progress** — but only once
  automatic resumption exists (decision 8). Until it does, every preemption is a
  **permanent** loss of the victim's forward progress, which makes the strict
  rule actively harmful rather than merely aggressive: a p5 arriving during a p7
  would park the p7 forever. Decision 7 is therefore not a nicety; it is the
  thing that makes decision 2 safe, and the two must ship together.
- **Parking a dependency's head parks its chain** (ADR 0015 decision 3): a
  paused dependency is not `complete`, so a preemption mid-plan pauses that plan
  until the preemptor finishes. Correct, but worth knowing: a busy p0 stream can
  starve a long plan by repeatedly parking it. The strict rule makes this more
  likely than the threshold did.
- **The preemptor is expected to be short.** A preemption is only tolerable
  because the interrupting thought finishes quickly and hands control back. A
  p0 that itself runs for a long time holds the victim parked for that whole
  span — which is why a scout (ADR 0036) doing cheap triage rather than the work
  is what makes the model pay off.
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
- **Leave resumption to the operator.** Rejected: it makes every preemption a
  permanent loss, which contradicts the whole point of parking. "I will come
  back to that" is only true if something brings it back.
- **Auto-resume anything paused.** Rejected: a pause is also how an operator
  stops work and how a navi shelves something deliberately. Resurrecting that
  would override a human decision, so the queue must distinguish a preemption
  park from a deliberate one.
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
- [ADR 0036](./0036-a-connector-event-triages-itself.md) — scouts, connector hints, and the scheduling authority a scout needs
- [runtime.md](../architecture/runtime.md) — the queue, slots, and preemption
