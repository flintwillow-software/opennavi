---
status: accepted
date: '2026-10-10'
supersedes: []
amends: ./0013-the-thought-queue-is-the-context-mechanism.md
tags:
  - adr
  - security
  - authorization
  - portal
  - object
---

# 0022 — Delegated authorization

## Context

A thought runs as the navi. Its tools are the navi's tools, gated by the
compiled ceiling and the thought's mode; nothing in the path records who caused
the thought. So a principal who may enqueue a thought can cause the navi to call
a tool that principal could not call directly — the confused-deputy problem. The
object surface has the same shape from the other side: ADR 0017 decision 8 makes
a portal's write path and a tool's write path the same mutation, so authorizing
one and not the other leaves a bypass.

ADR 0021 establishes principals, compiled policy, and runtime identity
resolution. This record decides where that policy is enforced and how the acting
principal is carried to each enforcement point. It resolves ADR 0019 open
question 1 and refines its decision 6.

## Decision

1. **Four enforcement points, one policy engine, one acting principal.**
   Attachment authorizes a connection; the object surface authorizes a read; the
   object surface authorizes an action; the tool path authorizes a call. Each
   asks the same engine about the same principal. `enqueue` is the fifth verb,
   checked at the portal boundary.

2. **A thought carries its originating principal and its lineage.** The
   principal is recorded when the thought is enqueued; the lineage records the
   thought-to-thought chain when the navi schedules follow-ups.

3. **Follow-up thoughts inherit the originating principal by default.** A
   deferred thought, a plan stage, and a closing check run on whose behalf the
   chain began. System-originated work acts as `navi:<name>` (ADR 0021
   decision 10).

4. **The mode chosen at enqueue is a session policy.** It narrows the
   principal's policy for that thought. The selectable set is derived: a
   principal may request a mode whose `PermissionPolicy` does not exceed the
   principal's own, which `PermissionPolicy.Exceeds` already computes. A mode
   can never widen.

5. **The tool path and the object path share the principal.** A mutation reached
   through a tool and the same mutation reached through an object action
   authorize identically; otherwise the cheaper path is the bypass.

6. **Evaluation follows the IAM rules.** Default deny; an explicit deny wins; an
   allow must be present from the intersection of the compiled ceiling, the
   principal's role policy, and the requested mode; every condition must pass.

7. **Read-scoping and field sensitivity compose.** A sensitive field is one a
   principal may see only with a grant, not one the surface removes for
   everyone. Projection becomes principal-aware, and the descriptor's
   sensitivity remains the declaration that a grant is required.

8. **The navi resolves trust for portals.** A portal asks the navi to
   authenticate a connection and to authorize or scope what that principal may
   see and do; it receives a principal and a scope, never the policy vocabulary.
   Portals do not evaluate policy.

9. **Authorization fails closed on a missing principal.** An action with no
   resolved principal is refused. This is why the service principal of ADR 0021
   decision 10 is load-bearing: without it, fail-closed would disable the
   runtime's own machinery.

## Consequences

- A principal can no longer launder an action through the agent: the tool call
  is authorized as the originator, not as the navi.
- `thought.Thought` gains an originator and lineage — a data-model change, and
  one that makes "on whose behalf did this happen" answerable from the record.
- `portal.Authorizer`'s single `(action, identity)` question is superseded by a
  principal-and-capability question; the old interface either adapts or is
  retired.
- The object surface's projection becomes principal-aware, so read-scoping and
  sensitivity stop being two overlapping mechanisms.
- A long-running thought keeps the policy it started with unless the check is
  per-call; this record chooses per-call, so revocation takes effect on the next
  call.
- Portals shrink: they present a credential and render what they are given, and
  no portal re-implements an authorization decision.

## Alternatives considered

- **Filter the tool surface once at thought creation.** Cheaper, but a running
  thought outlives a revocation, and it does not close the confused-deputy path
  for a surface chosen before the caller was known. Rejected in favor of a
  per-call gate.
- **Authorize only at the portal boundary.** Leaves the tool path unguarded,
  which is the bypass this record exists to close. Rejected.
- **Principal-less thoughts.** Every thought has a principal, or the runtime's
  own machinery stops under fail-closed. Rejected.
- **Separate authorization for tools and objects.** Two engines, two
  vocabularies, and a guaranteed drift. Rejected.
- **Sensitivity as static omission only.** Cannot express "this principal may
  see this field," and duplicates read-scoping. Rejected.
- **Portals evaluating policy.** Couples every portal to the policy vocabulary
  and duplicates the engine. Rejected; the navi resolves.

## Open questions

1. **Lineage depth.** Whether the full chain is retained or only the originating
   principal and the immediate parent.
2. **Cross-navi lineage.** How a peer navi's principal composes with a local
   principal when work crosses a navi boundary (ADR 0014, ADR 0021).
3. **Per-call cost.** Whether a per-call authorization check is cheap enough on
   the hot path, or whether a short-lived cached decision is needed.
4. **Read-scoping query shape.** Whether principal-aware reads filter in the
   provider or in the surface, and how a filter composes with the declared
   per-type filters (ADR 0017 open question 4).

## Related

- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — the ceiling, amended by 0021
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — the thought model this extends
- [ADR 0017](./0017-navi-state-as-typed-objects.md) — the object surface, sensitivity, and the shared write path
- [ADR 0019](./0019-portal-transport-bindings.md) — per-connection identity and the two gates
- [ADR 0021](./0021-principals-trust-and-policy.md) — principals, trust, and policy
