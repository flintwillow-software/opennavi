---
status: proposed
date: '2026-10-14'
supersedes: []
amends:
  - ./0024-navi-relay.md
  - ./0030-navi-ingress-two-listeners.md
tags:
  - adr
  - peer
  - p2p
  - transport
  - discovery
  - pubsub
  - security
---

# 0031 — The navi peer network: libp2p, tiers, and the relay as a role

## Context

ADR 0024 gave the fleet one relay for webhook ingress and navi-to-navi routing and left
the peer protocol to NAVI-412. ADR 0030 decided the navi's ingress as two listeners and
named the websockets listener as the first peer endpoint. That endpoint is too narrow:
the peer network needs identity, discovery, NAT traversal, group communication, and
durability, and those are precisely what **libp2p** provides.

The constraints that shaped this record:

- **Fleets are not on a mesh by default.** Some navis share a network; others run in
  cloud VPCs or behind home NAT. Discovery and NAT traversal are real requirements, not
  optional polish.
- **Dependency weight is acceptable for the hard parts.** Crypto, NAT traversal,
  discovery, and multiplexing are the shoulders to stand on; the navi's own envelope,
  authorization, and store-and-forward are hand-rolled because they are easy and
  because they are ours.
- **The relay is additive, not required.** Discovery is opt-in, and a navi may host the
  relay role itself. A single navi must work with no relay at all.
- **A navi is a user, not a service.** Each navi is its own identity in each service it
  touches, so its work is attributed and its access is scoped to its job.
- **Topics must be securable.** libp2p pubsub has no authorization; the navi's existing
  permission system is the authorization.

## Decision

1. **The peer transport is libp2p.** Each navi runs a libp2p host — identity, QUIC/TCP
   transports, Noise, yamux, `ConnectionGater`, `pnet`, circuit relay v2, DCUtR,
   rendezvous, and pubsub. The hard, well-tested parts are adopted; the navi's message
   envelope, topic authorization, and durable delivery are its own.

2. **The peer identity is the navi's principal key.** The navi's Ed25519 principal
   (ADR 0021) becomes the libp2p **PeerID** — there is no second identity system. A peer
   is authenticated by its key at the transport, and the trust store maps the PeerID to
   a principal. Users authenticate the same way they do today (OIDC, or a device key
   attached to their principal).

3. **Discovery is opt-in and off by default.** A navi does not advertise unless it
   declares discovery on. Intent is explicit, so nothing is discoverable by accident —
   the fail-closed direction.

4. **The relay is a role, not a deployment shape.** The role is **rendezvous + optional
   circuit relay + optional store-and-forward mailbox**. It may be hosted by a
   dedicated service (`cmd/navi-relay`, still first-class for webhook ingress and for
   fleets with no reachable navi) **or by a navi that opts in**. The peer path is
   **direct by default** — hole-punched with DCUtR — with the relay as the rendezvous
   and the fallback.

5. **Tiers keep the broadcast domain small.** A **team** of leaf navis (tier 0) runs
   floodsub among itself; **relay navis** (tier 1) bridge teams. A federation tier is
   **not** built until evidence demands it — Aptos is deprecating its middle tier
   (AIP-139), so the caution is real. Tiers are declared at composition (ADR 0013), not
   discovered.

6. **Communication is streams and pubsub.** **Streams** are the point-to-point path:
   private, request/response, and delivery. **Pubsub** is for topics — presence, fleet
   notices, group addressing. **Floodsub first**: gossipsub is designed for large,
   geodistributed networks and is explicitly not recommended for small ones, so it is
   adopted only if the fleet outgrows floodsub. Topic scope matches the tiers:
   `team/<id>/*` floods within a team; `fleet/*` is bridged relay-to-relay.

7. **Topics are authorized by the permission system.** The navi enforces a **declared**
   `topic → {publish, subscribe}` ACL against the peer's principal, reusing roles and
   groups (ADR 0021). libp2p's `StrictSign` binds each message to its sender, so a
   message cannot be spoofed. Connection gating (PeerID + fleet PSK) is the coarse
   layer; topic ACLs are the fine layer; group-key encryption is an optional
   confidentiality layer for sensitive topics, not the default.

8. **Delivery is durable: store-and-forward.** libp2p provides none — circuit relay v2
   relays a live stream and does not buffer, and pubsub is best-effort. The navi's
   datastore (ADR 0011) is its **outbox**; the relay's **mailbox** holds messages for an
   offline peer and delivers on reconnect, **end-to-end encrypted** so the mailbox sees
   ciphertext only. A message is cleared on ack and deduped on its id.

9. **Presence is the rendezvous TTL plus a heartbeat topic.** A peer that stops
   refreshing its registration disappears; a heartbeat topic gives liveness within a
   team. Neither is authority — presence is a hint, and a message is still authorized
   when it arrives.

10. **Portals ride the peer network, and the direct binding stays.** The navi's
    real-time surface has **two bindings** serving the same portal stream (ADR 0019): a
    **direct binding** (HTTP/WS) that needs no p2p at all — a single navi, a LAN, a
    browser on the same host — and a **p2p binding** (the libp2p host, including
    libp2p's browser transports: WebTransport first, WebSocket as the compatibility
    floor). A remote portal is a libp2p peer; a local one is a direct client. This is
    what makes the web orchestration tool work for both a fleet and a lone navi.

11. **A navi is a user in every service, scoped to its job.** Each navi is its own
    identity in Huly (a user), in GitHub/Forgejo (a **machine user** by default; a
    GitHub App is an opt-in configuration where short-lived tokens matter more than
    being addressable), and in Discord (its own bot/application, so users can mention it
    individually). Every navi composes every connector; what differs is its **job**,
    which is three separate axes and **not a configuration field**: the **system prompt**
    (how it operates), the **tool set** (what it can do — ADR 0006's compile-time
    omission), and the **permission groups** (what it may reach — ADR 0021). The
    provider accounts are scoped to match, so a compromised navi's blast radius is its
    job's access, at the navi and at the provider.

12. **Work claims are the board, not the mesh.** A navi claims work by **assigning the
    issue to itself** in Huly; **p2p is a courtesy heads-up** so two navis do not start
    the same work in the gap. A human may **assign an issue to a navi**, which the navi
    picks up: the assignment becomes a thought through **Huly inbound** (the ADR 0023
    shape, tracked separately).

## Consequences

- **NAT traversal, discovery, and multiplexing stop being our problem.** libp2p's
  transports, Noise, yamux, circuit relay v2, and DCUtR are adopted rather than built.
- **The peer network is heavier than the navi has been.** go-libp2p pulls a large
  indirect dependency set even with one transport enabled (a known upstream issue), so
  the dependency closure grows in a way the rest of the navi has been careful about
  (ADR 0004). This is a deliberate trade for the hard parts.
- **A navi with no p2p still works.** The direct binding is the default; discovery off
  means a lone navi is exactly what it is today.
- **The relay becomes optional infrastructure.** A fleet can start with a relay navi or
  a dedicated relay and later drop to direct connections as hole-punching succeeds.
- **Attribution improves at the source.** Because each navi is its own user, the
  provider's audit log answers "which navi" — matching ADR 0027 — and users can mention
  or assign a specific navi.
- **The credential surface is per-navi and per-role.** Onboarding a navi means
  provisioning its accounts in each service; that is the named cost, mitigated by
  role-scoped accounts and the trust ledger.
- **Two new pieces are on the critical path.** **Huly inbound** (assignment → thought)
  is required for "assign an issue to a navi and have it picked up"; and the **topic ACL
  declaration** reuses roles/groups but needs a shape.
- **The peer protocol's delivery semantics remain NAVI-412's.** This record fixes the
  transport, identity, discovery, topology, and authorization; ACK, retry,
  store-and-forward internals, presence semantics, and the clock agreement for
  timestamped signed messages are NAVI-412's.

## Alternatives considered

- **A custom QUIC protocol, no libp2p.** Lighter and fully ours, but we would rebuild
  identity binding, NAT traversal, hole punching, discovery, and multiplexing — the
  parts libp2p has already hardened. Rejected: this is the standing-on-shoulders case.
- **Mesh-first (WireGuard/Tailscale) as the assumption.** Removes NAT traversal, but
  fleets are not on a mesh by default (cloud VPCs, home NAT), and it would make
  reachability a deployment prerequisite. Rejected.
- **The relay as the normal path.** Simplest, but centralizes traffic and makes a
  private fleet depend on reachable infrastructure. Rejected in favour of
  direct-by-default with the relay as rendezvous/fallback.
- **gossipsub from the start.** Designed for large networks; explicitly not recommended
  for small ones. Deferred, not rejected.
- **A per-navi service account.** Simpler to provision, but no per-navi attribution and
  a fleet-wide blast radius. Rejected in favour of navi-as-a-user.
- **A role configuration field.** A field would duplicate what composition already
  expresses — the prompt, the tool set, and the permission groups — and would invite
  drift. Rejected: the job is the composition.

## Open questions

1. **The topic ACL declaration shape** — `topic → {publish, subscribe}` on the navi,
   narrowed at runtime, reusing roles/groups. Confirm the exact form.
2. **Whether the relay's mailbox is on by default** for a relay-enabled navi, or opt-in
   (it stores ciphertext, but it is state).
3. **The tier-1 relay topology** — how relay navis find each other (declared list vs
   rendezvous), and whether a team may have more than one relay.
4. **Peer message envelope** — the signed, timestamped message shape and the clock
   agreement NAVI-412 requires.
5. **Whether portals and peers share one libp2p protocol or use separate ones** (a
   portal stream vs a peer message), and whether a portal's libp2p identity is the user
   principal's device key.
6. **mDNS on a LAN** — whether local discovery is automatic (mDNS) or stays off with
   discovery.

## Related

- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — the tool set is the capability
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — the datastore as the outbox
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations
- [ADR 0019](./0019-portal-transport-bindings.md) — the portal stream the p2p binding carries
- [ADR 0021](./0021-principals-trust-and-policy.md) — the principal key that becomes the PeerID
- [ADR 0023](./0023-connector-inbound-subscriptions-and-transport.md) — the inbound shape Huly inbound reuses
- [ADR 0024](./0024-navi-relay.md) — the relay, now a role (amended)
- [ADR 0027](./0027-attribution-records.md) — attribution, improved by navi-as-a-user
- [ADR 0030](./0030-navi-ingress-two-listeners.md) — the ingress this amends (the peer transport is libp2p)
