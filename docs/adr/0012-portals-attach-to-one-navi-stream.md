---
status: proposed
date: '2026-09-30'
supersedes: []
tags:
  - adr
  - portal
  - security
  - stream
---

# 0012 — Portals attach to one navi stream; access is IAM-scoped

## Context

A portal currently receives two separate things from the runtime:

- **Read** — `Deps.Observation`, a bounded per-subscriber mailbox
  (NAVI-352), live-only, with no history (ADR 0011 fixes the history half).
- **Write** — a method set on the `Portal` interface (`Attach`, `Steer`,
  `Pause`, `Resume`, `Cancel`, `BumpPriority`, `Retry`, `Enqueue`), authorized
  per action through `portal.Authorizer`.

The gateway frames those as two endpoints with two authorization stories:
`GET /portal/events` rides `Attach`, `POST /portal/control` authorizes each
action. Every surface re-implements the same split, and "what this portal may
see and do" ends up encoded in *which methods it implements and calls* rather
than in configuration. Adding a fourth portal (a WebUI, a remote client) means
writing that split a fourth time.

There is also a direction question the read/write split hides: a portal is a
window onto navi state. Reading and writing are the same relationship with
different verbs, and once navigation is a stream, a surface is a translation
layer rather than an API client.

## Decision

1. **One bidirectional stream is the portal entrypoint.** A portal latches onto
   the navi's default stream. The stream carries state on attach, live events
   thereafter, and control commands back the other way. Building a portal means
   writing a **translation layer** for one protocol — not assembling a read path
   and a write path separately.

2. **The stream opens with a bounded state snapshot**, then continues with live
   events. The snapshot carries thought metadata, statuses, and transcript
   accessors (ADR 0011's metadata-eager shape); detail — transcript entries,
   memory contents — is delivered on access, never inlined wholesale.

3. **Read and write are not separate capabilities.** A portal speaks one
   protocol. What it may read and what it may write is decided by **IAM-style
   permissions configured per portal**, not by which methods it implements or
   which endpoints it is allowed to call.

4. **The gateway is one binding of the stream**, not a parallel API. The TUI's
   remote transport and a future WebUI translate the same stream. The in-process
   TUI transport is the same stream with no wire.

## Consequences

- **New portals are cheap and uniform.** ADR 0002's example
  (`n.Portals.Add(portal.TUI())`, `n.Portals.Add(portal.WebSocket(":8090"))`)
  becomes literal: each surface is a translation layer over one stream.
- **`portal.Observer` and the `Portal` method set converge** on a stream
  contract. `Addressed` (port-conflict detection) is unaffected.
- **Authorization moves to a resource/verb model.** The per-`Action`
  `Authorizer` either extends into IAM scoping (identity × resource × verb) or
  is superseded by it. Fail-closed remains the default with no policy wired.
- **Migration is real work.** The TUI's in-process transport, the gateway's two
  endpoints, and the remote transport all change shape; this is not an additive
  change to the component ABI.
- **History and live observation become one thing to a portal**, which is what
  makes NAVI-353 a contract property rather than a per-surface feature.

## Alternatives considered

- **Keep the read stream and write methods separate.** Works, and is what
  shipped for NAVI-352, but every surface re-implements the split and
  permissions live in code. Rejected as the end state.
- **One stream with no scoping** — every portal that can attach can read and
  write everything. Simplest, but a remote front-end would implicitly hold
  every capability, and an operator could not grant read-only history access.
  Rejected.
- **Two streams (data and control) multiplexed on one connection.** Closer to
  the current code, but it keeps the read/write distinction in the protocol and
  only moves it down a layer. Rejected.

## Open questions

1. **Frame and transport shape.** Not decided here; framing belongs to the wire
   protocol and is a separate design.
2. **Where IAM policy is expressed** — a navi-level config file, a per-portal
   declaration, or an external policy source.
3. **Whether the in-process TUI uses the stream or keeps direct calls.** The
   stream is the contract; using it in-process may be worthwhile for fidelity
   even where it is not required.
