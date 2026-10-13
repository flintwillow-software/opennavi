---
status: proposed
date: '2026-10-13'
supersedes: []
amends: ./0021-principals-trust-and-policy.md
tags:
  - adr
  - security
  - audit
  - trust
  - attribution
---

# 0027 — Attribution: the interaction record and the trust ledger

## Context

ADR 0021 decision 12 decides that "every trust event is audited, separately
from the navi-authored interaction record", and names the split: the **trust
ledger** — who was granted, by whom, when — is operator-facing, while the
**interaction record** — who did what — belongs to the navi's own state
(ADR 0011/0013).

Half of that exists. NAVI-404 gave a thought its `Origin`, `OriginInfo`, and
`Lineage`, so "on whose behalf did this happen" is recorded on the thought and
inherited down a chain. But none of it is visible through the object surface, so
a portal cannot read it; and there is no trust record at all.

The second half is harder than it reads, because **a grant or revocation happens
outside the navi**. Trust is either a compiled declaration (build-time) or the
contents of a trust store an operator edits; the navi observes the result and
has no actor to name as "by whom". ADR 0021 rejects an agent-reachable trust
mutation path, so there is nothing today that would append a `granted` event
with an operator as its author. A ledger that claimed to record grants would be
inventing an event the navi never witnesses.

## Decision

1. **The interaction record is the thought, exposed.** A thought carries its
   originator, the request facts it arrived with, its lineage, its mode, and its
   transcript; that *is* the record of who did what. `origin` and `lineage` join
   the thought type as read-only fields, so the record is answerable through the
   object surface like every other piece of navi state, and it is persisted with
   the thought (ADR 0011 decision 8).

2. **The trust ledger is operator-facing and separate.** It is not the navi's
   reasoning state, not the transcript, and not stored with a thought. It is an
   append-only record of **trust decisions**, read through its own surface.

3. **The ledger records the decisions the navi makes, not grants it cannot
   observe.** The event vocabulary is closed and referential to the mechanisms
   that exist: a credential was **authenticated** (which principal, when, and by
   which scheme — a bearer session or a signed request), or a credential was
   **refused** (what was presented, and why). A future operator
grant/revocation path appends `granted`/`revoked` carrying the acting operator
as `By`; until that path exists, the ledger does not pretend to know it.

4. **The ledger is bounded and read as a capability.** A bounded ring
   (drop-oldest, like the observation mailbox and the object delta window) keeps
   it from being an unbounded memory sink, and it is exposed as a read-only
   `trust_event` type — so reading the ledger is a capability
   (`read:trust_event`) that a deployment grants, and an operator portal renders
   it like any other type rather than through a bespoke endpoint.

5. **A trust event never carries a credential.** The ledger records identity,
   mechanism, and outcome — never a token, a signature, or key material
   (ADR 0008).

## Consequences

- "On whose behalf did this happen" is answerable from the record without a new
  store: the thought's origin and lineage are exposed where a portal already
  reads, and the transcript carries what happened.
- Trust decisions are auditable separately from work: an operator can see who
  was trusted and who was refused, and a refusal is as visible as a success.
- The ledger being a capability means a deployment decides who may read it, and
  the answer composes with everything else (`read:trust_event`, narrowed by
  roles and the ceiling).
- An in-memory ring loses its oldest entries and does not survive a restart; a
  durable ledger is a datastore view and a follow-up, not a promise this record
  makes.

## Alternatives considered

- **One record for both.** ADR 0021 decision 12 rejects it: a work record that
  also holds trust history is neither the navi's state nor a clean audit, and
  the two have different readers and different retention.
- **Record grants from the trust store's contents.** The navi cannot name an
  author for a file edit, so the ledger would attribute a grant to nobody.
  Rejected; decision 3.
- **A bespoke ledger endpoint or CLI.** A portal already renders declared types;
  a second read path is a second authorization story. Rejected; decision 4.
- **Record the credential for forensics.** A ledger of tokens and signatures is
  a secret store with the worst retention policy. Rejected; decision 5.
- **Unbounded in-memory history.** Simple, and a slow leak in a long-lived navi.
  Rejected; decision 4.

## Open questions

1. **Durable retention.** Whether the ledger becomes a datastore view (with the
   metadata-without-bodies discipline of ADR 0011) and what retention an
   operator wants for it.
2. **The operator grant/revocation path.** The API and the author that a
   `granted`/`revoked` event would name; ADR 0021 decides it is a
   principal-scoped capability, not a tool.
3. **A queryable interaction record.** Whether "who did what" needs a view
   beyond the thought (an actor-scoped index) or the lineage chain is enough.
4. **Naming the resolving trust source.** The ledger records the presentation
   scheme; naming *which* source answered (an OIDC issuer, a store path, a
   compiled declaration) needs the trust-source seam to identify itself, which
   it does not today.

## Related

- [ADR 0021](./0021-principals-trust-and-policy.md) — decision 12, the split this implements
- [ADR 0022](./0022-delegated-authorization.md) — the originator and lineage this exposes
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — where the interaction record persists
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — why a credential never enters the ledger
