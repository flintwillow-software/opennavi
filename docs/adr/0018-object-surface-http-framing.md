---
status: proposed
date: '2026-10-08'
supersedes: []
tags:
  - adr
  - portal
  - object-model
  - wire
---

# 0018 — The object surface's HTTP framing

## Context

ADR 0017 decision 10 fixed the *shape* a remote binding exposes — the type is
the top-level namespace (`GET /{type}`, `GET /{type}/{id}`,
`GET /{type}/{id}/{detail}`, `POST /{type}/{id}/{action}`), with the surface's
own routes namespaced under `_` so a type may be named anything — and left the
**bytes** open (its open question 2): "this fixes the shape a binding exposes,
not the bytes."

The gateway now binds the surface, so the bytes have to be decided. Two things
force the decision now rather than later:

- **A portal author outside this repository has to be able to implement the
  binding.** The extension path is a Go module (ADR 0005), but the wire is what
  a WebUI in another language speaks, so the framing cannot leak Go type or
  field names.
- **The framing is copied, not referenced.** Every future binding (a WebUI, a
  mobile client, a test harness) implements the same bytes, so a framing that
  is merely implicit in one Go client becomes an ABI by accident.

The descriptor types (`object.Type`, `Field`, `Action`, ...) also had no
serialized form: they were Go structs with Go field names, which is not an ABI
anyone should depend on.

## Decision

1. **The descriptors are their own wire format.** The `object` types carry
   `json` tags, so the Go value and the wire value are one shape rather than a
   parallel set of DTOs that can drift. Descriptor properties are camelCase
   (`refType`, `itemKind`, `readOnly`, `groupBy`), matching the framing the
   observation stream already uses (`thoughtID`, `next`, `lost`). The *field
   names inside an object's `fields` map* are unchanged: they are the navi's
   declared wire names (`created_at`, `tool_call_id`), and the descriptor is
   what tells a renderer about them.

2. **Routes and response bodies.**

   | Route | Response |
   |---|---|
   | `GET /_types` | `{"types": [Type, ...]}` |
   | `GET /{type}?cursor=&limit=` | `{"objects": [Object, ...], "next": "..."}` |
   | `GET /{type}/{id}` | the `Object` itself |
   | `GET /{type}/{id}/{detail}?cursor=&limit=` | `{"objects": [...], "next": "..."}` |
   | `POST /{type}/{id}/{action}` | `{"ok": true}` |
   | `GET /_deltas?from=N` | `{"deltas": [Delta, ...], "next": N, "lost": bool}` |

   The action's **request body is its arguments** (an empty body means no
   arguments) — the route already names the action, so wrapping them in
   `{"args": {...}}` would be ceremony. `next` is the opaque cursor the surface
   returned; `lost` is the surface's gap flag.

3. **`portal` and `_`-prefixed roots are reserved type names.** The
   observation stream and control channel keep their `/portal` namespace, and
   the surface's own routes live under `_`. A type may be named anything else.

4. **Every object route is authorized on the observation stream's fail-closed
   `Attach` path**, so a navi has one attachment story. The action route is
   *additionally* authorized by the surface itself (`navi.WithObjectAuthorizer`),
   so the gateway never decides who may act — it frames, and the surface
   decides.

5. **Errors are mapped explicitly, never defaulted to success.** A refusal is
   403, an unknown type/object/detail is 404, an unknown action or invalid
   arguments is 400, an unserved capability is 501, and anything else is a
   server-side 500. A malformed body is 400 rather than an empty argument set.

6. **The remote binding honors the contract's errorless methods honestly.**
   `Surface.Types()` cannot report an error, so descriptors — a frozen
   declaration — are fetched once and cached (`RefreshTypes`), and `Types()`
   returns that snapshot. `Surface.Watch()` cannot report an error either, so a
   transport failure is reported as a **gap** (`lost = true`, cursor
   unchanged) rather than as "nothing changed": a stale view the caller is told
   about is acceptable; a silent one is not. The error-returning methods
   (`List`, `Get`, `Detail`, `Act`) return a `StatusError` carrying the HTTP
   status, so a caller can tell a refusal from a transport failure.

7. **One client owns the framing.** `gateway.Client` is the remote binding of
   `object.Surface`; a portal embeds or holds it instead of hand-rolling
   fetches. Path segments are escaped, so an id or action containing a slash
   cannot forge another route.

## Consequences

- An out-of-repo portal binds the same contract an in-process portal is handed:
  the TUI's remote transport now reads pre-existing thoughts and their
  transcripts through it (its snapshot was previously empty), and the
  in-process and remote paths are testable for equality.
- The framing lives in one package: `portal/gateway` owns both the server
  routes and the `Client`, and the Go side of the contract is checked by the
  compiler (`Client` is asserted to satisfy `object.Surface`).
- There are **two live streams** for a remote portal: `/portal/events` (queue
  and agent observation) and `/_deltas` (objects). They overlap in what they
  report. ADR 0012's proposed unified stream is the intended resolution; until
  then a portal chooses by what it renders.
- `Surface.Types()` and `Surface.Watch()` remain errorless, which is a real
  friction for a network binding. It is recorded here rather than papered over:
  the cache and the gap flag are honest workarounds, and per-client IAM scoping
  (ADR 0012) is the likely forcing function for changing the interface.
- Authorization is still coarse: the gateway authorizes the *attachment* and
  the surface authorizes the *action*, but there is no per-type or per-field
  scoping yet. ADR 0017's resource/verb model is the direction.

## Alternatives considered

- **Go field names on the wire.** Least work, but it leaks the Go ABI, is
  unstable under refactors, and is unusable for a non-Go client. Rejected.
- **Wire DTOs defined in `portal/gateway`.** Keeps `object` free of tags, but
  the two shapes drift: a new descriptor field would be silently dropped at the
  boundary, which is exactly the kind of silent failure this project rejects.
  Rejected.
- **`{"args": {...}}` as the action body.** Self-describing, but the route
  already names the action, and it makes the common no-argument call a
  non-empty body. Rejected as ceremony.
- **A separate `/objects/...` root.** Would avoid reserving type names, but
  ADR 0017 decision 10 makes the type the top-level namespace so a portal can
  treat `/{type}` as a resource. Rejected.
- **A generic query/expression language on `/{type}`.** More powerful, but it
  is an injection surface and an ABI of its own; declared per-type filters come
  first (ADR 0017 open question 3). Rejected for now.

## Open questions

1. **Does `/_deltas` fold into ADR 0012's unified stream?** The two live
   streams report overlapping facts; the unified stream is where they should
   converge.
2. **Do `Types()` and `Watch()` gain error returns?** Per-client scoping and a
   first non-Go binding are the likely triggers.
3. **Per-type/per-field authorization.** Today the surface authorizes an
   action as a whole; ADR 0017's consequence anticipates a resource/verb model
   (type × field × action).
4. **Streaming vs polling.** `/_deltas` is a cursor poll. Server-sent events or
   a websocket frame are the obvious next step if a browser portal needs
   push; nothing here forecloses it.
