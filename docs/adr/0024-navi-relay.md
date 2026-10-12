---
status: proposed
date: '2026-10-11'
supersedes: []
tags:
  - adr
  - relay
  - transport
  - webhook
  - peer
  - security
---

# 0024 — The navi relay: one routing fabric for webhook ingress and navi-to-navi traffic

## Context

A navi fleet — the navi-dock workers, the services in the k8s cluster — needs
two kinds of inbound traffic: **provider webhooks** (GitHub now; Discord and
others later) and **navi-to-navi messages**. Both need an ingress; neither
should require every navi to be publicly reachable.

ADR 0023 made transport a per-connector binding, chose polling first, and named
webhook as the reachable binding — but left **endpoint ownership** open (its
open question 4). ADR 0021 and ADR 0022 make a navi a principal with a keypair,
and make a peer navi a principal (ADR 0021 decision 10). The deferred
peer-transport work has no transport at all.

The workspace already runs a relay with a public ingress in the cluster
(`infra/internal-tools/modules/remote-pi-relay`: nginx ingress + cert-manager +
a Deployment/Service) for the pi agent mesh. That is the pattern, and proof the
shape works — but it is a third-party service, not a navi-aware one.

This record decides the shared fabric both traffic classes route through. It
resolves ADR 0023 open question 4.

## Decision

1. **One relay, fleet infrastructure, not per-navi.** A single service with a
   single public ingress. Per-navi ingresses — N certificates, N deployments,
   N failure domains — are rejected.

2. **It carries two traffic classes.** Provider webhook ingress and
   navi-to-navi routing. The relay is the fleet's rendezvous point for both.
   They share the component, the ingress, and the identity model; they do
   **not** share delivery semantics.

3. **Webhook ingress is multi-service and multi-navi.** Routing is by provider
   plus destination — a path (`/webhooks/github/<navi>`) or a host
   (`<navi>.webhooks.flintwillow.software`). Adding a provider or a navi is a
   routing-table entry, not a new deployment.

4. **The relay is a dumb pipe for content.** For a webhook it forwards the raw
   body plus the signature headers; the **navi** verifies, so webhook secrets
   never live in the relay. The relay does not inspect, decrypt, or authorize
   payloads.

5. **A navi authenticates to the relay with its keypair**, by challenge–response,
   consistent with ADR 0021 (a principal has authenticators). The relay
   authenticates the connection and routes; it does **not** decide
   authorization — the receiving navi does.

6. **Routing is declared at deploy time; presence is runtime.** The set of
   navis and the destinations each owns are declared (terraform/config). A navi
   cannot register itself or a route the deployment did not declare; presence
   and liveness are runtime. This applies ADR 0013's declaration rule to the
   fleet's reachability surface.

7. **The two traffic classes get separate address spaces.** A webhook
   destination is `provider + navi`; a peer address is a navi principal.
   Conflating them would let a webhook path name a peer, or a peer address
   collide with a provider route.

8. **Peer delivery semantics are not the relay's.** ACK, retry,
   store-and-forward, presence, and group addressing belong to a companion
   record (tracked as NAVI-412), not to the webhook path.

9. **The architecture is recorded here; the deployment is internal-tools'.**
   The terraform module (ingress, image, replicas) lives in
   `infra/internal-tools`; the navi-facing contract — addressing,
   authentication, envelope — is this record, and the navi side implements the
   client.

## Consequences

- One ingress, one certificate, one trust boundary, and one routing table for
  the whole fleet.
- The relay is a **trust boundary** — it sees routing metadata and, for
  webhooks, the raw body and signature — but **not a trust authority**; navis
  authorize what they receive.
- The relay is a single point of failure unless replicated (open question).
- The navi side needs a client: a transport binding, or a connector (a
  navi-to-navi link is a connector, ADR 0014).
- Adding a webhook-using connector is now a routing-table entry plus that
  connector's own verification — not a new deployment, certificate, or ingress.
- The routing table is a fleet-level declaration, so "who may change it" becomes
  an operator question rather than a per-navi one.
- The two classes sharing one component means the peer protocol must be designed
  deliberately rather than grown out of webhook forwarding.

## Alternatives considered

- **Per-navi ingress.** N ingresses, N certificates, N deployments, N failure
  domains. Rejected.
- **Direct navi endpoints, no relay.** Requires every navi to be publicly
  reachable, which ADR 0023 decision 4 rejects. Rejected.
- **The relay verifies webhook signatures.** Puts every provider's secret in
  the cluster. Rejected: the secret belongs with the navi that acts on the
  event.
- **A webhook-only relay.** Simpler, but duplicates the transport for peer
  traffic and leaves the peer half needing its own ingress. Rejected: one
  fabric.
- **Runtime routing registration.** Lets a navi add a route the deployment did
  not declare — a runtime widening of the fleet's reachability surface.
  Deferred, not adopted: declared first.
- **The relay as an authorization point.** Would make the relay a policy engine
  duplicating the navi's IAM (ADR 0021/0022). Rejected: it authenticates and
  routes, nothing more.

## Open questions

1. **Replication and HA.** Is the relay a single replica with a fast restart, or
   replicated? What state, if any, must survive a restart?
2. **Presence and store-and-forward** for peer traffic — the companion record's
   subject (NAVI-412).
3. **Group and multicast addressing.**
4. **Routing-table tooling.** How the declared table is authored and validated
   (terraform, config, a CLI), and who may change it.
5. **Peer payload confidentiality.** Does the relay see peer message contents,
   or are they end-to-end encrypted between navis?
6. **The companion record's home.** The peer transport's delivery semantics may
   want their own ADR, and it may belong with the navi-to-navi connector rather
   than with the relay.

## Related

- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time declarations
- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — connectors are bidirectional integrations
- [ADR 0021](./0021-principals-trust-and-policy.md) — principals, trust, and policy
- [ADR 0023](./0023-connector-inbound-subscriptions-and-transport.md) — the webhook binding this resolves open question 4 of
