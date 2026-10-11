---
status: accepted
date: '2026-10-11'
supersedes: []
amends: ./0014-connectors-are-bidirectional-service-integrations.md
tags:
  - adr
  - connector
  - subscription
  - transport
  - security
---

# 0023 — Connector inbound: build-time subscriptions and delivery transport

## Context

ADR 0014 established that a connector is a bidirectional service integration
and specified its outbound half — egress tools the author adds explicitly via
`connector.SendTool()`, gated by `connector.Live[C]`. NAVI-371 shipped that and
proved it with `connector/github`, which today exposes one outbound tool
(`github_issue_comment`) and **no inbound loop at all**. A connector that only
speaks outward is half a connector.

ADR 0014 left the inbound half's governing question open (its open question 3):
*which inbound events a connector watches* — a build-time declaration with
runtime credentials, consistent with ADR 0013, or something partly runtime.
That decision constrains two shapes: what a subscription is, and how events are
delivered. This record resolves the question and fixes both.

Two other accepted records bear on it. ADR 0013 makes context and capability
build-time declarations because a prompt-reachable path to new inbound work is
an injection surface. ADR 0021 gives every unit of work a principal, so an
inbound stimulus that becomes a thought has an origin — the connector's service
principal (ADR 0021 decision 10).

## Decision

1. **A subscription is a build-time declaration.** Which repositories, which
   event kinds, and which intent an event produces are declared where the
   connector is composed, like tools and egress tools. Runtime may **narrow** —
   credentials, enable/disable, a repository allow-list — and may never add an
   event kind or a repository the binary did not declare. The rationale is
   ADR 0013's injection boundary: a prompt cannot open an inbound path.

2. **A subscription declares the service principal its inbound work acts as.**
   Each inbound stimulus is attributed to a principal named at composition
   (ADR 0021 decision 10); the thought it produces carries that principal as
   its origin (ADR 0022 decision 2). A connector may act as its own principal or
   as `navi:<name>`. Until the principal model lands, the origin is the current
   identity string — this is the retrofit point, recorded so it is deliberate.

3. **Delivery transport is a per-connector binding, not a mode.** A connector
   declares how it receives events — a webhook endpoint or a poll loop — the way
   a portal declares its transport (ADR 0019). Both are supported, and a
   connector may offer one or both; the binding is visible where the navi is
   composed.

4. **Polling is the first GitHub binding; webhook is the reachable-endpoint
   binding.** A navi should not have to be publicly reachable to receive work.
   Polling needs only outbound API access and an API budget, which a homelab
   navi has; a webhook binding is adopted when a reachable endpoint (a tunnel, a
   reverse proxy) is configured, and it verifies the provider's signature. This
   is the decision most worth review.

5. **Idempotency is explicit and persisted.** A redelivered event must not spawn
   a second thought. The connector dedupes on the provider's event identity
   (delivery id / event id), and the seen-set is persisted through the datastore
   rather than held only in RAM, so a restart does not replay. This is the same
   class of problem as NAVI-382's duplicate-check suppression.

6. **The inbound half reuses the connector's client and credential.** ADR 0014's
   reason for one component rather than two: inbound and outbound share the
   connection, the configuration, and the credential. The inbound loop consumes
   the client published into `connector.Live[C]` at `Start`; it does not build a
   second client.

7. **Inbound secrets follow the egress rule.** A webhook secret is a credential
   reference resolved at the point of use (ADR 0008), never written to a
   transcript, the datastore, or logs.

8. **The lifecycle vocabulary is not reinvented.** An inbound event maps onto
   the existing spawn / attach / steer vocabulary connectors already use; the
   subscription declares which intent each declared event kind produces.

## Consequences

- ADR 0014 open question 3 is resolved, and the connector is bidirectional
  without the "egress half only" caveat.
- The inbound surface is greppable in `main.go`, the same property egress has.
- A navi can receive work without being publicly reachable (polling) and can
  adopt webhooks when it is; the choice is explicit at composition.
- The dedupe set is persisted state, so it needs a datastore aggregate
  (ADR 0011) and a retention story.
- Service-principal attribution is the first consumer of ADR 0021 decision 10.
  Until that lands, inbound thoughts carry the current string identity and are
  retrofitted — a small, named cost, not a silent one.
- A polling connector needs an API budget and a poll cadence; a webhook
  connector needs reachability and signature verification.

## Alternatives considered

- **Runtime-configurable subscriptions.** A prompt- or config-added inbound
  path contradicts ADR 0013's injection boundary. Rejected.
- **Webhook first.** Lower latency and no API budget, but it requires the navi to
  be reachable, which the homelab case is not. Rejected as the *first* binding,
  not as a binding.
- **Polling only.** Reachability-free and simplest, but forfeits low-latency
  delivery when an endpoint exists. Rejected as the *only* binding.
- **A generic inbound subscription framework before one connector works.** Named
  out of scope in NAVI-394; it would generalize from zero examples. Rejected.
- **In-memory dedupe.** A restart replays redeliveries and spawns duplicates.
  Rejected.
- **A second client for inbound.** Duplicates the connection and credential that
  ADR 0014 exists to share. Rejected.

## Open questions

1. **Poll cadence and backoff** — per-connector configuration or a shared
   scheduler, and how a budget is respected.
2. **Dedupe storage** — a datastore aggregate of its own or a connector-owned
   view, and its retention window.
3. **Multiple principals per connector** — whether a connector may declare more
   than one service principal (e.g. a per-repository identity).
4. **Webhook endpoint ownership** — whether the endpoint is served by the
   connector or by the navi's existing HTTP surface.

## Related

- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credential references
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — persisted state and aggregates
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations and the injection boundary
- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — the connector this amends
- [ADR 0019](./0019-portal-transport-bindings.md) — transport as a binding
- [ADR 0021](./0021-principals-trust-and-policy.md) — the service principal inbound work acts as
