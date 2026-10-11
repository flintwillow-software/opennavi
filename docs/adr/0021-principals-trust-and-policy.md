---
status: proposed
date: '2026-10-10'
supersedes: []
amends: ./0006-compile-time-omission-plus-runtime-ceiling.md
tags:
  - adr
  - security
  - identity
  - trust
  - policy
---

# 0021 — Principals, trust, and policy

## Context

A navi has two authorization gates today: attachment authorizes a connection,
and the object surface authorizes each action. Both delegate to
`portal.Authorizer`, whose only question is `Authorize(action, identity)`.

The identity in that question is not established by mechanism. The gateway
reads it from an `identity` query parameter or the `X-Portal-Identity` header,
and the control body may carry its own; nothing verifies it. The object surface
does not use the connection's identity at all — `navi.WithObjectAuthorizer(auth,
identity)` declares one identity for the whole surface, so every action is
authorized as the same navi-declared principal (`"operator"` in the reference
navi, which also wires `portal.AllowAll()`). Reads are gated only by attachment,
and sensitive fields are omitted for everyone by a static projection with no
allow-list. There is no user, role, group, or trust concept anywhere;
`memory.subject` is a free string.

This is honest only on a trusted LAN, and that assumption is nowhere recorded as
a decision. ADR 0019 decision 6 names the destination — identity becomes
per-connection, and IAM scoping is the layer above — but leaves the model
unbuilt. This record decides who a principal is, where trust comes from, and
what may be granted at build time versus resolved at runtime.

## Decision

1. **A principal is an identity with authenticators attached.** Kinds are open:
   `user`, `service`, `connector`, `navi`, `group`, and future kinds. A principal
   may hold several keys, so rotation and revocation do not change identity, and
   the key system is pluggable. A blockchain-backed authenticator is a deferred
   open question, not a decision here.

2. **A group is a named collection of principals**, usable as a policy subject
   and optionally authenticatable itself (a shared identity with its own keys).
   **A role is a compiled capability set** and is not a principal. A
   group-as-principal has weaker attribution — the navi learns that the group
   acted, not which member — and that trade is accepted rather than hidden.

3. **Authorization policy is compiled at the navi level.** The capability
   vocabulary, the roles, the groups, and the policies that map
   role/group/principal to capability are build-time declarations, like tools,
   models, and types.

4. **Identity data is resolved at runtime.** Which principals exist, their
   authenticators, their group membership, and their claims come from a trust
   source. Runtime resolution never introduces a capability or a policy.

5. **A role is a trust policy plus a permission policy** — who may hold it and
   what it may do, kept separate. A group's membership is the trust side; the
   group's capabilities are the permission side.

6. **Trust sources are declared at compile time.** A navi declares where trust
   comes from: principals inline in the navi definition, an OIDC identity
   provider, or a keyed trust store (a database or file). The reference is
   compiled and the contents are resolved at runtime, exactly as ADR 0008 treats
   a credential reference.

7. **The identity provider supplies identity and claims; the navi supplies
   policy.** An IdP that could supply policy would be runtime-granted capability,
   which ADR 0006 forbids.

8. **Credentials are typed by principal kind.** Humans authenticate through an
   OIDC session; services and peer navis present an Ed25519 keypair. Every
   credential has a validity period, and a maintainer may revoke it. Revocation
   and expiry are runtime narrowing.

9. **Root/admin is a declared role.** Its key is an operator-supplied credential
   reference (configuration), and that configuration is therefore
   root-equivalent. The in-process console is root structurally — it is the
   binary, with no transport. No network location grants admin: source host and
   network are conditions on a request, never an identity.

10. **The navi has a service principal, `navi:<name>`**, with a compiled trust
    policy. System-originated work — cron, the plan-closing check, and any
    inbound work whose connector declares no principal — acts as it, so
    fail-closed authorization never disables the runtime's own machinery.

11. **The capability vocabulary is closed and referential.** It names things
    that already exist — a type name, a field name, an action name, a tool name
    — rather than introducing a parallel namespace. The verbs are `connect`,
    `read`, `act`, `call`, and `enqueue`. The evaluation rules are default-deny,
    explicit-deny-wins, allow-required, and intersection with the compiled
    ceiling.

12. **Every trust event is audited**, separately from the navi-authored
    interaction record. The trust ledger — who was granted, by whom, when — is
    operator-facing; the interaction record — who did what — belongs to the
    navi's own state.

## Consequences

- ADR 0006 is amended: it governs the capability vocabulary and policy, while
  identity resolution is a runtime input to compiled policy. Adding a principal
  to a group is not a policy change.
- The single declared identity disappears. `navi.WithObjectAuthorizer(auth,
  identity)` becomes a lookup from a verified principal to a compiled policy.
- The reference navi's `portal.AllowAll()` and the `"operator"` constant become
  a development default, not the shipped model.
- Trust can be rotated and revoked without a rebuild; capability cannot change
  without one.
- The security engine is core navi, not a component contract. Portals ask the
  navi to authenticate and authorize; they never see the vocabulary, roles, or
  trust sources (ADR 0022).
- An external trust source becomes an availability dependency: when the IdP is
  unreachable, only the compiled console root and any compiled principals
  remain. That is the accepted break-glass.
- `memory.subject` becomes a principal reference rather than a free string.

## Alternatives considered

- **A mutable trust table in the navi.** No audit, no separation between policy
  and identity, and history can be rewritten. Rejected.
- **An agent-callable trust tool.** A prompt-reachable path from agent injection
  to trust escalation. Rejected; trust mutation is a principal-scoped
  capability, not a tool.
- **localhost implies admin.** Location is not identity; on a multi-user host
  the loopback is shared, and behind a proxy the source is the proxy. Rejected.
- **A host allow-list as authorization.** The same error; host and network are
  conditions, never principals. Rejected.
- **Trust-on-first-use.** Imports SSH's known first-contact weakness. Rejected.
- **A compiled-in root key literal.** ADR 0008 forbids literals, and rotation
  would need a rebuild. Rejected.
- **A full IAM-style policy language.** ADR 0017 rejected a generic expression
  language as an injection surface; a policy DSL is a larger one. Rejected in
  favor of a closed, compiled vocabulary.
- **Policy supplied by the identity provider.** Runtime-granted capability;
  ADR 0006 forbids it. Rejected.
- **Resource-based principals (per-object grants).** Needed for cloud
  deployments, but not yet; deferred rather than rejected.

## Open questions

1. **Blockchain-backed authenticators.** A tamper-evident key registry or signed
   message log is a plausible authenticator; whether it buys anything over a
   signed transparency log is undecided.
2. **Resource-based principals.** When a cloud deployment needs per-object
   grants, does that become a second grant path or a projection of the
   identity-based model?
3. **Group-as-principal attribution.** Whether a shared-key group should be
   permitted at all in deployments that require per-principal attribution.
4. **Condition vocabulary.** Which conditions ship first beyond time, credential
   validity, network, and claim match, and who may extend the set.

## Related

- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — the ceiling this amends
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credential references
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations and the injection boundary
- [ADR 0019](./0019-portal-transport-bindings.md) — per-connection identity and the IAM layer above
- [ADR 0022](./0022-delegated-authorization.md) — where the policy is enforced
