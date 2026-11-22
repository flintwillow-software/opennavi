---
status: proposed
date: '2026-11-21'
supersedes: []
amends:
  - ./0041-go-libp2p-as-the-peer-transport-dependency.md
  - ./0042-peer-discovery-relay-trust-and-ingress.md
tags:
  - adr
  - peer
  - p2p
  - libp2p
  - composition
---

# 0044 — The peer host's declaration: its option, its listener, and its first transports

## Context

[ADR 0041](./0041-go-libp2p-as-the-peer-transport-dependency.md) decided that
go-libp2p lives in `navi/` and that the host is "built by a `navi.WithPeers(...)`
option and started by `serve`, following the `navi/webhook.go` `webhookListener`
precedent". It left three things open, and the first slice that implements the
peer declaration (NAVI-629 piece 2) turned two of them from hypothetical into
immediate:

1. **Which libp2p options the first host enables** (ADR 0041 open question 1) —
   transports, and whether `pnet` (a fleet pre-shared key) is on by default.
2. **Where the host's listener address comes from** (ADR 0041 open question 2) —
   declared like `WithWebhooks`, or derived.
3. **The option's name**, which ADR 0041 did not foresee would collide.

The collision is concrete. ADR 0041 decision 1 named the host option
`WithPeers(...)`. The merged peer-declaration slice then gave `WithPeers(...)` a
different job — declaring the **roster** — because ADR 0042 decision 5 separates
three layers that the ADR-0041 wording had folded together:

- **reachability** (which addresses a navi can dial) — a declaration;
- **discovery** (what a navi publishes to whom) — a scope, the publishing navi's;
- **authorization** (what a request may do) — the compiled policy, the receiving
  navi's.

"Declare the peers you connect to" reads as all three at once, and that reading
is exactly the one ADR 0042 refuses ("being listed is not permission, and
permission is not reachability"). So the roster and the transport must be two
options, and the record has to say which one keeps the name.

## Decision

1. **`WithPeers` is the roster, and keeps the name.** It declares the navis a
   navi knows how to reach — references, addresses, and published scopes — and
   it is a *declaration*, in the same class as a type or a tool. It starts
   nothing. This is what NAVI-629 piece 2 landed and what the ceiling derives
   `send:`/`pub:`/`sub:` from (ADR 0031 decision 7). Renaming it after it
   shipped would churn the public API and the reference template for a naming
   preference, and "peers" is the honest name for a roster.

2. **The host is a separate option, `WithPeerHost(addr)`.** Declaring it is what
   makes the declared peers *reachable* — it builds the libp2p host and starts
   it from `serve`. A navi may declare a roster and no host (the roster still
   derives capabilities and still renders on the object surface), and a navi may
   declare a host and no roster (it accepts connections with nothing pinned).
   Neither implies the other, which is the point: **listing is not
   reachability**, and folding them into one option would have said it was.

3. **The host's listener address is DECLARED, mirroring `WithWebhooks`.** The
   address is a string the author writes (`WithPeerHost("/ip4/0.0.0.0/udp/4001/quic-v1")`),
   the host binds it at Start, and a bind failure is a Start failure reported
   through the connector-health path like every other door. The alternative —
   deriving the address from the PeerID — was rejected because an address that
   cannot be written down cannot be provisioned, and an ephemeral port makes a
   navi unreachable in the one case (a public navi behind a relay) the peer
   network exists for. `BoundAddr()` reports the address actually bound, so
   port `0` remains usable in a test.

4. **The host is enabled by the ADDRESS, not by the roster.** `WithPeerHost("")`
   is no host — the fail-closed default — and declaring peers without
   `WithPeerHost` starts nothing. This keeps ADR 0031 decision 3's rule
   ("discovery is opt-in and off by default") true of *reachability* as well: a
   navi that declares a roster has not thereby opened a socket.

5. **The first host enables QUIC and TCP, and nothing else.** QUIC is the
   primary transport (ADR 0031 decision 1) and TCP is the compatibility floor;
   both carry the Noise handshake and yamux. **No `pnet` in the first host** —
   a fleet pre-shared key is a coarse second layer, and ADR 0031 decision 7
   already gives the fine layer (capability grants) and the connection gater;
   adding `pnet` before there is a fleet to key would make every navi need a
   shared secret to talk to itself. **No circuit relay, no DCUtR, no
   rendezvous, no pubsub** in the first host: ADR 0031 decision 6 defers
   floodsub, and relay/DCUtR are ADR 0024's and ADR 0042's subject, not the
   host's first increment. The first host listens, and that is all it does.

6. **One host carries every transport, per ADR 0042 decision 10.** The option
   takes one address in the first host; the record does not preclude a later
   form that declares several (QUIC + TCP + WebSocket on one PeerID), and
   ADR 0042 decision 10 already decided that shape. What this record fixes is
   that there is **one host**, one PeerID, and one connection gater — not a
   listener per transport.

## Consequences

- **The three layers stay separable in the API.** A navi can be listed without
  being reachable, reachable without being permitted, and permitted without
  being listed, and each is a different declaration. That is ADR 0042 decision 5
  expressed as composition rather than as prose.
- **`WithPeers` and `WithPeerHost` read as a pair**, which is what the ADR-0041
  wording wanted, without collapsing them.
- **The address is provisioning surface.** An operator sets it like a webhook
  address, and it appears in the health view; a public navi's address is a
  deployment fact rather than a derived one.
- **The first host is deliberately minimal.** No NAT traversal, no relay, no
  pubsub means a first host only works between navis that can reach each other
  directly — which is the LAN and same-host case, and enough for the NAVI-629
  verification bar (two navis, distinct PeerIDs, a connection in each roster).
- **A `pnet` decision is still owed** before a fleet ships with a shared secret;
  this record defers it rather than settling it.

## Alternatives considered

- **Keep `WithPeers` for the host (ADR 0041's wording) and rename the roster to
  `WithPeerRoster`.** Rejected: the roster shipped first, "peers" names it
  better than "peer host", and the host is the thing that needs the more
  specific name.
- **One option that declares both roster and host.** Rejected: it fuses
  reachability with listing, which ADR 0042 decision 5 separates on purpose.
- **Derive the listen address from the PeerID.** Rejected: unreachable by
  construction for a navi behind NAT or a relay, and unprovisionable.
- **Enable `pnet` by default in the first host.** Rejected: it would require a
  fleet-wide secret before there is a fleet, and it duplicates a layer the
  capability grants and connection gater already provide.
- **Enable circuit relay and DCUtR in the first host.** Deferred, not rejected:
  they are ADR 0024/0042's subject, and a host that listens is the increment
  this issue scopes.

## Open questions

1. **Whether the host option eventually takes several addresses** (QUIC + TCP +
   WebSocket) or one, given ADR 0042 decision 10's one-host-many-transports
   shape. Likely settles when the portal transport (WebSocket/WebTransport)
   joins the host.
2. **When `pnet` becomes the default** — if ever — and whether it is per-fleet
   or per-navi.
3. **Whether the host's PeerID is announced anywhere at startup** (a log line, a
   health-view field) so an operator can pin it out of band before TOFU.

## Related

- [ADR 0031](./0031-navi-peer-network.md) — the peer network; decision 7's
  capability vocabulary and decision 3's opt-in discovery
- [ADR 0041](./0041-go-libp2p-as-the-peer-transport-dependency.md) — where the
  dependency lives; this record settles its open questions 1 and 2
- [ADR 0042](./0042-peer-discovery-relay-trust-and-ingress.md) — the three
  layers (decision 5) and the one-host shape (decision 10)
- [ADR 0024](./0024-navi-relay.md) — the relay and NAT traversal this defers
- `navi/webhook.go` — the `webhookListener` precedent the host follows
- NAVI-629 — the increment this scopes
