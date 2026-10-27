---
status: proposed
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

# 0036 — A connector event is scouted, and a scout may schedule at ingress urgency

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

**Second: a scout cannot act on its judgement even if it has one.**
`thoughts_create` clamps self-scheduled work to the conservative band
(`EffectiveSelfPriority` → ≥5, `SelfScheduledHighBound`), deliberately: a navi
must not schedule its own work ahead of external work. That clamp is correct and
[ADR 0035](./0035-priority-classes-and-strict-preemption.md) keeps it. But a
scout's entire job is to decide that an event deserves priority 0, 1, or 2 — and
under the clamp its judgement is silently discarded, landing at 5. The navi
would read the notification, reach a conclusion, and be unable to act on it,
**with no error**. That is the failure mode this repository's security
documentation names as the one to design against: a capability that appears
present and is not.

The operator's model, stated plainly: *the scout is the initial pinging — like a
human getting a notification, reading it, and deciding to act now or later.*
Scouts are ordinary thoughts in the same queue with the same interrupt
mechanics, not a special mechanism.

## Decision

1. **A connector event is scouted.** A connector's inbound binding enqueues a
   **scout** — an ordinary thought, in the same queue, subject to the same
   priority and preemption rules as everything else. The scout reads the event
   and the current queue and decides one of:
   - **act now** — schedule the work at the urgency it warrants;
   - **act later** — schedule it lower, or with `defer_until`;
   - **shelve** — record it in memory and schedule nothing, or nothing yet.

   Scouting runs **on connector ingress only**, never per thought. A thought is
   already the product of a decision; re-deciding every thought would spend
   tokens to reach the answer the queue already encodes.

2. **Connectors declare a priority hint, and the scout may revise it.** The
   connector states the urgency it can know (ADR 0035 decision 5) — a Huly
   comment at 1, a new assignment at 2, an urgent issue at 0. The hint is the
   scout thought's own priority and the default for the work it schedules; it is
   **advisory**, because only the scout sees the queue. A hint is how the bulk
   of scheduling leans on the built-in priority system without a scout having to
   reason about every event.

3. **A scout may schedule at ingress urgency, and only a scout may.** A thought
   running in a **scout mode** may schedule follow-ups across the full priority
   range (0–10) instead of the clamped band. Ordinary thoughts keep the ≥5 clamp
   unchanged.

   This is the narrow answer to the authority question, chosen over relaxing the
   clamp generally: the clamp's guarantee (a navi cannot starve external work)
   survives for every thought that did not opt in, and the authority to schedule
   urgent work is a **compiled declaration** visible in `main.go`, not a
   self-asserted flag on a prompt. A scout that names a priority it may not
   schedule is an error, never a silent clamp — the silent-clamp failure is the
   one this decision exists to remove.

   **This needs new mechanism.** `navi.Mode` (ADR 0007) is the right carrier —
   it is already the compiled, per-thought capability profile a thought selects
   — but its `PermissionPolicy` today expresses only the **tool surface**
   (`MaxAccess`, `AllowTools`, `DenyTools`). It has no notion of scheduling
   authority, so this decision adds one: a scheduling-authority field on the
   mode's policy (or on the mode itself), enforced where the clamp is applied
   today (`Thought.EffectiveSelfPriority`, called from `Queue.Enqueue`). The
   enforcement point matters: the clamp lives in the queue, which does not know
   about modes, so the authority must reach `Enqueue` on the thought (as the
   mode it was queued under) rather than being checked at the tool layer, where
   a differently-sourced thought could bypass it.

4. **Scouts are declared, not inferred.** Which thoughts are scouts is a
   compile-time declaration (a mode, per ADR 0007/0026) rather than a metadata
   field a prompt can set, so the authority follows the declaration and cannot be
   requested at run time. The connector that enqueues a scout names the mode on
   the thought it creates, so a connector's ingress is the only thing that
   produces scout authority.

5. **The navi gains queue-navigation tools.** A navi maintaining its own queue
   needs to see and shape it, not just append to it:
   - **`thoughts_search`** — filter and sort the queue (by status, priority,
     age, metadata, text), returning a bounded, ordered view. `thoughts_read`
     stays as the simple snapshot; search is the one a scout uses to answer
     "what am I already doing".
   - **`thoughts_update`** — already covers priority, pause/resume/cancel, and
     context append, so parking exists. It is extended so a navi can shelve work
     **deliberately and legibly**: a stated reason on the park, and status or
     metadata adjustment — so "I decided to come back to this" is recorded
     rather than inferred from a bare `paused`.

   `thoughts_search` is new; `thoughts_update` grows. Both are bounded, and safe
   for a scout to call while deciding.

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

   The tiers stay bounded by policy: a scout may not silently grow the
   always-injected short-term tier, because that is a context cost paid by every
   later thought.

## Consequences

- **The navi can be responsive without being reckless.** Connector hints order
  the bulk of the work; the scout handles the cases where the hint is wrong
  because the queue says so. A p1 comment gets answered promptly while a p2
  assignment waits its turn, and a p0 genuinely interrupts.
- **The clamp's guarantee survives, scoped.** Ordinary thoughts still cannot
  preempt external work. The authority to do so is declared, greppable, and
  narrow — which is a smaller attack surface than relaxing the clamp, and a
  more honest one than a clamp that silently discards a scout's judgement.
- **A scout is inspectable like any other work.** It is a thought with a
  transcript, so "why did the navi decide this was urgent" is answerable from
  the datastore rather than by speculation. This is why scouts are not a
  separate mechanism.
- **One extra thought per inbound event.** A scout costs a thought before any
  real work, and a busy connector produces many cheap thoughts. The hint keeps
  the common case cheap (a scout that agrees with its hint schedules and exits),
  but the cost is real and is the price of the decision being made with the
  queue in view.
- **Long-term memory becomes writable, which is a context-budget question.**
  Completing the memory surface means a navi can grow its own archive; the
  always-injected short-term tier is the one that costs context on every
  thought, so its growth stays constrained while the searched archive is not.
- **A scout's authority is a capability surface**, visible in `main.go` beside
  the tools and connectors — consistent with ADR 0006/0014.

## Alternatives considered

- **Relax the self-scheduled clamp for all thoughts.** Rejected: it removes the
  guarantee that a navi cannot starve external work, for every thought, to solve
  a problem that only scouts have. The narrow capability is smaller and
  reviewable.
- **Let the connector decide priority and skip the scout.** Rejected: the
  connector cannot see the queue, so it cannot answer "is this urgent relative
  to what I am doing" — which is the question. Hints are kept for the bulk case
  precisely because they are cheap, not because they are sufficient.
- **A metadata flag marking a thought as a scout.** Rejected: it makes the
  authority requestable at run time by anything that can write metadata, which
  is the injection surface ADR 0013 decision 4 exists to close. A compiled mode
  keeps the authority in the binary.
- **A separate scout mechanism outside the queue.** Rejected: the operator's
  model is explicit — a scout is a thought like any other, with the same
  interrupt mechanics. A second mechanism would need its own scheduling rules
  and would not be inspectable through the existing transcript contract.
- **Scout every thought.** Rejected: it burns tokens to re-derive a decision the
  queue already encodes, and it would make the navi's own deferrals recursive.
- **Make the connector hint authoritative.** Rejected: a hint that cannot be
  revised cannot express "the queue is on fire, this waits" — which is the whole
  reason the scout exists.
- **Keep long-term memory unreachable and let the navi re-derive.** Rejected:
  re-deriving costs more than storing, and it is what makes a navi lose context
  between thoughts in a chaotic environment — the operator's stated concern.

## Open questions

1. **Scout cost control.** Whether a scout should be skipped when the queue is
   empty and the hint is unambiguous, and whether that optimisation is worth the
   inconsistency.
2. **How a scout reads the queue.** `thoughts_search` must be cheap on a long
   queue; whether the scout needs a narrow summary (what is running, at what
   priority) rather than a search surface is not settled.
3. **Memory tier policy.** Which tiers a scout may write, and whether
   short-term memory needs a size or age bound enforced by the runtime rather
   than by guidance.
4. **Scout scope.** Whether one scout per event is right, or whether a connector
   should batch a poll cycle into one scout with several events.
5. **Shelf semantics.** Whether "shelve" should create a low-priority thought,
   write memory only, or both — and how a shelved item is resurfaced.
6. **Interaction with ADR 0035's plan starvation.** A scout scheduling p0/p1
   work is the main source of the preemptions that can starve a long plan.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the thought queue is the context mechanism; a notification is not a task
- [ADR 0015](./0015-thought-dependencies-order-a-plan.md) — thought dependencies order a plan
- [ADR 0016](./0016-a-plan-closes-with-a-verification-thought.md) — a plan closes with a verification thought
- [ADR 0007](./0007-thought-modes.md) — thought modes; the mechanism decision 3 uses
- [ADR 0026](./0026-mode-session-policy.md) — how a mode's surface is scoped
- [ADR 0035](./0035-priority-classes-and-strict-preemption.md) — priority classes, strict preemption, and the clamp this ADR scopes an exception to
- [memory.md](../architecture/memory.md) — the memory tiers
