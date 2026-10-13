---
status: proposed
date: '2026-10-13'
supersedes: []
amends: ./0025-principal-aware-reads.md
tags:
  - adr
  - security
  - object
  - portal
  - authorization
---

# 0028 — Row-level read-scoping

## Context

ADR 0025 decision 3 decided that a type may declare a **scoping filter**, that
the surface ANDs it with the caller's query, and that a caller may narrow but
never widen — and deliberately deferred the implementation. NAVI-416 implements
it, and found the ground missing: **`object.Query.Filter` was declared but
applied by nobody.** Every provider paged the full list and ignored the query's
filter, so a scope added by the surface would have been a silent no-op — the
fail-open direction.

ADR 0025 decision 2 also fixed the split this record works within: **field
projection stays in the surface; row filtering stays in the provider.**

## Decision

1. **A type may declare a `Scope`, and it is closed and referential.** The one
   rule is "the named field equals the acting principal's reference"
   (`Scope{Field: "origin"}`), so there is no expression language (ADR 0017) and
   the scoped field must be one the type already declares. A type with no scope
   is not row-scoped.

2. **The surface ANDs the scope into the caller's filter, and the scope wins a
   conflict.** A caller that names the scoped field itself cannot reach another
   principal's row — the narrowing layer is applied last.

3. **Providers apply the declared filter before paging** — equality, and `$in`
   for a list of allowed values — so a page stays full and the cursor is an
   index into the filtered list. A filter applied after paging would return
   short pages and lose rows.

4. **The surface also enforces the scope on the way out.** The scope is a
   security property, so a provider that ignores the filter cannot leak a row:
   the surface re-checks what it returns. The provider filter is what makes
   paging correct; the surface check is what makes the scope true.

5. **A row the scope excludes is reported as ABSENT, not refused.** `Get` and a
   detail's parent answer `ErrNotFound`, because whether an id exists is itself
   something a scoped principal may not learn.

6. **The scope applies to every principal, the navi's service principal
   included.** There is no root exemption — the console is not special-cased
   (ADR 0025 decision 5). An author who wants the console to see every row
   simply does not declare a scope on that type, and the built-in types declare
   none by default.

7. **A scoped field is exposed with its EFFECTIVE value.** `thought.origin` shows
   the principal that caused the thought, or the navi's own service principal
   for system-originated work — so a scope on `origin` matches the navi's own
   machinery rather than hiding it from the navi that did it.

## Consequences

- The mechanism exists and is opt-in: a deployment declares a scope per type, and
  the reference navi declares none, so its console still sees the whole queue.
- "Read-scoping and field sensitivity compose" is now true in both dimensions:
  the read gate decides whether the type is readable, the field grant decides
  which fields, and the scope decides which rows.
- A provider that ignores filters is **backstopped** (no leak) but pages badly,
  which is a correctness bug rather than a security one — and the built-in
  providers now apply filters, so the shape is set by example.
- The scope is a *declaration on the type*, so it is visible in `main.go`
  alongside the type, not buried in a policy.

## Alternatives considered

- **Post-filter in the surface only.** Simple and fail-closed, but it pages
  wrongly (a page can filter to empty while matching rows remain) and it puts
  row semantics — the provider's domain — in the surface (ADR 0025 decision 2).
  Rejected; kept only as a backstop (decision 4).
- **An expression language for scopes.** ADR 0017 rejected one as an injection
  surface; a single field-equals-principal rule covers the known cases.
  Rejected.
- **Exempt the service principal.** It contradicts "the console is not
  special-cased" (ADR 0025 decision 5), and an author who wants the console to
  see all rows has a simpler tool: no scope. Rejected.
- **Refuse a scoped-out read with `ErrUnauthorized`.** It turns a scoped read
  into an existence oracle. Rejected; decision 5.
- **Put the scope in the policy instead of the type.** A capability says *what*
  may be done; the scope says *which rows* — the same split ADR 0025 decision 2
  draws between the read gate and the filter. Rejected.

## Open questions

1. **Relations beyond equality.** Whether a scope should express "the principal
   is in the object's group", a claim match, or a set membership, and how it
   stays closed.
2. **Per-provider coverage.** Which built-in providers should honour which
   filters, and whether a provider should be required to declare that it does.
3. **Composition with per-type filters.** ADR 0017 open question 4 names
   declared per-type filters; whether the scope is one of those or sits above
   them.

## Related

- [ADR 0025](./0025-principal-aware-reads.md) — decision 3, the mechanism this implements
- [ADR 0017](./0017-navi-state-as-typed-objects.md) — the descriptor, sensitivity, and the query shape
- [ADR 0022](./0022-delegated-authorization.md) — the acting principal the scope compares
