---
status: draft
created: '2026-10-14'
updated: '2026-10-14'
tags:
  - design
  - peer
  - p2p
  - topology
---

# The navi peer network — shapes

> Illustrative shapes for the peer network decided in
> [ADR 0031](../adr/0031-navi-peer-network.md). The ADR holds the decisions; this
> note holds the pictures: what connects to what, over which transport, with which
> identity, and where durability and the relay sit. It is deliberately concrete so
> a deployment can be reasoned about before it exists.

## Building blocks

- **Real-time surface — two bindings, one stream.** A **direct binding** (HTTP/WS) that
  needs no p2p at all, and a **p2p binding** (the navi's libp2p host, including
  libp2p's browser transports). Both carry the same portal stream (ADR 0019).
- **Webhooks listener** — third-party provider triggers, separate from the real-time
  surface (ADR 0029).
- **Peer identity** — the navi's Ed25519 principal becomes the libp2p **PeerID**.
- **Discovery** — opt-in, off by default.
- **Relay role** — rendezvous + optional circuit relay + optional store-and-forward
  mailbox; hosted by a dedicated service **or** a navi.
- **Tiers** — a **team** of leaf navis (tier 0) floodsubs among itself; **relay navis**
  (tier 1) bridge teams.
- **Communication** — **streams** (point-to-point) and **pubsub** (topics: presence,
  notices, groups), with **store-and-forward** for durability.
- **Security** — connection gating (PeerID + fleet PSK), topic ACLs from the permission
  system, signed messages, and an encrypted mailbox.

## Shape 1 — Private single navi with a remote TUI

```
        ┌──────────────────────────────┐
        │  Remote TUI (operator)       │
        │  OIDC bearer / signed request│
        └───────────────┬──────────────┘
                        │  DIRECT binding  (HTTP/WS)
                        ▼
   ┌─────────────────────────────────────────────────┐
   │  Navi "home"   (no p2p, no relay, no discovery)  │
   │   real-time surface                              │
   │    • direct binding  ◀── the TUI                 │
   │    • p2p binding      ── OFF                     │
   │   webhooks listener (optional)                   │
   │   core: queue · tools · connectors · datastore   │
   └─────────────────────────────────────────────────┘
```

Nothing p2p is involved: the TUI talks to the direct binding, identity comes from a
trust source, and the p2p binding is off. This is the zero-config case, and turning
p2p on later is additive.

## Shape 2 — Private fleet with Discord communication

```
                        Discord (cloud)
     ┌─────────────────────────────────────────────────┐
     │ interactions (POST, Ed25519)   gateway (WSS)     │
     └───────┬─────────────────────────────────┬───────┘
             │ in                              │ out (navi dials)
             ▼                                 ▲
   ┌─────────────────────────────────────────────────────┐
   │ Navi A   (tier 0, private)                          │
   │   webhooks listener  ◀── interactions               │
   │   discord connector  ──▶ gateway (outbound WS)      │
   │   egress tools       ──▶ Discord REST (bot token)   │
   └───────────────┬─────────────────────────────────────┘
                   │  libp2p · team floodsub (team/alpha/*)
        ┌──────────┴───────────┐
        ▼                      ▼
   ┌───────────────┐   ┌───────────────────────────────┐
   │ Navi B (t0)   │◀─▶│ Relay navi (tier 1)           │
   └───────────────┘   │  discovery ON · rendezvous ·  │
                       │  circuit relay · mailbox      │
                       └───────────────┬───────────────┘
                                       │ libp2p · relay net (fleet/*)
                                       ▼
                               ┌───────────────────┐
                               │ Team beta relay   │
                               └───────────────────┘
```

Internal traffic is p2p; Discord is the fleet's external channel in three directions
(interactions in via the webhooks listener, gateway outbound, REST egress). One navi's
Discord presence can serve the team by broadcasting on a topic. An offline peer is
served by the relay's mailbox.

## Shape 3 — Public navi peered to a private fleet

```
   ┌──────────────────────────────────────────────┐
   │  Public navi (discovery ON, reachable)        │
   │   relay role: rendezvous · circuit relay ·    │
   │   mailbox · allow/deny: fleet PeerIDs + PSK   │
   └───────▲───────────────────────┬───────────────┘
           │ (1) register/dial out │ (2) rendezvous → hole-punch
           │  (private side has    │ (3) fallback: circuit relay
           │   NO inbound needed)  │
   ┌───────┴───────────────────────▼───────────────┐
   │  Private fleet relay (tier 1, behind NAT)     │
   └───────┬───────────────────────────────────────┘
           │ team floodsub
   ┌───────▼───────┐        ┌───────────────┐
   │ Leaf navi 1   │        │ Leaf navi 2   │
   └───────────────┘        └───────────────┘
```

The public navi is a navi that turned discovery on and is reachable; it hosts the relay
role. The private fleet dials out — no inbound reachability — then hole-punches for a
direct connection, falling back to the circuit relay. The public navi is a full peer,
not just plumbing.

## Shape 4 — Relay service + remote TUI + private fleet

```
   ┌──────────────────────────────┐
   │  Remote TUI (operator)       │
   │  device key → user principal │
   └───────────────┬──────────────┘
                   │ dial out
                   ▼
   ┌──────────────────────────────────────────┐
   │  Relay SERVICE  (public, dedicated)      │
   │   rendezvous · circuit relay · mailbox   │
   └───────────────▲──────────────────────────┘
                   │ dial out — the fleet needs NO inbound
   ┌───────────────┴──────────────────────────┐
   │  Private fleet (tier 0)                  │
   │   p2p binding ON, discovery OFF          │
   │   team floodsub + streams                │
   └──────────────────────────────────────────┘
```

Same role as Shape 3's relay navi, but a dedicated service. Both the TUI and the fleet
dial out; the relay is the meeting point. The TUI is a portal on the p2p network, so it
reaches the fleet even when the fleet is unreachable.

## Shape 5 — Relay service, Discord, Forgejo, Huly, and a private fleet

```
        ┌──────────┐    ┌───────────┐    ┌──────────┐
        │   Huly   │    │  Forgejo  │    │ Discord  │   ← providers
        └────┬─────┘    └─────┬─────┘    └───┬──▲───┘
             │ egress         │ egress       │  │ gateway (dial out)
             │                │ webhook      │  │
             │                ▼              ▼  │
             │     ┌───────────────────────────────┐
             │     │  Relay SERVICE (public)       │
             │     │   webhook ingress             │
             │     │   rendezvous · relay · mailbox│
             │     └───────────────▲───────────────┘
             │                     │ dial out
             │     ┌───────────────┴───────────────┐
             └────▶│  Private fleet (tier 0)       │
                   │   navis with provider         │
                   │   connectors + own identities │
                   │   p2p: streams + team floodsub│
                   └───────────────▲───────────────┘
                                   │ p2p (or direct, same network)
                   ┌───────────────┴───────────────┐
                   │  Human team: TUI + web tool   │
                   └───────────────────────────────┘
```

The relay's webhook ingress routes provider events to the right navi
(`/webhooks/<provider>/<navi>`); the **navi verifies** (HMAC for Forgejo/GitHub,
**Ed25519** for Discord interactions). Egress goes directly from the navi to the
provider with its own credential. Navis coordinate internally over p2p.

## Shape 6 — Homogeneous fleet, navi-as-a-user

```
   Providers — every navi has its OWN identity in EVERY service
   ┌──────────────────────┐ ┌──────────────────────┐ ┌──────────────────────┐
   │ Huly                 │ │ GitHub / Forgejo     │ │ Discord              │
   │  user per navi       │ │  machine user/navi   │ │  bot per navi        │
   └──────────┬───────────┘ └──────────┬───────────┘ └──────────┬───────────┘
              │                        │                        │
              ▼                        ▼                        ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │  Relay SERVICE (public)                                                │
   │   webhook ingress /webhooks/<provider>/<navi> · rendezvous · relay ·    │
   │   mailbox                                                              │
   └──────────────────────────────────▲─────────────────────────────────────┘
                                      │ dial out
   ┌──────────────────────────────────┴─────────────────────────────────────┐
   │  Private fleet (tier 0) — every navi composes every connector          │
   │   Navi A: Huly-A · GH-A · Discord-A                                    │
   │   Navi B: Huly-B · GH-B · Discord-B                                    │
   │   Navi C: Huly-C · GH-C · Discord-C                                    │
   │   p2p: streams + team floodsub                                         │
   │   declared: navi principal ↔ {Huly user, GitHub user, Discord bot}      │
   └──────────────────────────────────▲─────────────────────────────────────┘
                                      │ p2p (or direct)
   ┌──────────────────────────────────┴─────────────────────────────────────┐
   │  Human team: TUI · web orchestration tool                              │
   │   every navi is addressable — @mention, assign, request review          │
   └────────────────────────────────────────────────────────────────────────┘
```

Any navi can serve any user, so failover is trivial; the fleet must coordinate to avoid
duplicate work. The declared identity map is N×M and should be displayable.

## Shape 7 — Job-title fleet

```
   ┌────────────────────────────────────────────────────────────────────────┐
   │  Private fleet (tier 0) — one navi per job title                       │
   │                                                                        │
   │   team-lead    prompt: plan · review · assign                          │
   │                tools: planning/review tools   perms: triage/review     │
   │                                                                        │
   │   infra-eng    prompt: infrastructure operator                          │
   │                tools: deploy · infra · shell(infra)  perms: infra repos │
   │                                                                        │
   │   backend-eng  prompt: backend engineer                                 │
   │                tools: code · PR · tests        perms: backend repos     │
   │                                                                        │
   │   frontend-eng · qa-eng · …                                             │
   │                                                                        │
   │   every navi: every connector, its own identity in each service         │
   │   p2p: streams (asks) + team floodsub (presence/notices)               │
   │   claims: assign-to-self in Huly  ·  p2p heads-up                      │
   └──────────────────────────────────▲─────────────────────────────────────┘
                                      │ p2p (or direct)
   ┌──────────────────────────────────┴─────────────────────────────────────┐
   │  Human team: TUI · web orchestration tool                              │
   │   assign an issue to a navi → it picks it up (Huly inbound)            │
   └────────────────────────────────────────────────────────────────────────┘
```

**The job is the composition, not a field.** A navi's job title is its **name and
system prompt** (how it operates), sitting beside its **tool set** (ADR 0006) and its
**permission groups** (ADR 0021). The system prompt shapes behaviour; the permission
groups shape access; the tool set shapes capability. Three separate axes, and no
`role:` configuration key.

**A day in this fleet:** a human assigns NAVI-418 to `backend-eng` in Huly → its Huly
inbound turns the assignment into a thought → it works the issue and opens the PR as its
own machine user → p2p-asks `infra-eng` for a deploy, or assigns a verification
sub-issue to `qa-eng` → `team-lead` reviews. Every action is attributed to the navi that
did it, in Huly, GitHub, and Discord.

## What is common to every shape

- The **real-time surface has two bindings** (direct = no p2p; libp2p = fleet), and the
  **webhooks listener is separate**.
- **Identity is the Ed25519 principal → PeerID**; users are principals too.
- **The relay is a role** — navi-hosted or dedicated — and never required for a single
  navi.
- **Discovery is off by default.**
- **Claims are the board**; p2p is the courtesy heads-up.
