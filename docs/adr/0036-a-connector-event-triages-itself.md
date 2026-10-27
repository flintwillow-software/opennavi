---
status: accepted
date: '2026-10-27'
supersedes: []
tags:
  - adr
  - thought
  - scheduling
  - connector
  - skills
  - memory
---

# 0036 — A connector event triages itself, and the self-scheduled clamp is enough

## Context

[ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) established
that a connector-scheduled thought is a **notification**, not a task: the navi
reads it, works out what is being asked, and plans — deferring the work into
follow-up thoughts rather than attempting it in one context window.
[ADR 0015](./0015-thought-dependencies-order-a-plan.md) gave that plan an order,
and [ADR 0016](./0016-a-plan-closes-with-a-verification-thought.md) gave it a
close. [ADR 0035](./0035-priority-classes-and-strict-preemption.md) has just
made priority express urgency and made any strictly-higher arrival preempt.

Two things are still missing, and they are the same missing thing seen from two
sides.

**First: the navi has no way to answer "is this urgent *right now*".** Urgency is
a property of the arrival *relative to the queue at that moment*. A mention
arriving while the navi is idle is trivial; the same mention arriving mid-plan
on a hot issue may matter. A connector cannot know this — it sees one event and
has no view of what the navi is doing. Today `navi-bastilli`'s Huly
subscription sets **no priority at all**, so every event lands at the default
(3) and the navi has no signal to work from.

**Second: the navi must be able to act on the judgement it reaches.**
`thoughts_create` clamps self-scheduled work to the conservative band
(`EffectiveSelfPriority` → ≥5, `SelfScheduledHighBound`), deliberately: a navi
must not schedule its own work ahead of external work. That clamp is correct and
[ADR 0035](./0035-priority-classes-and-strict-preemption.md) keeps it.

The operator's model, stated plainly: a connector's thought is the initial
pinging — like a human getting a notification, reading it, and deciding to act
on it now or later. **The judgement belongs to the running thought, and there is
no separate scouting step.**

### Why the judgement cannot happen at ingress

A connector cannot judge urgency, because urgency is a property of the arrival
*relative to what the navi is already doing*. A mention arriving while the navi
is idle is trivial; the same mention arriving mid-plan on a hot issue may matter.
A connector sees one event and has no view of the queue.

But a separate **scout mode** is also the wrong answer, and the operator rejected
it: it would add a mode, a new authority, and an extra thought per event to reach
a conclusion the ingress thought can reach itself. What actually happens is
simpler:

1. **thought1** (p2) is running.
2. **thought2** (p1) arrives and is strictly more urgent, so thought1 is
   **steered to park** — not cancelled (ADR 0035 decision 3: a p0 cancels, a p1
   steers). thought1 finishes the step it is in, writes what the next thought
   needs, and parks with a stated reason. It chooses the stopping point, which
   is what makes the handoff good.
3. **thought2 runs.** It reads the event, judges it, and — if the work is not
   actually urgent — does the *cheap* part: answer the source (reply in the
   thread, acknowledge the issue), schedule a follow-up for the real work, and
   **finish quickly**.
4. **thought1 resumes** from its checkpoint and continues.

The navi is interrupted, but the interruption is **cheap**, and the preempted
work resumes because its preemptor was short-lived. No mode, no extra authority,
no extra thought per event — the behaviour falls out of strict preemption
(ADR 0035 decision 2) plus automatic resumption (ADR 0035 decision 8) plus a
triaging thought that knows to get out of the way.

A thought may of course judge the opposite — *"this is a real outage"* — and
simply do the work. Same mechanism, opposite conclusion.

## Decision

1. **A connector-scheduled thought triages itself.** There is no scout mode and
   no separate scouting thought. The thought the connector queues is the one that
   reads the event, judges it against the queue, and decides:
   - **do it now** — the work is genuinely urgent, or small enough that
     finishing it beats deferring it;
   - **do the cheap part and defer the rest** — answer the source, then schedule
     the real work (lower, or with `defer_until`);
   - **shelve** — record it in memory and schedule nothing yet.

   Judging happens **once, per inbound thought**, never per thought in general. A
   thought the navi itself scheduled is already the product of a decision;
   re-deciding it would spend tokens to reach the answer the queue encodes.

2. **Connectors declare a priority hint, and the thought may revise it.** The
   connector states the urgency it can know (ADR 0035 decision 5) — a Huly
   comment at 1, a new assignment at 2, an urgent issue at 0. The hint decides
   **where the thought lands and whether it interrupts**; it is a starting
   position, not a verdict, and the thought may revise it once it has read the
   event and seen the queue. A hint is how the bulk of scheduling leans on the
   built-in priority system without every thought having to reason about it.

3. **The self-scheduled clamp is unchanged, and needs no exception.** This is
   the consequence of dropping scout mode, and it is a simplification: nothing
   needs authority to schedule at ingress urgency, because a connector's ingress
   thought is **not self-scheduled**. It carries external origin, so the clamp
   does not apply to it and the hint's priority survives intact.

   What a thought schedules for *itself* stays clamped at ≥5 — which is correct,
   because the follow-up it schedules for deferred work is genuinely lower
   urgency than the work it is doing. The clamp now expresses the intended rule
   with no exception: **work the navi discovers is not more urgent than work it
   was given.**

   The operator's example is the test: a p2 cleanup thought interrupted by a p1
   judges the p1 to be p3 work, replies to the source, and schedules a p3
   follow-up. Under the clamp that follow-up lands at 5, which is the correct
   relative position — it is *less* urgent than the p2 the navi is going back to.

4. **A thought's judgement is recorded, not implicit.** The decision a triaging
   thought reaches — do it, defer it, shelve it, and why — is written where the
   next reader can find it: a memory entry for the reasoning, a scheduled
   thought with a stated prompt, or a park-with-reason (decision 5). "Why did the
   navi decide this was not urgent" must be answerable from the datastore rather
   than by speculation.

5. **The navi gains queue-navigation tools.** A navi maintaining its own queue
   needs to see and shape it, not just append to it:
   - **`thoughts_search`** — filter and sort the queue (by status, priority,
     age, metadata, text), returning a bounded, ordered view. `thoughts_read`
     stays as the simple snapshot; search is the one a triaging thought uses to answer
     "what am I already doing".
   - **`thoughts_update`** — already covers priority, pause/resume/cancel, and
     context append, so parking exists. It is extended so a navi can shelve work
     **deliberately and legibly**: a stated reason on the park, and status or
     metadata adjustment — so "I decided to come back to this" is recorded
     rather than inferred from a bare `paused`.

   `thoughts_search` is new; `thoughts_update` grows. Both are bounded, and safe
   for a triaging thought to call while deciding.

6. **A navi manages its memory tiers, and the tiers are reachable.** Short-term
   memory carries context between thoughts (ADR 0013), and long-term memory is
   the searched archive. Today `memory_write` handles add/replace/remove for the
   `user` and `session` scopes — but `ScopeNavi` and `ScopeMemories` exist in
   the datastore and are reachable through **no tool at all**, so a navi cannot
   write long-term memory or its own notes.

   The memory surface is completed so a navi can **add, manage, and remove**
   entries in each tier it owns, and the core memory skill states which tier is
   for what — so a navi in a chaotic environment knows it can park a conclusion
   in long-term memory, or record a durable fact about the human, rather than
   losing it when the thought ends.

   The tiers stay bounded by policy: a triaging thought may not silently grow the
   always-injected short-term tier, because that is a context cost paid by every
   later thought.

## Consequences

- **The navi can be responsive without being reckless.** Connector hints order
  the bulk of the work; the thought handles the cases where the hint is wrong
  because the queue says so. A p1 comment gets answered promptly while a p2
  assignment waits its turn, and a p0 genuinely interrupts.
- **The clamp needs no exception, which is a smaller change than the first
  draft of this ADR proposed.** A connector's ingress thought is not
  self-scheduled, so its hint survives the clamp untouched; work the navi
  schedules for itself stays clamped. The rule the clamp expresses is exactly
  the intended one — *work the navi discovers is not more urgent than work it
  was given* — with no new authority, no new mode, and no new capability surface
  to review.
- **The interrupt is cheap because the triaging thought is short.** A preemption
  costs the victim a context switch, not its progress (ADR 0035 decision 8), and
  the preemptor is expected to finish quickly — read, decide, maybe answer the
  source, schedule, exit. The model pays off only if triaging thoughts stay
  short; a triaging thought that starts doing the work holds the victim parked
  for its whole duration.
- **A thought's judgement is inspectable like any other work.** It is a thought
  with a transcript, so "why did the navi decide this was not urgent" is
  answerable from the datastore rather than by speculation. This is why the
  judgement lives in an ordinary thought rather than in a separate mechanism.
- **Long-term memory becomes writable, which is a context-budget question.**
  Completing the memory surface means a navi can grow its own archive; the
  always-injected short-term tier is the one that costs context on every
  thought, so its growth stays constrained while the searched archive is not.

## Alternatives considered

- **Relax the self-scheduled clamp for all thoughts.** Rejected: it removes the
  guarantee that a navi cannot starve external work, for every thought, to solve
  a problem that turns out not to need solving. Once the judgement lives in the
  ingress thought, the clamp needs no exception at all (decision 3).
- **A scout mode carrying scheduling authority.** Rejected by the operator, and
  this ADR's first draft proposed it: a mode is the right carrier for a *tool*
  surface, but a scheduling exception is a bigger change than the problem
  warrants. The ingress thought already carries external origin, so it is not
  clamped — the authority was never needed.
- **Let the connector decide priority and skip the judgement entirely.** Rejected:
  the connector cannot see the queue, so it cannot answer "is this urgent
  relative to what I am doing" — which is the question. Hints are kept for the
  bulk case precisely because they are cheap, not because they are sufficient.
- **A metadata flag marking a thought as triaging.** Rejected: it makes the
  behaviour requestable at run time by anything that can write metadata, which is
  the injection surface ADR 0013 decision 4 exists to close.
- **A separate triage mechanism outside the queue.** Rejected: the operator's
  model is explicit — the triaging thought is a thought like any other, with the
  same interrupt mechanics. A second mechanism would need its own scheduling
  rules and would not be inspectable through the existing transcript contract.
- **Make the connector hint authoritative.** Rejected: a hint that cannot be
  revised cannot express "the queue is on fire, this waits" — which is the whole
  reason the thought judges for itself.
- **Keep long-term memory unreachable and let the navi re-derive.** Rejected:
  re-deriving costs more than storing, and it is what makes a navi lose context
  between thoughts in a chaotic environment — the operator's stated concern.

## Open questions

1. **Keeping the triaging thought short.** The model depends on a preempting
   thought finishing quickly. Whether that needs enforcement (a budget, or a
   guidance rule) or is reliably a property of the task is not settled.
2. **How a triaging thought reads the queue.** `thoughts_search` must be cheap on
   a long queue; whether it needs a narrow summary (what is running, at what
   priority) rather than a general search surface is not settled.
3. **Memory tier policy.** Which tiers a triaging thought may write, and whether
   short-term memory needs a size or age bound enforced by the runtime rather
   than by guidance.
4. **Batch vs. per-event.** Whether one triaging thought per event is right, or
   whether a connector should batch a poll cycle into one thought with several
   events — which would make the interrupt cheaper still.
5. **Shelf semantics.** Whether "shelve" should create a low-priority thought,
   write memory only, or both — and how a shelved item is resurfaced.
6. **Interaction with ADR 0035's plan starvation.** A triaging thought that
   schedules p0/p1 work is the main source of the preemptions that can starve a
   long plan.
7. **What happens when the hint and the judgement disagree persistently.** If a
   connector's hint is routinely wrong, whether the hint should be tunable per
   deployment rather than fixed in the connector.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the thought queue is the context mechanism; a notification is not a task
- [ADR 0015](./0015-thought-dependencies-order-a-plan.md) — thought dependencies order a plan
- [ADR 0016](./0016-a-plan-closes-with-a-verification-thought.md) — a plan closes with a verification thought
- [ADR 0007](./0007-thought-modes.md) — thought modes; considered and rejected for the triage judgement
- [ADR 0035](./0035-priority-classes-and-strict-preemption.md) — priority classes, strict preemption, and the clamp this ADR scopes an exception to
- [memory.md](../architecture/memory.md) — the memory tiers
