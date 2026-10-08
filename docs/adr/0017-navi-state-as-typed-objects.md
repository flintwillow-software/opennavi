---
status: proposed
date: '2026-10-08'
supersedes: []
tags:
  - adr
  - portal
  - datastore
  - object-model
---

# 0017 — Navi state is typed objects, exposed through one surface

## Context

Portals are windows onto navi state. Today each portal is handed a typed handle
per concern — `Deps.ThoughtSystem` for the queue, `Deps.Observation` for the
live stream, and (with NAVI-361) a transcript/history handle — and it hardcodes
how to render each concrete type: a thought, a todo, a note, a memory record.
Adding a fourth surface (a WebUI, a remote client) means writing the same
per-type rendering again, and state a component ships is invisible until every
portal learns about it.

The set structs already exist: `thought.Thought`, `datastore.Todo`/`Note`/
`MemoryRecord`/`Entry`, `model.Provider`, `skill.Skill`, `tool.Def`. What is
missing is:

1. a **declared descriptor** per type — fields, kinds, labels, grouping,
   sensitivity, relations, and allowed actions;
2. **one uniform read/act/watch surface** over every type, instead of a
   different typed handle per aggregate; and
3. a **live delta model** that updates objects, rather than type-specific
   events each portal must interpret.

ADR 0012 decided that portals attach to one bidirectional stream that opens with
a bounded state snapshot. This record decides what that snapshot *carries*:
typed objects with descriptors, not a domain-typed projection every portal has
to understand.

## Decision

1. **A type is a declared descriptor over a shared struct.** A type names the
   struct it projects and describes it: name, label, identity field, fields
   (name, kind, label, section, read-only, sensitive, optional), relations,
   details (named sub-collections of another type — a thought's transcript
   entries), and actions. The Go struct stays the in-process representation; the
   descriptor is the portal-facing ABI, so wire names are stable and versionable
   independently of Go field names.

2. **Descriptors are declared at the navi level.** The composition root
   registers the types it exposes, exactly like tools, models, and skills. The
   registry is a navi-level collection, not something each portal or component
   maintains: what a portal can see is visible in `main.go`, and a portal renders
   whatever the navi exposes. Components may ship a descriptor for a navi to
   register, but the registry is the navi's — that is what makes portals cheap to
   write.

3. **One surface, no per-type handles.** The surface offers `Types`, `List`,
   `Get`, `Detail`, `Act`, and `Watch`. A portal reads descriptors and objects,
   invokes declared actions, and consumes deltas. The same surface is handed to
   an in-process portal and framed over the wire by the gateway, so the
   in-process and remote paths share one contract (ADR 0012 decision 4).

4. **Actions are part of the type.** Control is not a separate capability
   surface: a type declares the actions that may be performed on its objects
   (`steer`, `pause`, `retry`, `complete`, `remove`, …), each with an argument
   schema (the existing `tool.ParseSchema`). A portal renders the actions it is
   granted instead of hardcoding verbs.

5. **Sensitive fields are excluded by the surface, not hidden by the portal.**
   Descriptors mark fields sensitive or read-only; the surface omits unauthorized
   fields from serialized objects entirely (absent, not null). The allow-list is
   explicit at the navi level and nothing is exposed by default — a free-form
   `Metadata` map and a raw `Checkpoint` are not exposed without an explicit
   declaration. This keeps ADR 0006 (compile-time ceiling) and ADR 0008 (secrets
   never literal) intact rather than punching a reflective hole in them.

6. **Live state is object deltas.** The observation fanout is translated into
   `{type, id, op, fields}` deltas (`create`/`update`/`delete`/`append`), so a
   portal keeps objects current without knowing queue or agent event kinds.
   Streaming text is an `append` to a detail collection, not a field update.

## Consequences

- A new type is visible to every portal the moment the navi registers it; a new
  portal renders every type without declaring a single struct. New portals are
  UI/UX work, not protocol work.
- `portal.Deps`'s typed handles shrink toward the surface (the queue handle
  remains only for a verb that is not yet an action).
- Per-type work does not disappear; it moves into the navi's providers: listing
  and reading each type, and mapping its events to deltas. That is where the
  domain knowledge belongs, and it is written once rather than per portal.
- The `datastore` per-aggregate views remain the typed persistence contract. The
  object surface is a projection over them, not a replacement, and the metadata
  / bounded-detail shape ADR 0011 chose is exactly what the surface serves.
- Authorization moves to a resource/verb model (type × field × action), as ADR
  0012 anticipated; the per-`Action` authorizer either extends into IAM scoping
  or is superseded by it, fail-closed by default.
- The in-process TUI's bespoke views (live chat, plan/dependency graph) still
  want typed data; the surface must not force every view through generic
  rendering (open question 1).

## Alternatives considered

- **Reflect over the Go structs (no declared descriptor).** Least work, but it
  exposes every field — including `Checkpoint` and free-form metadata — with no
  place to mark a field sensitive, no stable wire name, and no action schema.
  Rejected on security and versioning grounds.
- **Per-type typed handles on `Deps` (the NAVI-361 slice-1 shape).** Additive and
  type-safe, but every portal still learns each type and new state stays
  invisible until each portal is updated. Rejected as the end state; acceptable
  as a stepping stone for history.
- **Component-declared types.** Each component ships its descriptor and the navi
  aggregates automatically. Less boilerplate per navi, but it hides the exposed
  surface from `main.go` and makes "what can a portal see" implicit. Rejected in
  favor of the navi-level registry (decision 2); components may still ship
  descriptors for a navi to register.
- **Generic rendering everywhere, including in-process.** Uniform, but a
  lowest-common-denominator UI that renders the plan graph and streaming chat
  poorly, and it discards compile-time safety in the binary that has the most
  context. Rejected as a blanket rule (see open question 1).
- **A generic expression language for queries.** More powerful, but it is an
  injection surface and an ABI of its own; declared per-type filters cover the
  known surfaces first (open question 4).

## Open questions

1. **How much of the in-process TUI is generic.** Recommended: hybrid — typed
   structs for views with designed UX (live chat, plan graph, steering), the
   surface for the uniform parts (lists, metadata, history, todos/notes/memory)
   and for remote/WebUI. Confirm.
2. **Framing and transport** for the remote binding (ADR 0012 open question 1).
3. **Where IAM policy is expressed** — navi-level config, per-portal
   declaration, or an external source (ADR 0012 open question 2).
4. **Query shape.** Start with declared per-type filters (no expression
   language), and revisit only if a surface needs more.
5. **Type versioning.** How a descriptor changes without breaking a connected
   portal, and whether a type carries an explicit version.
