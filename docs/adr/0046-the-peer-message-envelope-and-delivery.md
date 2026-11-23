---
status: proposed
date: '2026-11-23'
supersedes: []
amends:
  - ./0031-navi-peer-network.md
  - ./0042-peer-discovery-relay-trust-and-ingress.md
tags:
  - adr
  - peer
  - p2p
  - protocol
  - delivery
  - envelope
---

# 0046 — The peer message: envelope, delivery, and the outbox

## Context

[ADR 0031](./0031-navi-peer-network.md) decided the peer network's transport,
identity, topology, and authorization, and its decision 7 stated the rule that
binds every message: **a point-to-point message is authorized by capability, not
by arrival.** The envelope "carries the sender's principal, and the receiver
checks a `send:<peer>` grant before accepting or mailboxing it." It named the
envelope and the delivery semantics as NAVI-412's to settle and did not settle
them.

[ADR 0042](./0042-peer-discovery-relay-trust-and-ingress.md) fixed the discovery
and trust shape around them — a signed, expiring presence assertion verified
against the querier's pinned trust store (decision 2); keys pinned out of band
or TOFU'd (decision 3); messages **sealed to the recipient's public key** so a
relay sees ciphertext (decision 4) — and left the envelope's "exact wire shape —
field set, expiry default, and whether a revocation list rides it" as its **open
question 1**, explicitly deferring it here.

What exists to build on, as of NAVI-629:

- The navi's **identity** is an Ed25519 keypair whose PeerID is a pure function
  of its public key (`navi/identity.go`, ADR 0031 decision 2).
- The **trust store** pins peers' public keys and resolves a principal
  (`navi/trust.go`, ADR 0021 decision 6).
- The **host** runs, with QUIC and TCP, one PeerID (`navi/peerhost.go`,
  ADR 0044).
- The **roster** is declared and renders as objects; the ceiling derives
  `send:<peer>` from it (`navi/peer.go`, `navi/policy.go`, ADR 0031 decision 7).
- The **connector inbound pattern** turns an external event into a thought
  (`connector/github/inbound.go`, ADR 0023), and the **egress tool pattern**
  exposes an action to a thought (`connector/egress.go`, ADR 0020).

What does **not** exist: the envelope, ACK/retry, dedupe, ordering, presence,
the store-and-forward outbox, and the navi-side connector. This record settles
the first six as design; NAVI-412 implements them.

**The constraints that shape it.**

- **A peer message is a capability, not an arrival** (ADR 0031 decision 7). The
  receiving navi checks `send:<sender>` before it accepts or stores.
- **The relay is not a trust authority** (ADR 0024 decision 5; ADR 0042
  decision 2). It forwards an opaque signed blob; it never re-signs, never
  edits, and never supplies a key.
- **Attribution is never author-supplied.** `thought.Thought.Origin` is
  "recorded when the thought is enqueued … never from a value the thought author
  supplied" (`thought/thought.go`). A peer's identity reaches `Origin` only if
  the message was verified first.
- **Delivery is durable** (ADR 0031 decision 8): the navi's datastore is its
  outbox, the relay's mailbox holds messages for an offline peer, end-to-end
  encrypted.
- **Presence is a hint, not authority** (ADR 0031 decision 9).

## Decision

1. **The envelope is a signed, expiring, self-contained record.** It carries:
   `id` (a unique message id, the dedupe and ACK key), `from` (the sender's
   principal reference), `to` (the recipient's principal reference), `sent_at`
   and `expires_at` (the clock-agreement pair ADR 0042's consequences make
   load-bearing), `re` (the id this message replies to, empty for a fresh
   message), `kind` (a small closed set — `message` now, `ack` and `presence`
   reserved), and `body` (the payload, opaque to the transport). The **signature
   covers the canonical encoding of every field but the signature itself**, so
   `id`/`from`/`to`/`expires_at` cannot be altered in flight without detection.
   The field set is closed and versioned: an unknown `kind` is refused, not
   ignored, because "ignore what you do not understand" is how a protocol grows
   a silent downgrade.

2. **The envelope is verified against the pinned key BEFORE anything is
   enqueued or stored.** The receiver resolves `from` through its trust store
   (ADR 0042 decision 3), checks the signature over the canonical bytes, and
   checks `expires_at` against its own clock. **Only a verified envelope may set
   `Origin`** — this is the one place the "never author-supplied" rule of
   `thought.Thought.Origin` meets a network peer, and the ordering is the
   guarantee: verify, then attribute, then enqueue. An envelope that fails
   verification is refused and recorded as a trust event (ADR 0027), never
   mailboxed and never attributed.

3. **Authorization is checked at receipt, by the receiving navi, against its own
   compiled policy.** After verification and before enqueue, the receiver
   evaluates `send:<from>` (ADR 0031 decision 7) as the sender's principal. A
   refusal is a refusal, not a drop: the sender is told (an `ack` with a refusal
   reason), because a silently discarded message is indistinguishable from a
   lost one and would make retry loops pathological. **Being listed is not
   permission and permission is not reachability** (ADR 0042 decision 5): a
   sender in the roster still needs the grant, and a sender with the grant still
   needs to be reachable.

4. **Delivery is at-least-once with idempotent receipt, and ACK is
   end-to-end.** A message is retried until the **recipient** acknowledges it —
   not the relay, which would let a relay's 2xx stand in for delivery. The
   recipient's `ack` names the message `id`; the sender clears the message from
   its outbox on that ack. **Dedupe is by `id`** and is the recipient's job: a
   redelivered message is recognized and its effect is not repeated, so
   at-least-once transport yields effectively-once handling without requiring
   exactly-once transport, which libp2p does not provide and ADR 0031 decision 8
   does not claim.

5. **Ordering is per-`re`-chain, never global.** The protocol guarantees no
   ordering between independent messages, because there is no ordering to
   guarantee across two paths and two clocks. It **does** preserve reply order
   within a `re` chain to the extent the recipient processes them, and the
   `sent_at` field is a fact for a reader, not a sequencing instruction. A
   consumer that needs ordering expresses it as a chain (each message `re`'s the
   previous), which is the same shape ADR 0022 decision 2 gives a thought's
   lineage.

6. **Retry is bounded and backed off; expiry is the outer bound.** A sender
   retries with exponential backoff and jitter up to a cap, and gives up at the
   envelope's `expires_at`, which the sender sets. **Expiry is what makes a
   stale message safe to drop**: a message that outlives its window is removed
   from the outbox and recorded, rather than retried forever against a peer that
   may never return. A default expiry (on the order of hours, not days) is a
   configuration value, not a protocol constant; the record fixes that there
   **is** one and that it is the sender's.

7. **Store-and-forward lives in the navi's outbox, and the outbox is a new
   datastore aggregate.** The navi's own pending outbound messages are persisted
   through the ordered writer (ADR 0011) so a message survives a restart between
   enqueue and ack. It is a **new aggregate** (`peer_outbox`) because the
   existing ones are thoughts, tool state, and memory, and none of them is a
   queue of outbound records. **The writer's key-coalescing is the wrong shape
   for it** (a keyed write *replaces*, `datastore/writer.go`) — an outbox
   appends and removes — so the outbox uses a **per-message key**
   (`peer_outbox/<id>`) where each write is that message's own state, and a
   removal is its own write, rather than one coalescing key for the whole
   mailbox. The relay's mailbox (ADR 0024, ADR 0042) is the *other* half and
   lives in the relay, not the navi.

8. **Presence is pull-based, matching the remote-pi mesh.** A navi learns who is
   online by asking (a query the relay answers from pushed assertions, ADR 0042
   decision 1) and by a heartbeat TTL (ADR 0031 decision 9), **not** by waking a
   thought on join or leave. Presence is a hint a sender uses to choose direct
   versus mailboxed delivery; it is never an authorization input, and a message
   is authorized when it arrives regardless of what presence said. This mirrors
   the remote-pi mesh deliberately: a join/leave that wakes a turn makes a
   presence flap into work, which is the wrong coupling.

9. **Payloads are sealed end-to-end to the recipient's public key.** The
   envelope's `body` is encrypted to the recipient (ADR 0042 decision 4) before
   it reaches any relay or mailbox, so a middleman sees ciphertext and routing
   metadata only. A **direct** libp2p connection already has the Noise handshake
   (ADR 0031 decision 1) and needs no additional sealing for confidentiality —
   but the envelope is sealed anyway, so the **same message** works whether it
   travels direct or through a mailbox, and the relay never needs to be trusted
   with plaintext. The signature is over the sealed body, so integrity does not
   depend on the transport either.

10. **The navi side is a connector, and a peer message becomes a thought.**
    Following ADR 0014 (a navi-to-navi link is a bidirectional integration) and
    ADR 0023 (the inbound shape), the navi exposes an egress tool `navi_send`
    (ADR 0020) and an inbound that turns a verified, authorized envelope into a
    thought whose `Origin` is the peer's principal and whose `OriginInfo`
    records the peer transport. **This is the first connector to set `Origin`
    from a network peer** — the existing connectors leave it zero and carry the
    principal in `Metadata` — and the ordering of decision 2 is what makes that
    safe. The tool is a capability like any other: `navi_send` requires
    `send:<peer>` in the ceiling and a role grant, so a thought may only message
    a peer its composition permits.

## Consequences

- **At-least-once plus dedupe is the delivery model**, which is what the
  transport can actually provide. Exactly-once is not claimed and not needed:
  the recipient's dedupe is what makes a retry harmless.
- **A relay's ack is not delivery.** End-to-end ACK means the sender's outbox
  only clears when the recipient has it, so a relay that loses a message loses
  nothing the sender believes was delivered.
- **Expiry makes the outbox bounded.** Without it, an outbox against an
  unreachable peer grows forever; with it, a stale message is dropped and
  recorded.
- **The outbox is a new aggregate and a new migration.** It is the first
  aggregate added since the contract's three, and the writer's coalescing
  semantics do not fit it, so it is a real addition rather than a reuse.
- **Attribution is provable.** A thought's `Origin` for a peer message is
  backed by a verified signature, which is stronger than the `Metadata` a
  webhook connector carries — and it means the peer path cannot be spoofed by a
  connector that simply writes a ref.
- **Presence flaps do not become work**, because presence is pull-based and
  never wakes a turn.
- **The envelope is versioned and closed**, so a future field is a deliberate
  change with a compatibility decision, not a silent addition a peer ignores.

## Alternatives considered

- **Exactly-once delivery.** Rejected: it needs transport guarantees libp2p does
  not provide, and dedupe at the recipient gives the effect without the claim.
- **A relay-side ACK, with the sender clearing on the relay's 2xx.** Rejected:
  it lets the relay's success stand in for the recipient's, which is precisely
  the authority ADR 0024 decision 5 denies the relay.
- **Global ordering via sequence numbers.** Rejected: two paths and two clocks
  make a global sequence a fiction; per-chain ordering is the honest subset and
  matches a thought's lineage.
- **Reusing the writer's coalescing key for the outbox.** Rejected: coalescing
  *replaces* a pending write, so a mailbox keyed as one unit would collapse
  distinct messages into the last one written.
- **Presence as push, waking a thought on join/leave.** Rejected: a presence
  flap becomes work, and presence is not authority (ADR 0031 decision 9).
- **Plaintext body on a direct connection, sealed only through a mailbox.**
  Rejected: it makes the same message two different shapes depending on its
  path, and forces the relay to be trusted for the mailboxed case.
- **A revocation list riding the envelope** (ADR 0042 open question 1).
  Deferred, not rejected: revocation is the trust store's concern (ADR 0042
  decision 11) and a per-message list would duplicate it; if it is needed it
  belongs in the assertion, not every message.

## Open questions

1. **The exact retry schedule** — base interval, cap, and whether jitter is
   required by the protocol or left to the implementation.
2. **The default expiry value**, and whether a deployment may set it per-peer
   rather than per-navi.
3. **Whether `presence` is a reserved `kind` on the envelope or a separate
   libp2p protocol** — it is listed as reserved here because it is a message,
   but a heartbeat topic (ADR 0031 decision 9) may be cheaper.
4. **Whether the outbox is exposed as an object type** so an operator can see
   what is pending, or stays internal.
5. **Whether a group/multicast message is one envelope with a group `to` or N
   envelopes**, which decides whether fan-out is the sender's or the relay's.

## Related

- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — the datastore the
  outbox is a new aggregate of
- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — the
  connector shape the navi side takes
- [ADR 0020](./0020-egress-tools-lifecycle-gated-connector-seam.md) — the egress
  tool `navi_send` follows
- [ADR 0021](./0021-principals-trust-and-policy.md) — the principal and trust
  store the envelope is verified against
- [ADR 0023](./0023-connector-inbound-subscriptions-and-transport.md) — the
  inbound shape a peer message becomes a thought through
- [ADR 0024](./0024-navi-relay.md) — the relay that must not be a trust authority
- [ADR 0027](./0027-attribution-records.md) — the trust event a refusal is
  recorded as
- [ADR 0031](./0031-navi-peer-network.md) — the peer network, decisions 7-9 this
  implements
- [ADR 0042](./0042-peer-discovery-relay-trust-and-ingress.md) — the trust and
  sealing model, whose open question 1 this settles
- [ADR 0044](./0044-the-peer-host-declaration.md) — the host the protocol runs on
- NAVI-412 — the increment this scopes
