---
status: accepted
date: '2026-10-28'
supersedes: []
tags:
  - adr
  - memory
  - context
  - scheduling
  - skills
---

# 0039 — Short-term memory is budgeted by tokens and prunes itself

## Context

[ADR 0037](./0037-memory-tiers-load-timing-and-management.md) decision 6 states
that the always-injected tier is bounded and the searched archive is not,
because short-term memory "costs context on **every** thought, so its growth is
a cost paid by all future work". Its consequences section names the three
behaviours available at the limit — **refuse, evict oldest, or require
consolidation** — and leaves the choice open.

[NAVI-578](./) shipped the memory surface without any of them. `stm_write` can
add notes without limit, and every note in the short-term tier is injected into
every subsequent thought's prompt. So the tier ADR 0037 calls bounded is, in
fact, unbounded, and its growth compounds across all future work.

That gap is recorded rather than unknown, and this ADR closes it — but not with
any of the three behaviours ADR 0037 listed, because all three are the wrong
shape:

- **Eviction** contradicts ADR 0037's own rule that memory grows deliberately
  ("an entry exists because the navi decided it was worth keeping"), and its
  failure is invisible: the navi never learns it lost a note.
- **A hard refusal at the tier level** stops a navi that is *already* over
  budget from writing anything at all, including the small note it needs to
  record what it is doing — the tier becomes unusable at exactly the moment it
  needs to be brought back under control.
- **Consolidation guidance alone** is not enforcement, and ADR 0037 is explicit
  that this is "a policy the runtime enforces rather than guidance the navi is
  trusted to follow".

Three things have also changed since ADR 0037 was written.

**First, the sizes are being estimated, not measured.** The context budget is
enforced by a ~4-characters-per-token heuristic in two places
(`navi/assemble.go` truncation, `internal/agent/budget.go`'s deferral notice).
That is defensible for a soft ceiling and wrong for a gate: a heuristic that
under-counts lets the tier drift over its budget, and one that over-counts
refuses writes that would have fitted. The heuristic is also worst for exactly
the content a knowledge note holds — identifiers, code, non-Latin text. Two
independent estimates of the same thing also means truncation and the deferral
notice can disagree about how full a thought is.

**Second, the self-scheduled clamp is costing more than it buys.** ADR 0035
decision 5 keeps `EffectiveSelfPriority`'s clamp (self-scheduled work ≥ 5) and
ADR 0036 decision 3 keeps it "unchanged, and needs no exception", both framing
it as a security property: a manipulated navi cannot schedule its own work ahead
of external work. On review that threat model is over-weighted. A navi that can
schedule its own work is *already* trusted to act on the operator's behalf, and
"starving external work" is not a meaningful attack when the navi is the thing
doing the external work in the first place. What the clamp actually does is
prevent the legitimate case: a navi that discovers housekeeping it needs to do
**now** — the cleanup pass this ADR introduces is exactly that — cannot ask for
it, and must instead wait behind whatever else is queued.

**Third, plan completion has a natural hook.** `navi/planclose.go` (ADR 0016)
already computes the plan as the dependency-connected component of a completed
thought, waits for every stage, derives a deterministic id so a replayed event
cannot duplicate it, and re-closes a plan that grows. Anything that should
happen "when a plan finishes" can hang off that, mechanically, without the model
having to remember.

## Decision

1. **The bound is enforced at the write, not at the tier.** A write that would
   push a scope **over** its budget is refused; the tier's existing contents are
   never destroyed, and **removals and long-term transitions are never
   gated**. The rule the navi experiences is: *you cannot add to a tier you have
   not brought back under budget.* A navi already over budget (after a hydrate,
   or after the budget was lowered) can still bring itself back under by
   removing and moving; it simply cannot grow further until it does.

   This is the whole enforcement mechanism, and it is deliberately the *only*
   one: the ADR 0037 behaviours are replaced by "refuse the write that exceeds,
   permit everything that reduces".

2. **The budget is measured in tokens, per scope, and configurable.** Each
   short-term scope (`user`, `session`) carries its own budget, so a burst of
   working state cannot consume the room reserved for notes about the human, and
   vice versa. Budgets are declared per navi
   (`navi.WithShortTermMemoryBudget(scope, tokens)`) with a default.

3. **Tokens are counted by a real tokenizer, behind a narrow interface.** The
   heuristic is replaced by a counted value in all three places that estimate
   context — memory budgets, assembly truncation, and the deferral notice — so
   the three agree. The counter is an interface rather than a concrete
   dependency, so a navi can supply a provider-specific one and the heuristic
   remains the fallback when none is available.

4. **Over-budget schedules a cleanup thought; it does not block the navi.** When
   a scope is found over budget — on a refused write, or on hydrate of a
   persisted set that exceeds it — the runtime queues a **short-term memory
   cleanup** thought. The thought is self-scheduled, carries a stated purpose
   (reduce the tier below budget), and is expected to prune stale notes and move
   what matters into long-term memory. The runtime enforces that *something*
   happens; the navi decides *what to keep*, because only the navi knows which
   notes still matter.

5. **A cleanup thought is p1, and cannot schedule another cleanup.** p1 is the
   steering band (ADR 0035 decision 3): the cleanup is housekeeping, and
   *cancelling* a running thought to perform bookkeeping is backwards. The
   no-recursion rule is what makes scheduling-as-enforcement safe: a cleanup
   thought that leaves the tier still over budget is a logged warning, not
   another thought, so the mechanism cannot loop.

6. **The self-scheduled priority clamp is removed.** A self-scheduled thought
   may declare any priority; when it declares none, it **defaults to p1** rather
   than to the conservative band. The rule this expresses is unchanged in
   spirit — *work the navi discovers should not outrank work it was given* — but
   it is now a **default rather than a floor**, because the floor's cost (a navi
   unable to ask for housekeeping it has identified) is real and its benefit
   (a threat model that does not hold) is not.

   This supersedes ADR 0035 decision 5 and ADR 0036 decision 3 as they apply to
   the clamp. It does **not** change the ingress-hint reasoning those decisions
   rest on: a connector's thought is still not self-scheduled, so its hint still
   survives intact, and that part of ADR 0036 decision 3 stands.

7. **A finished plan schedules a second cleanup, from the plan-close
   mechanism.** When `planclose` determines a plan has genuinely drained, it
   queues a cleanup thought alongside the closing check, depending on the plan's
   sinks. Its stated purpose is different from the over-budget cleanup: *move
   what this chain produced into long-term memory and remove the working state
   it no longer needs.* This is the mechanical half of ADR 0037's
   write-as-you-go guidance — the notes a chain wrote as it went are exactly the
   notes that should not outlive it.

   **It hangs off plan-close rather than the verification thought**, and that is
   deliberate: a verification that fails schedules more work (ADR 0016), so a
   cleanup depending on it would run *before* the plan actually finished.
   Plan-close already knows when the plan truly drained, including a plan that
   grew, so it is the only correct trigger.

8. **The two triggers share a mechanism and differ in their brief.** Both queue
   a self-scheduled cleanup thought at p1 with no-recursion, and both are
   recorded as such in metadata so the transcript says why it exists. The
   over-budget cleanup is told to focus on **reducing the tier** (clear what is
   known stale, prefer removal and long-term transition over rewriting); the
   plan-completion cleanup is told to focus on **this chain's residue** (move
   what it produced into long-term memory, remove the working state that
   produced it). One mechanism, two briefs, so the code path is single and the
   guidance is specific.

9. **Long-term memory remains unbounded.** It is searched rather than injected,
   so its growth is not a cost paid by every thought, and `ltm_prune` (ADR 0037
   decision 7) is its retention answer. The asymmetry is stated in the core
   memory skill so a navi does not assume symmetry between the tiers.

## Consequences

- **The tier can no longer grow without limit, and nothing is ever destroyed to
  achieve that.** The failure mode is a refused write with an actionable error,
  not a silently dropped note — which is the property eviction could not offer.
- **A navi over budget is not wedged.** Because removals and long-term moves are
  ungated, the tier is always recoverable by the navi that owns it, and the
  scheduled cleanup is the thing that tells it to do so.
- **The bound is not enforced *at the moment of writing* if the navi ignores the
  cleanup thought.** The runtime guarantees that a cleanup is scheduled, not
  that it succeeds: a navi could in principle stay over budget indefinitely and
  simply be unable to add. That is accepted — the alternative is a runtime that
  decides what to forget, which is the thing ADR 0037 rejects.
- **Token counting becomes a real dependency.** It is the first non-stdlib
  addition in this area, and it must be optional enough that a navi without a
  tokenizer still runs. The interface keeps that possible; the heuristic stays
  as the fallback.
- **Self-scheduled work can now preempt external work.** That is the point of
  removing the clamp, and it is the cost: a navi that schedules badly now
  degrades its own responsiveness rather than being prevented from doing so. The
  p1 default means it takes a deliberate declaration to reach the cancel band.
- **Plans gain a second closing thought.** Every drained plan now queues a
  cleanup as well as a check, so a plan is two extra thoughts rather than one.
  The cleanup is cheap and p1; the cost is accepted for a tier that does not
  accumulate a chain's working state forever.
- **The cleanup thought's own writes are gated like any other.** It can remove
  and move freely, and its additions count against the budget — so a cleanup
  that tries to *rewrite* notes rather than prune them can be refused. That is
  correct (rewriting is not reducing) but it is a sharp edge the brief must
  state.

## Alternatives considered

- **Evict the oldest note to make room.** Rejected: it destroys something the
  navi deliberately kept, contradicts ADR 0037's "memory grows deliberately",
  and fails invisibly.
- **Refuse every write while over budget, with no cleanup thought.** Rejected:
  the navi is told it is stuck but not what to do, and the tier stays over budget
  until something else prompts a review. The cleanup thought is what converts a
  refusal into an action.
- **Enforce by silently truncating the injected block at render time.** Rejected:
  the notes would still exist and still count, so the budget would bound the
  *prompt* rather than the tier, and a navi would be unable to tell which of its
  notes the model can actually see.
- **A hard cap at a multiple of the budget (refuse everything above 2×).**
  Rejected for now: it adds a second number and a second failure mode to guard
  against a navi ignoring its own cleanup, and the ADR's stance is that the navi
  decides what to keep. Recorded as an open question rather than dismissed.
- **Keep the self-scheduled clamp and give the cleanup an exception.** Rejected:
  an exception is a hole in exactly the rule ADR 0036 decision 3 closed, and
  once the threat model is not accepted, removing the clamp is the honest change
  rather than carving an exception into it.
- **Schedule the plan-completion cleanup from the verification thought.**
  Rejected: verification can fail and schedule more work, so the cleanup would
  run against a plan that had not finished. Plan-close is the only trigger that
  knows the plan truly drained.
- **Make plan-completion cleanup runtime-only and skip the skill guidance.**
  Rejected for now, and this is the closest call: it is more reliable (it cannot
  be forgotten) but ADR 0037 decision 9 deliberately keeps curation a tool/skill
  obligation. See open question 1.
- **Give long-term memory a bound too, for symmetry.** Rejected: it is searched,
  not injected, so the asymmetry is the point rather than an oversight.

## Open questions

1. **Skill guidance versus a runtime pass, revisited.** ADR 0037 decision 9
   rejects a runtime post-thought memory pass because a pass must decide what to
   keep without the reasoning the thought just did. The plan-completion cleanup
   is a narrow exception in spirit — the runtime decides *that* curation happens,
   the navi decides *what* — but the boundary is worth watching. If cleanup
   thoughts are routinely ignored, the answer may be a runtime pass for the
   mechanical part (removing notes scoped to a finished plan) with the judgement
   left to the navi.
2. **The default budgets.** What the per-scope defaults should be, and whether
   they should scale with `WithThoughtBudget` rather than being absolute. A navi
   with a 4k budget and one with a 200k budget do not want the same number.
3. **Whether a hard backstop is needed.** Whether a navi that ignores its
   cleanup indefinitely should eventually be refused outright above some
   multiple of budget, or whether the write lockout is sufficient.
4. **Whether a provider-specific counter should be declarable per model**, the
   way routing is (ADR 0034). The default implementation is settled —
   `github.com/pkoukk/tiktoken-go` (pure Go, so it survives `CGO_ENABLED=0`, and
   its cl100k vocabulary embeds offline, which a navi on an isolated host
   needs). What is not settled is whether a navi running a model with a
   different tokenizer should be able to declare its own counter, since a
   mismatch between the counter and the model's real tokenizer reintroduces
   exactly the drift decision 3 removes — just with a better estimator.
5. **Cleanup thought ordering against the closing check.** Both hang off a
   drained plan at the same priority; whether they should be ordered (check
   first, then clean) or left to the queue's `(priority, created_at)` rule.
6. **Whether the cleanup brief belongs in the core memory skill or a dedicated
   one.** It is a procedure rather than a description of a surface, which is a
   different kind of skill content from what ships today.

## Related

- [ADR 0037](./0037-memory-tiers-load-timing-and-management.md) — memory tiers, load timing, and management; decision 6 is the bound this implements, and decision 9 the no-runtime-pass stance open question 1 revisits
- [ADR 0035](./0035-priority-classes-and-strict-preemption.md) — priority classes and strict preemption; **decision 5's self-scheduled clamp is superseded by decision 6 here**. The rest of 0035 stands unchanged, which is why that record keeps `status: accepted` rather than becoming `superseded`: this is a *partial* supersession, and marking the whole record replaced would misstate what remains in force (the bands, strictly-higher-wins, the ordering, and automatic resumption are all untouched).
- [ADR 0036](./0036-a-connector-event-triages-itself.md) — the ingress thought and the clamp; **decision 3 is superseded only as it applies to the clamp**. Its ingress-hint reasoning stands: a connector's thought is still not self-scheduled, so its hint still survives intact. For the same reason as 0035, that record keeps `status: accepted`.
- [ADR 0016](./0016-a-plan-closes-with-a-verification-thought.md) — the plan-closing check this ADR's second cleanup trigger hangs beside
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — retention stance; decision 11's "pruned by an explicit act" is what `ltm_prune` follows
- [memory.md](../architecture/memory.md) — the two-tier model
