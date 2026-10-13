---
status: accepted
date: '2026-10-13'
supersedes: []
amends: ./0017-navi-state-as-typed-objects.md
tags:
  - adr
  - security
  - object
  - portal
  - authorization
---

# 0025 — Principal-aware reads: field sensitivity and read scoping

## Context

ADR 0017 decision 5 made the **surface**, not the portal, responsible for
omitting fields a caller may not see, with an "explicit allow-list at the navi
level", and left the query shape open (its open question 4). What shipped was
static omission: the projection dropped every `Sensitive` field for everyone.
Sensitivity was therefore a property of the *type*, not of the caller — either
everyone saw a field or nobody did, and there was no way to express "this
principal may see this field".

ADR 0022 decision 7 fixes the intent: "A sensitive field is one a principal may
see only with a grant, not one the surface removes for everyone. Projection
becomes principal-aware, and the descriptor's sensitivity remains the
declaration that a grant is required." NAVI-404 gave the object surface a
per-connection actor and a compiled-policy gate; this record decides how
projection uses it and settles the query shape.

## Decision

1. **Projection is principal-aware, and it happens in the surface.**
   `Sensitive` on a field is the *declaration* that a grant is required; the
   grant is `read:<type>.<field>`, and a `read:<type>` grant deliberately does
   not cover it (the referential rule, ADR 0021 decision 11). The surface
   includes a sensitive field only when the acting principal holds the field
   grant; a field the principal may not read is **absent, never null**.

2. **Field projection stays in the surface; row filtering stays in the
   provider.** A descriptor is the surface's ABI, so a provider is never trusted
   to enforce it (ADR 0017 decision 5). Which *rows* a principal may see is a
   different question, answered where the query is: the provider.

3. **Row-level read-scoping is a declared per-type filter, composed with the
   caller's.** A type may declare a scoping filter; the surface ANDs it with the
   caller's `Query.Filter` before the provider sees it, so a caller can narrow
   but never widen. There is no expression language (ADR 0017, alternatives):
   the filter is a declared, closed shape, as the query already is.
   **Implementation is deferred** — the mechanism is decided here; the
   declaration and its per-provider application are a follow-up.

4. **A delta never carries a sensitive field.** The live delta window is shared
   by every connection (ADR 0017 decision 6), so a delta carrying a sensitive
   field would leak it to a connection that may not read it. A granted principal
   reads a sensitive field through `Get`/`List`/`Detail`; live updates for
   sensitive fields would need per-connection projection and are not promised.

5. **The console is not special-cased.** The in-process console acts as the
   navi's service principal, which holds the compiled ceiling — and the ceiling
   includes every declared field grant — so it sees sensitive fields because it
   is *granted* them, not because the surface treats a console differently.

## Consequences

- Read-scoping and field sensitivity **compose** rather than duplicate: the read
  gate decides whether the type is readable at all, the field grant decides
  which fields, and (with decision 3) a declared filter decides which rows.
- A portal renders what it is given and learns nothing about grants; a portal
  that does not hold a field simply does not receive it.
- The reference navi and the in-process console are unchanged in effect (the
  service principal holds the ceiling). A remote principal now needs
  `read:<type>.<field>` for a sensitive field and fails closed without it.
- A principal holding `read:<type>` no longer sees sensitive fields — which is
  the point, but it is a behavior change for any deployment that granted type
  reads and relied on static omission.

## Alternatives considered

- **Keep static omission and add a second "sensitive view" type.** Two
  descriptors for one struct, and the portal chooses between them — which moves
  an authorization decision into the portal. Rejected.
- **Filter rows in the surface.** The surface would have to know each type's
  semantics, which is the provider's domain, and a provider could still return
  rows the surface cannot judge. Rejected; decision 2.
- **An expression language for scoping.** ADR 0017 rejected it as an injection
  surface; a declared filter is the same closed shape the query already is.
  Rejected.
- **Per-connection projection of deltas.** Correct in principle, but it means a
  per-connection delta window or a raw delta store projected at read time.
  Deferred (open question 1), not rejected.
- **Carry grants on the descriptor.** The descriptor is a frozen declaration
  (ADR 0017 decision 2); who holds a grant is runtime identity, not build-time
  shape. Rejected.

## Open questions

1. **Live sensitive fields.** Whether a granted principal should receive
   sensitive-field updates live, and if so whether the shared window stores raw
   deltas and projects per reader, or each connection gets its own window.
2. **Declared scoping filter shape.** The concrete declaration (which field,
   which relation to the principal) and how it composes with the per-type
   filters ADR 0017 open question 4 names.

## Related

- [ADR 0017](./0017-navi-state-as-typed-objects.md) — the typed-object surface this resolves
- [ADR 0022](./0022-delegated-authorization.md) — decision 7, the intent this implements
- [ADR 0021](./0021-principals-trust-and-policy.md) — the capability vocabulary and the derived ceiling
