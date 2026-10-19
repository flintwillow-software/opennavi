---
status: accepted
date: '2026-10-14'
supersedes: []
amends: []
tags:
  - adr
  - peer
  - p2p
  - service
  - sdk
  - discovery
  - security
---

# 0032 — Exposing services over the navi p2p transport

## Context

ADR 0031 gave the fleet a peer network: libp2p, the navi's principal as the PeerID,
topics, messages, and a relay role. What it did not decide is how a **service** — a
navi's own capabilities, or a third party's API — is exposed to peers and consumed by a
navi.

The motivating shape: a provider (GitHub, or one of ours such as sruth or smial) wants to
offer its operations **natively over the navi p2p transport** — a p2p service that
translates a call into its own API calls — so a navi pulls issue information over p2p
instead of over HTTP. Today a connector speaks a provider's HTTP API; the transport
should be swappable.

The constraints:

- The navi's interaction surface must stay **uniform** — a p2p service is consumed like
  any other connector (ADR 0014/0020), not through a new kind of component.
- **Capability is build-time** (ADR 0006/0013); nothing on the network may widen it.
- The caller's **identity** already exists (the principal, ADR 0021), and so does the
  **schema** (`tool.Def`).
- A service must be able to control its **own authorization**, because its backend is
  its own.

## Decision

1. **One service protocol, with declared operations.** A single versioned libp2p
   protocol ID (`/opennavi/service/1.0.0`) carries a call as an **operation name plus
   arguments**, and a service declares its operations as data — name, argument schema,
   result. Adding a service or an operation is a **declaration**, not a new wire
   contract: the gRPC shape, and the object surface's action shape. Operations reuse
   `tool.Def`, so a consumer maps them 1:1 to tools and there is **one schema**, not two.

2. **A p2p service is consumed by a connector.** On the navi side the client for a
   service is an ordinary **connector** (ADR 0014/0020) whose transport is the p2p
   protocol instead of HTTP: the tools keep their names and schemas, only the wire
   differs. No new component kind and no new interaction surface — a p2p service is as
   visible in `main.go` as an HTTP one.

3. **The provider SDK is standalone.** Exposing a service is a library plus a small
   server that speaks the protocol: declare operations, serve them, verify the caller,
   and hand the call to the backend. It must **not** require a navi — a service like
   GitHub, or one of ours, runs it as its own process.

4. **The caller presents its principal.** A call carries the caller's **principal** (the
   PeerID resolved through its key) in a signed, timestamped request, and the service
   verifies it with the caller's public key. This reuses the peer-message identity and
   the same clock agreement (ADR 0031, NAVI-412).

5. **Authorization is the service's own.** The SDK provides **identity and transport
   security**, not policy: what a call may do against a backend is the tool/connector
   implementation's decision, because the backend's authorization is the backend's. The
   navi's own policy engine governs a **navi provider's** exposure
   (`call:<service>.<op>`, ADR 0021); a third party brings its own.

6. **Request/response and streaming are both supported.** A service may answer a call,
   or stream a result and publish events/subscriptions; which it implements is the
   maintainer's choice. Streaming is what lets a p2p service cover the inbound half a
   webhook binding covers over HTTP.

7. **Discovery is a federated rendezvous network.** A service **registers** (opt-in)
   under a service name at one or more **p2p navi-relays** acting as rendezvous points,
   and a consumer queries them. We host relays that maintainers add to their peers, and
   anyone may host one and contribute — the network is **federated**, not a single
   directory. This is separate from ADR 0031's navi discovery, which stays off by
   default: a navi may consume services without being discoverable itself.

8. **Discovery informs; it never widens capability.** The discovery tool lists public
   services — name, provider, operations — and **explains to the user** how to add one.
   It does not add a connector, grant a capability, or change the navi's surface.
   Capability improvement is a **human act**: a rebuild and redeploy, or a config
   change. A navi may **propose** — explain the change, or open a PR against a
   repository it can reach — and may **learn** (memory, notes, thoughts), but it may not
   **effect** a capability change. The barrier is human review, deliberately, so a
   hostile rendezvous point or a hostile service cannot widen what a navi can do by
   offering it.

## Consequences

- **A provider can meet a navi on the navi's own transport**, and a navi consumes it
  like any connector — the same tools, the same catalog, the same policy.
- **The capability surface stays compiled in.** Discovery points at a provider and a
  human grants the credential; nothing on the network adds a tool.
- **A service owns its authorization**, so the SDK does not have to model every
  backend's policy, and a third party is not asked to run our engine.
- **Streaming services can push**, so a p2p service can cover the inbound half that a
  webhook binding covers over HTTP.
- **Rendezvous points are a federated dependency.** A consumer needs one it trusts (or
  a declared peer), and a provider must register somewhere reachable; trust in a
  rendezvous point is therefore a real question, not an afterthought.
- **The self-improvement barrier is explicit.** Learning is the navi's; capability is
  the operator's. A navi that could add its own tools — or accept a capability a
  stranger offered — would make the compiled ceiling meaningless (ADR 0006).

## Alternatives considered

- **One protocol per service.** More isolation per service, but every consumer must
  know each protocol and a new service becomes protocol plumbing rather than a
  declaration. Rejected in favour of one versioned contract.
- **A generic "call any discovered service" tool.** Convenient, but a
  runtime-widening capability: the model's surface would change with what is on the
  network, and a hostile rendezvous point could offer a service that widens the navi.
  Rejected (ADR 0014 decision 3, ADR 0013).
- **The SDK provides the policy engine to third parties.** Would make our engine a
  dependency of every backend, and would not fit a backend whose authorization is its
  own. Rejected: the SDK provides identity; the service provides policy.
- **A central service directory.** One operator, one point of failure, one trust
  anchor. Rejected for a federated rendezvous network.
- **Discovery that installs the connector.** Self-modifying capability. Rejected
  explicitly (decision 8).
- **HTTP-only services.** The status quo, and it stays supported; this record adds the
  p2p transport rather than replacing it.

## Open questions

1. **The rendezvous registration shape** — what a service record carries (name,
   provider, operations, version), its TTL, and how a consumer verifies a provider's
   claim (a signed record, presumably).
2. **The protocol's framing** — the concrete message shape and version negotiation on
   top of the one protocol ID.
3. **Trusting a rendezvous point** — whether a consumer may use only relays it
   declares, and how a relay's results are verified.
4. **The onboarding flow** — how a navi explains and a user grants access across a
   portal, and whether that flow is a tool or a portal view.
5. **Whether a navi provider is a service or a portal** — a navi exposing its tools
   over the service protocol vs over the object surface; the overlap is real and should
   be settled.
6. **Streaming semantics** — backpressure, resumption, and how a streamed service
   composes with a connector's inbound half.

## Related

- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — capability by omission; the barrier this makes explicit
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credentials as references
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations
- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — connectors, and no generic payload
- [ADR 0019](./0019-portal-transport-bindings.md) — the portal stream a navi provider may overlap with
- [ADR 0020](./0020-egress-tools-lifecycle-gated-connector-seam.md) — the connector/egress seam
- [ADR 0021](./0021-principals-trust-and-policy.md) — the caller identity and the policy engine
- [ADR 0031](./0031-navi-peer-network.md) — the peer network this rides on
