---
status: accepted
date: '2026-11-15'
supersedes: []
amends:
  - ./0024-navi-relay.md
  - ./0030-navi-ingress-two-listeners.md
  - ./0031-navi-peer-network.md
tags:
  - adr
  - peer
  - p2p
  - discovery
  - relay
  - security
  - trust
---

# 0042 — Peer discovery through relays, and the trust model that makes it safe

## Context

[ADR 0031](./0031-navi-peer-network.md) decided the peer network's transport
(libp2p), identity (the navi's principal key), topology (tiers), and
authorization (capability grants), and left **discovery** as decision 3 — opt-in
and off by default — without deciding the mechanism. [ADR 0024](./0024-navi-relay.md)
decided the relay's role and posture: it authenticates and routes, **it does not
authorize**, and routing is declared at deploy time. [ADR 0030](./0030-navi-ingress-two-listeners.md)
decided the navi's ingress as two listeners and named a websockets listener as
the first peer endpoint — a decision made the day before libp2p was chosen, when
the peer transport was still unspecified.

[ADR 0041](./0041-go-libp2p-as-the-peer-transport-dependency.md) settled where
the libp2p dependency lives. Three things remain open, and they are entangled:
**how a navi finds its peers**, **what the relay is allowed to know**, and
**which listeners the navi actually runs**.

The motivating case is a **public navi** — one a customer or the public can
reach — which is the natural host for the relay role because it is already
reachable when the navis behind it are not. Its maintainer must be able to
control who can talk to it, and the fleet must be able to find each other
without a central authority deciding who exists.

**The constraints.**

- **The relay is infrastructure, not an authority.** It has no principal and
  holds no policy. A compromised relay must not be able to grant a permission
  or forge a peer.
- **Discovery must not leak existence by accident** (ADR 0031 decision 3).
- **The relay operator must be able to refuse.** A relay that cannot block a
  misbehaving navi is worse than one that can, so censorship is a supported
  capability rather than a vulnerability to be prevented.
- **Trust mutation is never a runtime path an agent can reach** (ADR 0006,
  ADR 0013, ADR 0021 decision 3).

## Decision

1. **Discovery is a query the relay answers from navi-pushed answers.** A navi
   that connects to a relay publishes what it is willing to be discovered as; a
   querier asks the relay, which answers from what navis told it. The relay
   **caches answers, it does not decide them** — correctness never depends on
   the cache being fresh, and a cold boot simply re-asks. On connect a navi
   pushes its assertion; a navi whose policy changes pushes an update. A relay
   MAY pull on query as a refresh, but the push is what keeps the common path
   warm.

2. **A presence assertion is signed by the publishing navi, and verified against
   the querier's pinned trust store.** The assertion carries the navi's
   reference, its published scopes, the permissions it advertises, and a
   timestamp with an expiry; the navi signs it with its principal key (ADR 0021,
   ADR 0031 decision 2). The relay stores and forwards the **opaque, signed
   blob** — it never re-signs and never edits. A querier verifies the signature
   against the key **it already holds**; it never accepts a key carried in the
   request or supplied by the relay, because a relay that could supply both the
   key and the assertion would prove nothing. This is the Tor v3 descriptor
   shape: the middleman is untrusted by construction.

3. **Key distribution is a pinned trust store, with TOFU for onboarding.** The
   primary path is provisioning a peer's public key out of band (config, the
   operator CLI, or the trust store's file), consistent with ADR 0013's
   declaration rule. Trust-on-first-use is the onboarding path for a public navi
   whose peers cannot be enumerated in advance: the first key seen for a
   reference is pinned, and every later use is verified against the pin. There
   is **no certificate authority** — an authority that signs keys would be the
   trust authority ADR 0024 decision 5 excludes.

4. **The message path is end-to-end encrypted; the relay sees an opaque blob.**
   A message to a peer is sealed to the recipient's public key before it reaches
   any relay, so a relay — and a store-and-forward mailbox, per ADR 0031
   decision 8 — sees ciphertext and routing metadata only. A **direct** libp2p
   connection uses the transport's own Noise handshake (ADR 0031 decision 1) and
   needs nothing further.

5. **Three security layers, each owned by the party that can enforce it.** The
   **network** layer (the relay's IP/CIDR allow and deny lists) decides whether
   a peer can reach the relay at all, and belongs to the relay operator. The
   **discovery** layer (a navi's published scopes, plus the relay's cached
   answer) decides what is listed to whom, and belongs to the publishing navi.
   The **authorization** layer (the receiving navi's compiled policy, ADR 0033)
   decides whether a given request is permitted, and belongs to the receiving
   navi. No layer can substitute for another: being listed is not permission,
   and permission is not reachability.

6. **Scopes bound the fan-out and reuse the peer vocabulary.** A navi publishes
   to a declared scope (`team/alpha`, `fleet`), reusing the namespace vocabulary
   of ADR 0031 decision 7 rather than inventing a second one. A query names a
   scope; the relay asks and lists only within it. A navi's choice of scope **is**
   its discoverability decision — the relay holds a label, not a rule, which is
   what keeps it from becoming a policy engine.

7. **Cross-relay communication is opt-in, per navi and per scope.** A navi that
   publishes only to its local relay is reachable only through it; publishing to
   a scope that bridges relays is what takes a navi from private, to a team, to
   public. Broadcast across relays is **not** the default, because a relay graph
   that fans out queries makes every navi's social graph legible to every relay
   in the path — encryption does not hide metadata.

8. **The relay role is a declared capability a navi can enable.** A navi enables
   the relay role at composition (the public navi is the natural host), and it
   authorizes what it will relay for with **its own compiled policy** — "who may
   I relay for" is an ordinary policy question, not a relay-specific ACL. A
   **dedicated relay service** is infrastructure: no principal, no policy, no
   authorization, a declared routing table (ADR 0024 decision 6), and ephemeral
   state. The role is a role, not a deployment shape (ADR 0031 decision 4).

9. **The relay may withhold, and the design tolerates it rather than preventing
   it.** An operator can refuse to list or to forward for a navi — the easy
   remedy for a misbehaving peer. This is a deliberate trade: the alternative, a
   relay that cannot refuse, is worse operationally. The design absorbs it
   because a relay's answer is a **hint**: a navi that knows a peer's address
   from its own configuration can still reach it directly or through another
   relay, and a withheld navi is not thereby unauthorized.

10. **One libp2p host per navi carries every peer and portal transport; ADR
    0030's separate websockets listener is superseded.** A navi runs a single
    libp2p host (QUIC and TCP for navi-to-navi, WebSocket for browser portals,
    WebTransport when ready) — one host, one PeerID, several transports. Portal
    traffic and peer traffic are **distinct protocols on that one host**, not
    separate listeners, so the identity, connection gater, and pubsub are
    configured once. The direct HTTP/SSE+POST portal binding (ADR 0019 decision
    4, ADR 0030 decision 1) remains for a local or LAN client that needs no p2p;
    what is superseded is a **separate websockets listener**, which libp2p's
    WebSocket transport now covers. The webhooks listener is unchanged.

11. **Trust mutation is a portal capability, gated by policy — never a tool.**
    Reading and writing the trust store (adding, rotating, revoking a peer's key)
    is exposed through the portal (TUI/web), where it is authenticated as a
    principal and authorized by the compiled policy like any other action. It is
    **not** a tool, so a thought cannot reach it: the ADR 0006/0013 rule becomes
    structural rather than a convention. "Operator" is a **role**, not a special
    case. A navi's **own** key cannot be revoked this way — a compromised key
    cannot be trusted to announce its own revocation — so that remains an
    out-of-band operator action, and the asymmetry is deliberate.

## Consequences

- **A relay cannot forge a peer or grant a permission.** It can only forward
  signed assertions and cached answers; verification is against the querier's own
  pinned keys.
- **Permissions are re-derived rather than stored authoritatively**, so a
  revocation takes effect at the next assertion rather than at a sync — with the
  cost that a navi's assertion must be re-pushed when its policy changes, or the
  cache is stale until its expiry.
- **Metadata is exposed and stated, not hidden.** A relay learns who talks to
  whom, how often, and when. This is the price of a relay and the reason
  cross-relay fan-out is opt-in.
- **Clock agreement becomes load-bearing.** A signed, expiring assertion is only
  as good as the agreement between signer and verifier, which ADR 0031 already
  names as NAVI-412's problem; this record gives it its first concrete consumer.
- **The relay is disposable.** Ephemeral state means a restart loses only
  warmth, not correctness, so it stays cheap to run and safe to redeploy.
- **Censorship is tolerated.** A relay can hide a peer; a navi with prior
  knowledge of an address can route around it. The design prefers a
  refusable relay to an unrefusable one.
- **One host means one identity surface.** Portal and peer traffic share a
  PeerID and a connection gater, so there is one place to reason about who is
  connected — at the cost that a portal client and a peer navi are gated
  together.

## Alternatives considered

- **The relay evaluates a per-requester discoverability rule.** Rejected: it
  makes the relay a policy engine duplicating the navi's IAM, which ADR 0024
  decision 5 explicitly refuses. Publishing to a **scope** gives the same control
  while leaving the relay holding a label.
- **A certificate authority for peer keys.** Rejected: an authority that signs
  keys is a trust authority, which the whole design excludes.
- **A DHT for key and peer lookup (the IPFS path).** Deferred, not rejected: it
  is a large subsystem, ADR 0031 defers it, and a relay-scoped directory answers
  the immediate need.
- **Unsigned presence assertions, with integrity left to the transport.**
  Rejected: a relay forwards to third parties, so transport integrity does not
  survive the hop; only an end-to-end signature does.
- **Self-registration at the relay (the remote-pi mesh shape).** Rejected for
  the infrastructure service, which has no policy to gate it (ADR 0024 decision
  6); **adopted** for a navi-hosted relay, which gates it with its own policy.
- **A separate websockets listener as ADR 0030 decided.** Superseded: once
  libp2p is the peer transport it brings its own WebSocket transport, and a
  second ingress path would be a second thing to secure for no gain.
- **A relay that cannot refuse to list.** Rejected: an operator must be able to
  stop a misbehaving peer without taking the relay down.

## Open questions

1. **The assertion's exact wire shape** — field set, expiry default, and whether
   a revocation list rides it or is fetched separately. Likely settles with
   NAVI-412's envelope.
2. **Whether a navi may publish to several relays at once**, and what that means
   for its assertion's consistency across them.
3. **Whether CIDR filtering lives in the relay or the cluster ingress.** Both
   can do it; doing it in two places invites drift.
4. **Whether the relay's cached answers are per-querier** (filtered by the
   scope the querier named) or a single shared directory keyed by scope.
5. **How a scope is authorized** — whether publishing to `team/alpha` is a
   `pub:` grant in the navi's own policy (ADR 0031 decision 7), which would make
   discoverability itself a capability.

## Related

- [ADR 0021](./0021-principals-trust-and-policy.md) — principals, keys, and the
  trust store this builds on
- [ADR 0024](./0024-navi-relay.md) — the relay's posture, amended here for the
  navi-hosted role
- [ADR 0030](./0030-navi-ingress-two-listeners.md) — the two listeners, whose
  websockets listener is superseded
- [ADR 0031](./0031-navi-peer-network.md) — the peer network, whose discovery
  decision this implements
- [ADR 0033](./0033-compile-and-inspect-the-policy-without-serving.md) — the
  compiled policy that authorizes every layer
- [ADR 0041](./0041-go-libp2p-as-the-peer-transport-dependency.md) — where the
  libp2p dependency lives
- NAVI-629 — the increment this scopes; NAVI-412 — the peer envelope
