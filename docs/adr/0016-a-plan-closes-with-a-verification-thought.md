---
status: accepted
date: '2026-10-03'
supersedes: []
tags:
  - adr
  - thought
  - context
---

# 0016 — A plan closes with a verification thought

## Context

[ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) decision 1
says a task too large for one thought becomes **a plan of thoughts**, and
[ADR 0015](./0015-thought-dependencies-order-a-plan.md) gives that plan an
order. Neither says a plan is ever *checked*.

The first end-to-end multi-thought run showed why that matters. The run worked
through its stages, and its own stream showed it **noticing inaccuracies at the
end of a stage and simply moving on**. Nothing in the system asked what the plan
had actually produced, so "done" meant "the last stage returned", which is a
claim about the queue rather than about the work. A chain you cannot check is a
chain you cannot trust, and trusting the chain is the whole point of the thought
system.

`core_thoughts` tells the model to defer into follow-ups. It does not tell it to
close a plan, and even if it did, guidance cannot guarantee anything: the
observed failure was a model that chose to move on.

The alternative is a runtime that synthesizes a check. That has a real cost and
a real risk — an extra thought per plan, and a synthesized prompt that cannot
know what "verified" means for a particular task, so it risks producing a
thought that rereads the transcript and agrees with itself.

What the runtime *does* have is the plan's shape: ADR 0015 made the dependency
graph durable, which means "what this plan is", "where its output landed", and
"did it finish" are all answerable without inventing a plan object.

## Decision

1. **A plan is not finished until it has been checked.** A multi-stage plan
   closes with a **verification thought**: a thought that checks what the plan
   produced rather than extending it.

2. **Guidance owns the check's content.** `core_thoughts` tells the model to
   end a multi-stage plan with a verification thought — `depends_on` set to the
   stage it checks, metadata `role: verification` — and states what a check is:
   run the result, read it back, diff it. Whether "verified" means a build, a
   test run, or reading a file is task-specific, and only the model that did the
   work knows.

3. **The runtime owns the guarantee.** When a plan drains with no check behind
   it, the navi queues one. The rule is mechanical, not heuristic:
   - the plan is the **dependency-connected component** of the completed
     thought — no plan object exists or is needed;
   - every member must be `complete` (a failed or cancelled member means the
     plan did not succeed, and its dependents were already failed);
   - a plan that contains a verification is left alone, so **a check never
     spawns another check**;
   - the synthesized check **depends on the plan's sinks**, so it is released
     only once the whole plan drained — parallel branches included — and is
     claimable immediately;
   - its **id derives from those sinks**, so a second completion event, a
     replayed event, or a restart cannot queue a duplicate.

4. **A check is identifiable.** `role: verification` in the thought's metadata,
   surfaced on the record and in the portal view, so a plan's closing check is
   distinguishable from more work at a glance.

5. **The runtime check is a backstop, and it is optional.** `core_thoughts` is
   the primary path (a task-specific check, written by the model that knows the
   task); the synthesized check covers the case where the model did not. A navi
   that must not spend thoughts on checks turns it off
   (`navi.WithPlanCloseCheck(false)`), and the guidance stands alone.

## Consequences

- **"Done" means something checkable.** A finished plan leaves a record of what
  it produced and what was verified about it, so a human reading the queue can
  tell the difference between a plan that was checked and one that merely
  stopped.
- **The chain is trusted by construction, not by hope.** The guarantee does not
  depend on the model remembering, which is exactly the failure that motivated
  it.
- **A drained plan costs one more thought** — once, never recursively, and only
  for plans of two or more thoughts. A standalone thought is not a plan and is
  not closed.
- **The synthesized check's prompt is bounded and concrete**, naming each stage
  and the result it reported (capped, with a visible elision) rather than
  asking vaguely for verification.
- **The plan's shape now has readers outside the scheduler**: `thought.Plan`,
  `PlanSinks`, and `PlanSucceeded` are public graph helpers, so the "what is a
  plan" answer lives in one place.
- **A failed plan is not closed.** That is deliberate — a check of a failed plan
  would verify nothing — but it leaves a gap: nothing currently proposes
  recovery from a failed plan (see open questions).

## Alternatives considered

- **Guidance only.** Rejected as insufficient on its own: it is the right place
  for the check's *content*, but the observed failure was a model moving past a
  known inaccuracy, and the acceptance is a plan that *leaves a check behind*.
  It is kept as the primary path, with the runtime as the guarantee.
- **Runtime synthesis only.** Rejected: a synthesized prompt does not know how
  to check a particular task, so a check the model did not write is weaker than
  one it did. Synthesis is the backstop, not the mechanism.
- **A plan object or registry.** Rejected: the dependency graph already
  determines membership, and a second source of truth would need to be kept in
  sync with it (and would have to survive restarts). No plan metadata beyond the
  check's own plan id.
- **Synthesizing a check for a failed plan** (or a recovery thought). Deferred
  rather than rejected: the scheduler already fails dependents of a dead
  dependency, so the plan stops cleanly, and what recovery should look like is
  a separate decision.
- **Synthesizing on a timer or at navi shutdown.** Rejected: an idle sweep would
  re-close plans that were already handled (or deliberately not closed), and the
  deterministic id would then be the only thing standing between a restart and a
  spurious thought. Firing on completion keeps the trigger tied to the event
  that changed the plan's state.

## Open questions

1. **What happens to a check that finds a discrepancy.** Today the check reports
   it and stops; whether a failed check should propose a fix, re-plan, or
   escalate to the operator is undecided.
2. **Recovery from a failed plan.** A failed stage fails its dependents
   (ADR 0015) and no check is synthesized; nothing proposes what to do next.
3. **Check depth.** One synthesized check per drained plan regardless of size;
   whether a large plan should be checked per branch is not designed.
4. **Mode and surface for the check.** The synthesized thought runs under the
   navi's default mode like any other; whether a check should be constrained to
   read-only tools is not decided.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — a task too large for one thought becomes a plan of thoughts
- [ADR 0015](./0015-thought-dependencies-order-a-plan.md) — thought dependencies order a plan; this ADR reads that graph
- [runtime.md](../architecture/runtime.md) — closing a plan
