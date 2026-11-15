---
status: accepted
date: '2026-11-15'
supersedes: []
tags:
  - adr
  - peer
  - p2p
  - dependency
  - transport
---

# 0041 — go-libp2p is the navi's first heavyweight dependency

## Context

[ADR 0031](./0031-navi-peer-network.md) decision 1 adopts libp2p as the peer
transport and names the cost in its own Consequences:

> **The peer network is heavier than the navi has been.** go-libp2p pulls a large
> indirect dependency set even with one transport enabled (a known upstream
> issue), so the dependency closure grows in a way the rest of the navi has been
> careful about (ADR 0004). This is a deliberate trade for the hard parts.

This record makes that trade concrete and decides **where the dependency is
allowed to live**. ADR 0031 decided *that* libp2p is adopted; it did not decide
the packaging, and the packaging is what keeps ADR 0004's invariant intact.

**ADR 0004's actual rule** is not "no new dependencies" — it is that the
**component packages are dependency-light (stdlib-only)** and that implementations
are private. The repo enforces the *shape* of that in CI: the
`external-consumer` job builds a synthetic out-of-repo component and fails if its
**contract closure** — the transitive imports of `tool`, `thought`, `connector`,
`portal`, `model`, `skill`, `datastore`, `object`, and `principal` — reaches
`navi/` or `internal/`, or contains anything other than those contract packages
(`.github/workflows/ci.yml`, the NAVI-355 contract-purity check).

That check filters to `^github.com/flintwillow-software/opennavi/`. It polices
**our** packages leaking into each other; it does not police third-party
dependencies. So the question this ADR answers is not "does libp2p break the
guardrail" (it cannot, as long as it stays out of the contract packages) but
**which package may import it**, because the answer decides whether every navi
pays its cost or only those that declare peers.

**The measured cost.** With `go-libp2p v0.52.0` added to the root module:

| | Before | After | Delta |
|---|---|---|---|
| Go modules | 68 | 190 | **+122** |
| `go.mod` | — | — | +90 lines |
| `go.sum` | — | — | +199 lines |
| Bare binary, no listeners | 2.4 MB | 31.9 MB | **+29.5 MB** |

The binary figure is for `libp2p.New()` with **no** transports enabled; QUIC,
circuit relay v2, and pubsub add more. The contract packages' third-party
closure is **zero** today and this record keeps it zero.

**What the templates are.** `navi-standard`'s own doc comment says it is *"the
reference for what a COMPLETE navi looks like: every option the framework
offers, declared in one legible file"*, and that it *"deliberately does NOT
declare peer/P2P features"* only because ADR 0031 was unimplemented. Its "no
external services" promise is about **runtime** dependencies — local Ollama, no
cloud credentials — not about compiled-in capability: a peer host with discovery
off needs no external service either.

## Decision

1. **go-libp2p is imported directly by `navi/`**, the composition root, and by
   nothing else. The host is built by a `navi.WithPeers(...)` option and started
   by `serve`, following the `navi/webhook.go` `webhookListener` precedent (a
   navi-owned connector the author declares rather than a component the runtime
   discovers).

2. **No contract package may import it, transitively or otherwise.** `tool`,
   `thought`, `connector`, `portal`, `model`, `skill`, `datastore`, `object`,
   and `principal` stay stdlib-only. This is the invariant ADR 0004 protects and
   the one the NAVI-355 CI check enforces; the peer feature must not become a
   reason to relax it.

3. **The dependency is not build-tag-gated and is not a separate package.** A
   build tag would make the reference template's composition conditional — the
   opposite of a legible "every option in one file" — and a separate package
   would only move the import, since `navi-standard` would import it anyway.
   Every navi that links `navi/` carries libp2p. This is accepted as the cost of
   ADR 0031 being a *baseline* capability rather than a plugin.

4. **`navi-standard` declares `WithPeers`**, and its doc comment's "deliberately
   does NOT declare peer/P2P features" paragraph is updated in the same change —
   the comment is part of the reference's teaching value and must not describe a
   state that no longer exists.

5. **The dependency is pinned and audited like any other.** `go.mod` records the
   version; a bump is an ordinary PR whose diff is dominated by `go.sum`. The
   measured figures above are the baseline a reviewer compares against.

6. **This record supersedes nothing.** ADR 0004's rule stands unchanged — this
   ADR is the worked example of applying it to the first dependency heavy enough
   that "where does it live" needed saying out loud.

## Consequences

- **The peer transport gets the hard parts for free** (identity binding, NAT
  traversal, hole punching, multiplexing, discovery) rather than rebuilding them,
  which is ADR 0031's standing-on-shoulders case.
- **Every navi binary grows by ~30 MB and the module graph triples.** A navi that
  declares no peers still pays, because the dependency is compiled into the
  composition root. This is the named, accepted cost.
- **The contract layer is untouched.** An out-of-repo component author sees no
  new dependency, and the NAVI-355 check keeps it that way — which is the
  property that makes the cost tolerable.
- **The templates' `go.mod` files are unaffected** until a template imports the
  host; the nested modules pick the dependency up through their own `require`
  when they do.
- **`go.sum` churn becomes routine.** A libp2p bump touches ~200 lines of
  `go.sum` for a one-line version change; a reviewer reads `go.mod` and the
  measured figures, not the hash diff.

## Alternatives considered

- **A build-tag-gated host (`//go:build libp2p`).** Rejected: it makes the
  reference template's composition conditional, so the "complete navi" would
  depend on how it was compiled, and a peerless default build would silently
  drop a declared feature.
- **A separate `navi/peer` package so peerless navis skip it.** Rejected after
  the operator's direction: `navi-standard` is the full-fat reference and
  imports it regardless, so the split buys nothing while adding a package
  boundary and an import the template must remember.
- **A lighter custom QUIC protocol.** Rejected in ADR 0031: it rebuilds exactly
  the parts libp2p has hardened.
- **Vendoring libp2p.** Rejected: it trades a `go.sum` diff for a vendored tree
  that must be kept in step by hand, with no benefit at this size.
- **Deferring the dependency by shipping identity only** (NAVI-629 slice 1,
  already merged). Rejected as a *stopping point*, not as a step: the identity
  needs no dependency, but the transport does, and stopping there leaves the
  peer network unimplemented.

## Open questions

1. **Which libp2p options are enabled in the first host** — transports (QUIC
   first, TCP as the floor), and whether `pnet` (a fleet pre-shared key) is on by
   default or opt-in.
2. **Whether the host's listener address is declared like the webhook listener's
   (`WithWebhooks`) or derived** — ADR 0030 named the websockets listener as the
   first peer endpoint, but libp2p brings its own QUIC/TCP listener.
3. **How a libp2p bump is reviewed** — whether the repo wants a
   dependency-audit step (license, CVE) beyond the ordinary PR, given the size of
   the closure.

## Related

- [ADR 0004](./0004-component-packages-public-implementations-private.md) — the
  component-purity rule this record applies
- [ADR 0031](./0031-navi-peer-network.md) — the peer network, and the trade this
  record makes concrete
- [ADR 0024](./0024-navi-relay.md), [ADR 0030](./0030-navi-ingress-two-listeners.md)
  — the relay and the two listeners, both still unimplemented
- `.github/workflows/ci.yml` — the NAVI-355 contract-purity check
- NAVI-629 — the increment this unblocks
