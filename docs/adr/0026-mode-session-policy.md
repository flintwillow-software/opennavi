---
status: accepted
date: '2026-10-13'
supersedes: []
amends: ./0022-delegated-authorization.md
tags:
  - adr
  - security
  - authorization
  - thought
  - policy
---

# 0026 — The session policy a mode imposes

## Context

ADR 0022 decision 4 decides that "the mode chosen at enqueue is a session
policy": it narrows the principal's policy for that thought and can never widen
it, and the selectable set is derived — "a principal may request a mode whose
`PermissionPolicy` does not exceed the principal's own, which
`PermissionPolicy.Exceeds` already computes". The engine half exists:
`Policy.Decide` takes `AccessRequest.Session *CapabilitySet` and requires every
layer to permit, so a session can only ever remove capability (NAVI-399). What
was missing is the derivation — how a `Mode` (a tool-surface `PermissionPolicy`
plus a tool subset) becomes the engine's `CapabilitySet` session — and the
principal-scoped selectable rule.

The two are not the same representation. A `Mode` restricts **tool calls** by
access level and by name; a `CapabilitySet` speaks the capability vocabulary
(`call:<tool>`, `read:<type>`, `act:<type>.<action>`, `connect`, `enqueue`).
The direction of the selectable comparison is also easy to get backwards: a
*more restrictive* mode must always be selectable, and a mode that permits
something the principal cannot do must not be.

## Decision

1. **A session is a narrowing layer, so it is expressed as wildcard allows plus
   a deny-list.** The engine's `CapabilitySet.Permits` is allow-required: a set
   with only denials would deny *everything*. A mode's session therefore allows
   every verb (`Any(VerbCall)`, `Any(VerbRead)`, `Any(VerbAct)`, `connect`,
   `enqueue`) and denies what the mode excludes. It grants nothing — the
   compiled ceiling and the principal's roles are checked independently — so it
   is never compared with the ceiling, and its wildcards are safe.

2. **A mode denies exactly what its `PermissionPolicy` and tool subset
   exclude**: every declared tool the mode's access cap forbids, every tool its
   deny-list names, every tool outside its declared subset, and — when the mode
   is capped at read-only — `act:*`, because read-only means no mutation of an
   object either. `enqueue` stays permitted: scheduling a follow-up is how a
   planning thought continues, not a mutation.

3. **The selectable set is checked against the principal, at the door.** A mode
   is selectable by an actor when every tool the mode permits is a tool the
   actor may already call, evaluated against the actor's roles **without** the
   session. A mode that permits a tool the actor may not call is refused loudly
   at enqueue, rather than silently narrowed to nothing at call time. A more
   restrictive mode is always selectable, because it permits fewer tools; a mode
   that declares no restriction is selectable exactly when the actor's own
   policy is at least as broad.

4. **The choice is recorded on the thought and enforced per call.** The mode
   rides `thought.Thought.Mode`, is validated when the thought is enqueued (the
   queue's assembler runs at the door, so a refusal is loud and nothing is
   scheduled), and the derived session is applied to every tool call the thought
   makes. The tool surface is already narrowed at bind time; the session is the
   engine's own layer, so a mode's restriction is enforced at the same place as
   every other capability decision rather than only by which tools were bound.

## Consequences

- A mode can never widen: the session is a required layer, and the engine
  already refuses when any layer refuses. Adding a mode-specific restriction in
  the future is a matter of denying another capability, not a new mechanism.
- A principal that cannot use a mode is told so when it enqueues, not when the
  thought runs — a failure at the door rather than a confusing half-run.
- The mode's meaning is now visible in one place: `modeSession`. A mode author
  who wants to restrict reads or acts, not just tools, extends the deny-list.
- System-originated work acts as the navi's service principal, which holds the
  compiled ceiling, so it may select any mode — the runtime's own machinery is
  not narrowed by a policy it did not choose.

## Alternatives considered

- **Represent the session as the mode's `PermissionPolicy`.** The engine speaks
  capabilities, and a tool-surface policy cannot express `read`/`act`/`enqueue`
  restrictions. Rejected; decision 1.
- **Express the session as an allow-list of the mode's permitted tools.** It
  would deny every read and act, because `Permits` is allow-required and the
  session is a layer that must permit too. Rejected.
- **Compare the session against the ceiling with `Exceeds`.** The session grants
  nothing, so a comparison would refuse every wildcard for being "wider" than
  the ceiling. The ceiling and the roles are the layers that grant; the session
  only removes. Rejected.
- **Filter the tool surface at bind time only (the pre-397 state).** It narrows
  which tools are *offered* but is not an engine decision, so it cannot express
  a mode that restricts a capability the surface still binds, and a revocation
  of the mode's meaning would need a rebind. Rejected; decision 4.
- **Refuse a mode only when it is unknown.** Cheap, but it lets a principal
  request a mode broader than its own policy and get a silently empty surface.
  Rejected; decision 3.

## Open questions

1. **Per-mode object restrictions.** Whether a mode should be able to restrict
   reads (`read:<type>`) or specific actions rather than only acts wholesale,
   and how a mode author declares that.
2. **Carrying the mode on every binding.** The control route and `navi enqueue`
   carry the mode; the unified stream's call channel does not yet, so a portal
   on that path enqueues with the navi's default mode.

## Related

- [ADR 0022](./0022-delegated-authorization.md) — decision 4, the intent this implements
- [ADR 0021](./0021-principals-trust-and-policy.md) — the capability vocabulary and the derived ceiling
- [ADR 0007](./0007-thought-modes.md) — the mode this narrows with
