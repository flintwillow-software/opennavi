---
status: accepted
date: '2026-10-14'
supersedes: []
amends:
  - ./0019-portal-transport-bindings.md
  - ./0023-connector-inbound-subscriptions-and-transport.md
tags:
  - adr
  - ingress
  - transport
  - portal
  - connector
  - security
---

# 0030 — The navi's ingress: a webhooks listener and a websockets listener

## Context

A navi is reached by two kinds of traffic, and they are not alike.

**Third-party provider events** (GitHub, Forgejo, and the like) are *untrusted*:
the sender is not a principal of this navi, authenticity is a provider
**signature** over the raw body, and the shape is request/response — a burst of
short-lived requests, no persistent connection. ADR 0029 decided their
listener: connector-owned routes behind one shared **webhooks** listener with a
router, with the navi verifying the signature and the router as the events-log
choke point.

**Portals, real-time connectors, and peer navis** are *principals*: they present
a credential the navi resolves (ADR 0021/0022), and their shape is a long-lived,
bidirectional stream — ADR 0019's one stream per portal, carrying a bounded
snapshot, observation events, object deltas, and control results. ADR 0024 gave
the fleet one relay for webhook ingress and navi-to-navi routing, but left the
navi-side endpoint open.

Two accepted records constrain the answer. ADR 0019 decision 4 chose **SSE +
POST first** and deferred a websocket until "a named consumer needs a
low-latency upstream stream". ADR 0023 decision 3 made a connector's inbound
transport a per-connector binding, with **webhook and poll** as the set. The
consumers are now named — portals, real-time connectors, and peer navis — and
the navi's ingress shape has to be decided so NAVI-410 (the GitHub webhook
binding), the portal transport, and NAVI-412 (the peer transport) can implement
against it.

## Decision

1. **Two ingress surfaces, one per trust model.** A **webhooks listener** for
   third-party provider events — untrusted input, authenticated by a provider
   signature, connector-owned routes behind one router (ADR 0029). A
   **real-time surface** for principals and peers — authenticated as a verified
   principal (ADR 0021/0022), carrying a long-lived bidirectional stream over a
   **direct binding** (HTTP/WS) and, per ADR 0031, a **p2p binding** (the
   navi's libp2p host). They do **not** share a socket: a webhook cannot reach a
   portal or peer route, and a portal credential cannot satisfy a webhook. This
   is the separation ADR 0029 decision 1 states from the webhook side, made
   symmetric.

2. **Websockets are adopted for the stream.** ADR 0019 decision 4 deferred a
   websocket until a named consumer needed a low-latency upstream stream; the
   consumers are named — portals, real-time connectors, and peer navis — so the
   real-time surface carries ADR 0019's **one stream** — over the direct binding
   and, per ADR 0031, the p2p binding. The stream contract is
   unchanged: a frame is a `Surface` call or result, or an observation
   event/delta, never a new dialect. Websockets are a transport under that
   contract, not a second ABI. **SSE + POST remains a supported binding** (ADR
   0019 decision 4) — the stream is the contract, the transport is a binding,
   and a deployment chooses.

3. **Real-time connectors gain a websocket binding — and the direction is part
   of the declaration.** ADR 0023 decision 3's transport set grows from
   {webhook, poll} to {webhook, poll, websocket}, but there are two shapes and
   they are not the same thing:

   - **Inbound** — a service or connector connects *to* the navi's websockets
     listener and streams; the navi hosts the socket. This is the binding this
     record decides, and it is the portal endpoint; peers use the libp2p binding
     (ADR 0031).
   - **Outbound** — the connector dials *out* to a service's websocket (Discord's
     gateway is the case) and receives a stream on a long-lived connection the
     navi does not host. It is an inbound *source* like polling — push instead of
     pull — not a listener, and it does not use the websockets listener.

   Both are build-time declarations with the same subscription and the same
   persisted dedupe (ADR 0023 decisions 1 and 5); only the inbound shape uses
   the websockets listener. Polling stays the reachability-free fallback and the
   webhook listener the third-party-trigger binding; a connector may offer one
   or more, declared at composition.

4. **The peer transport is libp2p, carried by the real-time surface alongside
   the direct binding.** Navi-to-navi traffic terminates on the navi's **libp2p
   host** (ADR 0031), authenticated by the principal keypair (ADR 0021): peers,
   portals, and real-time connectors are libp2p peers. The navi must be able to
   **allow or deny** a peer — connection gating by PeerID plus a fleet PSK,
   with topic ACLs from the permission system — because who may connect is a
   trust decision, not an afterthought. Peer **delivery** semantics — ACK,
   store-and-forward, presence, group addressing, and the clock agreement
   timestamped signed requests need — belong to NAVI-412, not to this record.
   What this record fixes is that the peer endpoint is the **libp2p host**, not
   the portal websocket: the real-time surface keeps a **direct binding**
   (HTTP/WS, no p2p required, for a single navi or a LAN) and gains a **p2p
   binding**, both serving the portal stream (ADR 0019).

5. **The relay routes to both, and the address spaces stay separate.** ADR
   0024's relay forwards provider webhooks to the webhooks listener and peer
   traffic to the real-time surface, by its declared routing table (ADR 0024
   decisions 6 and 7). It is a dumb pipe for both (ADR 0024 decision 4): the
   navi verifies a webhook signature, and it authenticates a peer as a
   principal, regardless of which listener the relay reached.

6. **Each listener is declared and exposed independently.** A deployment may
   expose the webhooks listener publicly (or through the relay) while keeping
   the real-time surface on the tailnet or mesh, or the reverse; each has its
   own address, certificate, and exposure, and neither implies the other. This
   is what lets a navi receive provider events without exposing its portal or
   peer surface, and vice versa.

## Consequences

- **Portals and real-time connectors get a push transport.** ADR 0019 decision
  4's stated condition — a named consumer — is met, so the websocket is adopted
  rather than deferred. Low-latency upstream traffic no longer waits on a
  cursor poll.
- **The two trust models stay separate by construction.** A bug in one
  listener's routing cannot expose the other's surface, because they do not
  share one.
- **The navi has two ingress surfaces to declare, secure, and expose** — two
  addresses, two certificates, two things an operator must get right — and two
  the relay's routing table must reach.
- **A connector author now has three inbound transports** to choose from
  (poll, webhook, websocket). The choice stays a build-time declaration (ADR
  0023 decision 1), so the surface remains greppable in `main.go`.
- **NAVI-412 gets its endpoint without pre-deciding its delivery semantics.**
  The peer protocol is free to define ACK, retry, and presence on top of a
  principal-authenticated stream.
- **ADR 0019's SSE + POST binding is not removed.** The stream contract is
  unchanged and both bindings implement it; which one a deployment uses is its
  choice, and a future record can make websockets the default on evidence.
- **Two listeners mean two rate-limiting and backpressure stories**, not one;
  the webhook side's (ADR 0029 open question 1) and the stream's (ADR 0019's
  bounded drop-oldest) are separate.

## Alternatives considered

- **One listener with a router for both.** Cheapest to expose — one address,
  one certificate — but it puts signature-authenticated untrusted traffic and
  principal-authenticated portal/peer traffic behind one routing layer, which is
  exactly the conflation ADR 0029 decision 1 rejects. Rejected.
- **SSE + POST only, no websockets.** Rejected: the named consumers (portals,
  real-time connectors, peer navis) need a low-latency bidirectional stream,
  which is ADR 0019 decision 4's own stated condition for adopting a websocket.
- **A listener per consumer** (one for portals, one for real-time connectors,
  one for peers). Rejected: they all authenticate as verified principals and
  share one stream contract, so splitting them multiplies addresses and
  certificates for no trust-model gain.
- **Fold peer traffic into the webhooks listener.** Rejected: ADR 0024 decision
  7 keeps the two address spaces separate, and the trust models differ — a
  signature is not a principal.
- **A listener per connector** (for the websocket binding too). Rejected as in
  ADR 0029: it multiplies exposed ports and scatters traffic, which is what
  makes one events log and one stream hard.
- **Leave the ingress shape to each binding.** Rejected: three bindings
  (webhook, portal stream, peer) would each choose, and the navi would end up
  with an accidental shape rather than a declared one.

## Open questions

1. **Websocket protocol details.** The subprotocol, the framing, the handshake,
   and whether a credential arrives as a header at upgrade or as a first frame.
2. **Multiplexing.** One stream multiplexed per principal, or one connection per
   consumer (a portal, a real-time connector, a peer). ADR 0019's one-stream
   contract fits both; the wire shape differs.
3. **A real-time connector's binding declaration.** What a websocket
   subscription declares beyond the existing build-time `Subscription` — the
   service, the connection, the credential reference — and how it composes with
   the webhook and poll bindings' declarations.
4. **Backpressure and rate limiting for long-lived streams.** ADR 0019's bounded
   drop-oldest is the starting point; how a slow portal or peer is told, and
   whether the listener sheds load, is undecided.
5. **Whether websockets become the default portal binding** or coexist with SSE
   + POST indefinitely, and what evidence would decide it.
6. **Whether the websockets listener also serves an operator console** (a
   browser portal), or that stays a separate deployment concern.
7. **The peer network's shape** is decided in [ADR 0031](./0031-navi-peer-network.md)
   (libp2p, tiers, discovery opt-in, the relay as a role); its delivery semantics
   remain NAVI-412's.

## Related

- [ADR 0012](./0012-portals-attach-to-one-navi-stream.md) — portals attach to one navi stream
- [ADR 0019](./0019-portal-transport-bindings.md) — the portal transport this amends (websockets adopted; SSE + POST retained)
- [ADR 0021](./0021-principals-trust-and-policy.md) — principals, trust, and policy
- [ADR 0022](./0022-delegated-authorization.md) — the verified principal a portal or peer presents
- [ADR 0023](./0023-connector-inbound-subscriptions-and-transport.md) — the inbound transport set this extends with a websocket binding
- [ADR 0024](./0024-navi-relay.md) — the relay, which routes to both listeners
- [ADR 0029](./0029-webhook-inbound-binding.md) — the webhooks listener and the binding that serves it
- [ADR 0031](./0031-navi-peer-network.md) — the peer network this hands the peer transport to
