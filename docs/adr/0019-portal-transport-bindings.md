---
status: accepted
date: '2026-10-09'
supersedes: [0012]
tags:
  - adr
  - portal
  - transport
  - stream
---

# 0019 — Portal transport bindings and the unified stream

## Context

ADR 0012 (proposed) decided that portals attach to **one bidirectional navi
stream**, opened with a bounded state snapshot, with read and write scoped by
IAM rather than by separate surfaces. ADR 0017 implemented its decision 4 — one
`object.Surface` serves the in-process portal and the remote binding — and ADR
0018 fixed the framing of the object routes. What remained was the stream
itself and the transport under it.

The shape that shipped is four surfaces: `/portal/events` (queue and agent
observation), `/_deltas` (object changes), `/portal/control` (verbs), and the
type root `/{type}...` (reads and actions). Two of them are overlapping live
streams, and `/_deltas` has no consumer at all. The remote client polls
`/portal/events` from a pump goroutine, and the in-process TUI polls its own
mailbox on a timer, because nothing can wake it.

The remote path was validated end to end (`navi connect` against a served
gateway), and that validation produced three bugs with one shape — **a portal
must observe a process it does not own**:

1. the remote client rendered an empty screen, because its asynchronous
   descriptor fetch raced the idle tick that stopped the drain;
2. the gateway panicked on a fast shutdown, because its serve goroutine read a
   server pointer the shutdown had already cleared;
3. an idle host never saw a thought queued from the remote client, because the
   idle-free tick stopped the drain — it assumed the TUI was the only actor,
   and a navi changes from outside it (`navi enqueue`, cron, another portal).

All three are symptoms of the same missing thing: the stream is not a stream.
It is a mailbox a timer asks about.

This ADR also answers the question that prompted it — "portals have two modes,
baked into the binary: local, or remote over websockets or similar" — by
separating what "mode" was conflating and deciding the binding.

## Decision

1. **Transport, role, and identity are three axes, not one mode.** *Transport*
   (where the state lives) is chosen once at the navi level; *role* (`host` /
   `connect`) is a `cmd/navi` concern; *identity and policy* are
   per-connection. Collapsing them is what made a client demand a model and
   what would make "remote" imply "trusted" — while local is in fact the more
   privileged case.

2. **One stream per portal.** A portal opens a single stream carrying the
   bounded opening snapshot, observation events, object deltas, and control
   results. The split surfaces collapse into it. The object routes stay as a
   convenience API for non-portal clients (a script, `curl`, `navi enqueue`),
   but they are not the portal path.

3. **Frames carry the same contract.** A frame is a `Surface` call or result,
   or an observation event/delta — never a new dialect. ADR 0018's lesson: one
   shape, not a parallel ABI that drifts.

4. **SSE + POST is the first remote binding.** A portal is read-mostly with
   occasional actions; server-sent events are proxy-friendly, reconnect for
   free, need no new framing, and work from a browser without a library. A
   websocket is adopted only when a **named consumer** needs a low-latency
   upstream stream. Cursor polling remains a supported binding — it is what
   ships today and it works.

5. **The in-process binding pushes rather than polls.** The stream wakes the
   portal (Bubble Tea's `Program.Send`, or the equivalent) instead of a timer
   asking a mailbox. The 150 ms active and 1 s idle cadences both go away, and
   with them the blindness they were masking.

6. **The two authorization gates stay; identity becomes per-connection.**
   Attachment authorizes the connection, and the surface authorizes each
   action. IAM scoping (type × field × action) is the layer above. That is why
   identity cannot stay what it is today — `navi.WithObjectAuthorizer(auth,
   identity)` declares one identity for the whole surface, which is the seam
   that has to give.

7. **One write path per action, before any new transport.** `Transport.Steer`
   (control channel) and `Surface.Act` (object route) are two routes to the
   same queue verb, in both transports. The typed control verbs are removed
   from the transport and every action goes through `Act`. Creation
   (`Enqueue`) stays a distinct call: an object that does not exist yet has no
   id to act on.

8. **`Types()` and `Watch()` gain error returns** when the streaming binding
   lands. A stream can fail mid-flight, and a binding that answers a transport
   failure with "no changes" is a fail-open. Until then the current
   workarounds are honest and documented (descriptors cached because the
   declaration is frozen; a `Watch` failure reported as a delta gap).

## Consequences

- A new transport implements the stream, `Surface`, and creation — not a
  second verb set. That is the difference between "add SSE" and "implement the
  portal twice".
- `/_deltas` either gains its consumer (the stream) or is deleted; it has none
  today.
- An idle portal stops being blind by construction rather than by keeping a
  timer alive.
- More up-front work than bolting a socket onto four surfaces. The alternative
  is freezing a protocol with four overlapping pieces and two live streams.
- `cmd/navi`'s roles (NAVI-389) are unchanged: only the transport behind
  `connect` changes.
- The gateway keeps serving HTTP for non-portal clients, so nothing that
  depends on `curl`-ability regresses.

## Alternatives considered

- **Websockets first.** Bidirectional and low-latency, but it would socket four
  surfaces with four semantics for a read-mostly client. Adopted later, on
  demand, for the upstream stream only.
- **Keep the split surfaces; add a socket for deltas only.** Cheapest, but
  leaves two overlapping live streams and no single notion of "this portal's
  view".
- **Fold deltas into `/portal/events` and leave control as POSTs.** Close to
  the decision, but object reads stay on a separate path, so a portal still has
  two ways to learn the same fact.
- **One "mode" flag (local vs remote) with authorization implied.** Rejected:
  it is how a client ended up demanding a model, and it makes remote imply
  trusted.
- **Per-portal transport flags (`--remote` on each portal).** Multiplies modes
  by portals; transport is a navi-level choice.
- **Fix the poll cadence and stop there.** The three validation bugs are all
  fixable in the poll loop (they were), but every fix is a patch on the
  absence of a stream.

## Open questions

1. **IAM scoping.** What a connection's identity may read and write, and
   whether `object.Authorizer` grows a resource/verb model or is superseded by
   it (ADR 0017's consequence).
2. **The opening snapshot's bound.** How much state opens a stream — a page of
   every type, or only the types the portal declares it renders — and how that
   cursor relates to the delta cursor.
3. **Do the object routes survive as public API** once the stream lands, or
   become internal plumbing with the stream as the only portal path?
4. **Reconnection.** Does a reconnecting portal resume from its cursor or take
   a fresh snapshot? Today it resumes and reports a gap.
5. **Versioning.** Does the frame format get an explicit handshake? ADR 0018
   left the bytes open beyond the object routes.
