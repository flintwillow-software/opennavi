---
status: proposed
date: '2026-10-14'
supersedes: []
amends: ./0023-connector-inbound-subscriptions-and-transport.md
tags:
  - adr
  - connector
  - webhook
  - transport
  - security
---

# 0029 — The webhook inbound binding: connector-owned endpoints behind one navi webhook router

## Context

ADR 0023 made a connector bidirectional, made delivery transport a **per-connector
binding**, chose polling first, and named the webhook as the reachable-endpoint
binding. It left two things to a later record: the binding's exact contract, and
**endpoint ownership** (its open question 4) — whether a webhook endpoint is
served by the connector or by the navi's existing HTTP surface.

ADR 0024 resolved the **fleet** half of open question 4: one relay, a dumb pipe
that forwards the raw body plus the signature headers, routing by provider and
destination, with the **navi** verifying so webhook secrets never live in the
cluster. It left the **navi-side** half open — it says only that "the navi side
needs a client: a transport binding, or a connector".

The polling binding shipped (NAVI-394, `connector/github/inbound.go`): build-time
`Subscription` declarations, a persisted seen-set, deterministic thought ids, and
an enqueue path that turns a normalized event into a thought. The webhook binding
(NAVI-410) and the relay (NAVI-411) need the seam it must implement.

Three facts shape the decision:

- **The navi's existing HTTP surface is the portal gateway** (ADR 0018, ADR
  0019). Its authentication is a **verified principal** — a bearer token or a
  signed request resolved through the navi's trust sources (ADR 0021/0022). A
  webhook's authenticity is a **provider signature** over the raw body, and the
  sender is not a principal of this navi at all.
- **A webhook endpoint is a public, unauthenticated ingress**, and its payload is
  untrusted input that becomes a thought. ADR 0013's injection boundary says what
  shapes the assembler is build-time declared; ADR 0013's residual risk says
  stimulus *content* can still try to steer the model, bounded by the capability
  surface.
- **ADR 0014 decision 3 rejected a generic webhook component with a standard
  envelope.** Real services do not share a payload, so a common shim becomes a
  lowest-common-denominator every connector bypasses.

The navi's ingress is **two listeners, not one**: a **webhooks listener** for
third-party triggers and event streams (GitHub, Forgejo, and the like), and a
**websockets listener** for portals, real-time connectors, and peer navi
traffic. They have different trust models — a provider signature versus a
verified principal — and different traffic shapes, so they do not share a
socket. This record decides only the **webhooks** listener and the binding that
serves it; the websockets listener is the portal transport (ADR 0019) and the
peer transport (NAVI-412).

## Decision

1. **The webhook endpoint is connector-owned.** A connector declares a webhook
   binding where it is composed and provides the handler that verifies and
   processes it. The navi's **portal surface is not the webhook surface**: the
   gateway does not serve webhook routes, hold provider vocabulary, or
   authenticate a provider, and a webhook request never passes through
   `portal.Authenticator`. The two authentication models — a provider signature
   and a verified principal — do not share a dispatcher. This is ADR 0014
   decision 3 applied to inbound: provider-specific semantics — the signature
   scheme, the payload's shape — belong to the connector. What the navi supplies
   is a **router** that maps a path to a connector-registered handler; the router
   never parses, verifies, or interprets a payload, so it is plumbing rather than
   the generic provider dispatcher ADR 0014 rejected.

2. **The navi owns one shared webhook listener with a router; the connector owns
   the route.** Rather than each connector owning a listener, the navi composes
   **one** webhook ingress — one address, one certificate, one exposure, and one
   of the navi's two ingress listeners (the other is the websockets listener for
   portals and peers) — and a router that dispatches a webhook request by path to
   the handler a connector registered. A connector declares its route and its
   secret reference where it is composed, visible in `main.go` the way egress
   tools (ADR 0020) and subscriptions (ADR 0023 decision 1) are, and registers
   its handler on the router at `Start`; the navi removes it at `Stop`. Runtime
   may **narrow** a binding (enable/disable, a repository allow-list); it never
   adds a route, a provider, or an event kind. The seam is a handle the navi
   supplies through `connector.Deps` (ADR 0004), so it stays contract-pure: the
   interface is `net/http` and the standard library.

3. **The router is the webhook-events log's choke point.** One webhook listener
   means every delivery passes the same router, so the navi — not the connector
   — records it: when, from where, which route (provider), the delivery id, the
   verification outcome, and the outcome (a thought id, a duplicate, or a
   refusal). A connector cannot bypass it, because it never owns the socket. The
   log holds metadata and outcome — never the secret, and not the raw payload
   body by default, since a delivery may carry private or untrusted content. It
   starts as **structured log lines** written by the router; the persisted,
   browsable form — a bounded, read-only object type with the trust ledger's
   shape (ADR 0027) — is tracked separately (NAVI-433), so the binding can ship
   before the aggregate does.

4. **The navi verifies the signature over the raw body, before parsing.** The
   scheme is the provider's — for GitHub, HMAC-SHA256 over the exact received
   bytes (`X-Hub-Signature-256`), compared in constant time. Verification
   happens **at the navi**, never at the relay (ADR 0024 decision 4), and it
   happens **even when the request arrived through the relay**: the relay is a
   trust boundary, not a trust authority (ADR 0024 decision 5), so a
   compromised or misconfigured relay cannot inject an event. The body is read
   whole and bounded before it is parsed, and the signature is checked before
   any field is trusted.

5. **The secret is a credential reference, resolved at the point of use, and it
   never leaves the navi.** It is an ADR 0008 reference (an environment variable
   name or a file path), not a literal, and is never written to the cluster
   (ADR 0024 decision 4), a log, a transcript, or the datastore (ADR 0023
   decision 7). A binding whose secret cannot be resolved **refuses every
   request** rather than accepting unverified input, and the navi still runs:
   the capability is inert, not fatal (ADR 0020 decision 7). It fails closed,
   and it fails loudly — a warning names the reference, never a value. The
   binding takes an **array** of references and verifies against each, so a
   rotation can overlap: a delivery is accepted if it matches **any** configured
   secret. One secret is the common case; the array shape is what makes rotating
   one a config change rather than a cutover, and it costs nothing when the
   array has a single entry.

6. **Idempotency is the shared event identity, persisted, so the two transports
   dedupe against each other.** The binding keys the **same persisted seen-set**
   (ADR 0023 decision 5) on the event's **normalized identity** — the underlying
   object the event is about (`comment-<id>`, `issue-<number>`), which the
   polling binding already computes — so a delivery seen by one transport is not
   reprocessed by the other when both run for one subscription. The provider's
   delivery id (`X-GitHub-Delivery`) is recorded in the events log (decision 3)
   rather than used as the dedupe key. A redelivery is acknowledged and spawns
   no thought. The **deterministic thought id** (`github-<subscription>-<event
   id>`) is the second backstop, so a redelivery that slips past the seen-set
   collides on the queue rather than duplicating (ADR 0023 decision 5,
   NAVI-394). The seen-set is marked **only after a successful enqueue**, so a
   failure is retried by the provider rather than lost.

7. **The endpoint is a stimulus boundary, not an assembler input.** The payload
   is untrusted and bounded: the body is size-limited before it is read, and a
   delivery is rejected before parsing if it is not a declared event. What it
   becomes is the thought's **stimulus** — the subscription's declared prompt
   plus normalized, bounded event fields (ADR 0023 decisions 2 and 8) — and
   **never** the assembler's configuration (ADR 0013 decision 4): it cannot add
   a context source, select a skill, or change a prompt template. The residual
   risk is ADR 0013's, stated plainly: content *inside* the thought can attempt
   to steer the model, and that is bounded by the compiled capability surface,
   which is why the surface is kept small. The payload does reach the thought's
   context as stimulus — the polling binding already does this — and that is the
   intended difference between *content* (allowed, bounded by capability) and
   *assembly* (build-time only).

8. **A webhook is authenticated by signature, not by a principal; the thought it
   produces has one.** The endpoint is not a portal and does not authenticate a
   sender. The resulting thought acts as the subscription's **declared service
   principal** (ADR 0023 decision 2, ADR 0021 decision 10), so its capability
   and attribution are the declared ones, never the sender's. A subscription
   that names `navi:<name>` acts as the navi; one that names
   `connector:github` acts as the connector.

9. **Acknowledgement is explicit and provider-correct.** The endpoint answers:

   | Outcome | Status |
   |---|---|
   | accepted (a duplicate included) | `2xx` |
   | missing or bad signature | `401` |
   | non-POST method | `405` |
   | body over the bound | `413` |
   | rate-limited (shed) | `429` |
   | malformed body | `400` |
   | undeclared event kind | `2xx` (acknowledged and ignored) |
   | enqueue failed (not marked seen) | `5xx` (provider retries) |

   An undeclared event kind is not an error — GitHub sends only events the App
   subscribes to, and refusing would make it retry forever — so it is
   acknowledged and dropped. A `5xx` is reserved for the one case where a retry
   is the right response.

10. **The webhook and polling bindings are one connector with two transports.**
    They share the subscription declaration, the client, the credential, the
    seen-set, and the enqueue path (ADR 0023 decisions 3 and 6). Polling stays
    the reachability-free fallback (ADR 0023 decision 4); the webhook binding is
    an addition, not a replacement, and a connector may offer one or both. A
    declaration the binding cannot serve — an intent it does not implement, an
    event kind it does not know — is refused at composition, not dropped at
    runtime (the polling binding's `validateSubscription` pattern).

11. **Relay and direct are one endpoint.** The same router and the same handler
    serve a direct post and a relay-forwarded one; which is used is a deployment
    choice (ADR 0024 decisions 2 and 3). The relay forwards the raw body and the
    signature headers to the navi's one inbound address and is not trusted for
    authenticity, so the two paths cannot drift.

12. **Rate limiting is layered: the navi and the relay both enforce it.** The
    endpoint is public and unauthenticated before its signature is checked, so
    neither layer is trusted to be the only defense — the platform expects
    people to expose navis, and one missing limit must not be an open door. The
    **navi** rate-limits inbound webhooks — per source and per route — and sheds
    load rather than queueing unbounded work; the **relay** (ADR 0024)
    rate-limits at the fleet edge, before traffic reaches any navi, and its
    limits cover every navi behind it (implemented with the relay, NAVI-411). A
    single-tenant homelab navi may set a generous limit and lean on the relay,
    but a navi exposed directly must enforce its own. The limits are runtime
    configuration that may only narrow what the compiled declaration allows
    (ADR 0006), and a shed request is refused `429` rather than processed late.

## Consequences

- **ADR 0023 open question 4 is fully resolved.** ADR 0024 decided the fleet
  fabric; this record decides the navi-side endpoint: the connector owns it, and
  the navi's portal surface is not involved.
- **The capability surface is unchanged in shape.** A webhook endpoint exists in
  a binary only if the author declared the binding, exactly as an egress tool
  exists only if the author added it (ADR 0006) — so "can this navi be reached
  by a provider?" is answerable from `main.go`.
- **The portal's trust model is not diluted.** A webhook can never reach the
  portal or object routes, and a portal credential can never satisfy a webhook:
  they are separate endpoints with separate authentication.
- **One webhook port, one certificate, one place to watch.** Every connector's
  webhook route shares the navi's webhook listener, so exposing a navi to a
  provider (directly or through the relay) is one address, and every delivery
  passes one router — which is what makes the webhook-events log a single,
  non-bypassable record. The websockets listener is a separate ingress with its
  own trust model.
- **`connector.Deps` gains one handle.** The router is supplied through `Deps`
  (ADR 0004), so the connector contract changes by one stdlib-typed field rather
  than gaining a listener of its own; the package stays contract-pure.
- **A webhook binding adds a thing to configure** — a route and a secret
  reference (the address is the navi's, declared once) — and a thing to expose.
  Polling needs none of it, which is why it remains the default.
- **The relay's routing table names a navi destination per provider** (ADR 0024
  decision 6); with one navi inbound listener that destination is the navi's
  address, and the provider is the route the router dispatches on.
- **Replay is bounded by the event identity, not by a timestamp.** GitHub's
  signature covers the body alone, so a captured request replayed verbatim names
  the same underlying event and is deduped; a re-signed body would require the
  secret, which lives only with the navi.
- **A missing secret degrades one capability, not the navi** (ADR 0020 decision
  7), but it degrades to *refusing* rather than to accepting unverified input.
- **Two rate-limit configurations, deliberately.** The navi's limits and the
  relay's are separate; a deployment that exposes a navi directly must set the
  navi's, and one behind the relay gets the fleet edge as well. The redundancy is
  the point, and the cost is that neither can be assumed to cover the other.

## Alternatives considered

- **The navi's portal surface dispatches webhooks** (`/webhooks/{provider}` on
  the gateway). Rejected: it makes the gateway a provider dispatcher with
  provider knowledge, and it puts signature authentication and principal
  authentication behind one routing layer — exactly the conflation ADR 0014
  decision 3 and the trust model argue against.
- **A listener per connector.** Each connector owning its own address is
  symmetric with a portal owning its transport, but it multiplies exposed ports
  and certificates and scatters inbound traffic across N places — which is
  exactly what makes a single webhook-events log hard. Rejected in favour of one
  navi webhook listener and a router.
- **A generic navi webhook component with a standard envelope.** Rejected in ADR
  0014 decision 3 and re-rejected here: real services do not share a payload, so
  the envelope is a shim every connector bypasses.
- **The relay verifies the signature.** Rejected in ADR 0024 decision 4: it puts
  every provider's secret in the cluster. Re-verified here even for
  relay-delivered traffic, because the relay is a boundary, not an authority.
- **Trust the relay's `X-GitHub-Delivery` without verifying.** Rejected: a
  compromised relay could then inject an event with a forged delivery id.
- **In-memory dedupe.** Rejected: a restart replays redeliveries (ADR 0023
  decision 5).
- **Acknowledge before enqueueing (fire-and-forget).** Rejected: an enqueue
  failure would be silently lost, and the seen-set would be marked for work that
  never happened. The endpoint returns `5xx` and lets the provider retry.
- **Refuse an undeclared event kind.** Rejected: it makes the provider retry
  forever for an event the navi simply does not act on.
- **A per-provider endpoint that parses the payload before verifying.** Rejected:
  parsing untrusted input before its signature is checked is the vulnerability
  the verification exists to prevent.

## Open questions

1. **Signature schemes other than HMAC.** Some providers sign with Ed25519 or
   include a timestamp (replay window). The seam must admit them without
   assuming HMAC-SHA256.
## Related

- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credentials as references
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations and the injection boundary
- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — connectors are bidirectional; no generic payload
- [ADR 0019](./0019-portal-transport-bindings.md) — transport as a binding
- [ADR 0020](./0020-egress-tools-lifecycle-gated-connector-seam.md) — a missing credential disables the capability, not the navi
- [ADR 0021](./0021-principals-trust-and-policy.md) — the service principal inbound work acts as
- [ADR 0022](./0022-delegated-authorization.md) — the verified principal a portal presents
- [ADR 0023](./0023-connector-inbound-subscriptions-and-transport.md) — the record this amends; resolves its open question 4
- [ADR 0024](./0024-navi-relay.md) — the relay fabric; the navi verifies, the relay does not
- [ADR 0030](./0030-navi-ingress-two-listeners.md) — the navi's two-listener ingress; the websockets listener this record does not decide
