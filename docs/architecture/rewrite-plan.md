---
status: draft
created: '2026-09-24'
updated: '2026-09-24'
tags:
  - architecture
  - plan
  - migration
---

# Rewrite Plan

> Ordered milestones from the current monolith to the navi composition root.
> This document is the durable roadmap; **issue status, ownership, and blockers
> live in Huly**. Each milestone maps to a Huly epic.

## Target

Current: `cmd/navi` + `internal/daemon.Assemble` hardcode the runtime; `sdk/`
is a validation stub that nothing bridges to.

Target: a public `navi` composition root over public contract packages
(`tool`, `thought`, `connector`, `portal`, `model`, `skill`), with
implementations behind them, `cmd/navi` reduced to a reference navi, and the
starter templates as real out-of-repo-style navis.

See [overview.md](./overview.md), [runtime.md](./runtime.md),
[security.md](./security.md), [extension-model.md](./extension-model.md).

## Milestones

| ID | Goal | Depends on | Verification gate | Huly |
|---|---|---|---|---|
| **M0** | Freeze design + decisions | — | ADRs accepted | NAVI-344 |
| **M1** | Create leaf contract packages: `tool`, `skill`, `model`, `thought` (interface), `connector`, `portal` — stdlib-only, no behavior changes | M0 | `go build ./...`, unit tests for new types | NAVI-345 |
| **M2** | Build the `navi` composition root: `New`, `With*` options, collections, `Add` validation, freeze-on-`Run`, cross-cutting validation | M1 | Run a mock-provider navi end-to-end in a test | NAVI-343 |
| **M3** | Promote `internal/thought` → public `thought/`; split `internal/interfaces` into `connector`/`portal`; keep queue semantics | M1 | Existing queue/routing/recovery tests green | NAVI-348 |
| **M4** | Port the catalog to the collection model: builtin tools, todo/note/time/http/thoughts tools, skills, providers, cron connector, TUI/gateway portals | M2, M3 | Catalog parity with today's tool set and active-set behavior | NAVI-342 |
| **M5** | Rebuild `cmd/navi` as a reference navi; convert `templates/navi-code` and `templates/navi-daemon` to real navi modules on the new surface | M4 | Template builds; `cmd/navi` runs the same flows | NAVI-347 |
| **M6** | Retire `sdk/` (`NaviDef`/`Build`); add synthetic out-of-repo CI modules (external navi + external component) | M5 | Guardrail + external-consumer CI green | NAVI-346 |
| **M7** | Reconcile remaining docs and remove dead references | M6 | `grep` clean; docs consistent with code | NAVI-349 |

## Notes per milestone

### M1 — contract packages

- Define types by *consumption*: `connector.Deps` and `portal.Deps` expose
  `thought.ThoughtSystem`, `memory.API`, `model.Registry`, `*slog.Logger`.
- Keep `tool` aligned with the existing `tools/registry` descriptor so the
  catalog port is mechanical.
- Contract packages must not import `store/sqlite`, `llm/*` adapters, or
  `internal/*`.

### M2 — composition root

- `Add` validates shape eagerly; `Run` validates cross-cutting invariants and
  freezes.
- `Thoughts` is constructed by `New`, not added.
- Provide a headless test path (mock provider) so `Run` is testable without
  network or TUI.

### M3 — public thought

- The public surface is the handle API + event types; queue internals
  (slot accounting, flusher, reconcile) stay unexported.
- Preserve: priority scheduling, preemption, foreground slot, park-with-
  checkpoint, crash recovery, memory-first persistence, `RouteMessage`
  lifecycle semantics.

### M5 — reference navi

- `cmd/navi` keeps its subcommands (`run`, `enqueue`, `health`) but assembles
  through `navi.Run`.
- Log routing, signal handling, and TUI/daemon presentation move into the SDK
  or the reference navi, not duplicated per navi.

### M6 — guardrail

- The current `S000805` guardrail builds the two in-tree templates. Extend it
  with a synthetic external navi module and an external component module that
  import only public contract packages.

## Explicitly out of scope

- Runtime plugins (S000600) — deferred (ADR 0005).
- Cloud platform / marketplace components (S000900).
- Memory subsystem rewrite; this plan moves visibility, not semantics.
- Removing the workspace `d3-*` skills; other repos may still use a spec tree.

## Backlog disposition

This rewrite supersedes the platform **foundation** epic, not the product
capability backlog. Existing Huly work is triaged as:

| Disposition | Issues |
|---|---|
| **Superseded** (canceled) | NAVI-68 (`navi.yaml` definition model), NAVI-69 (builder/compiler pipeline), NAVI-74 (build pipeline & Makefile) — children of the old NAVI-48 foundation epic |
| **Deferred** | NAVI-72 (runtime extension contract) — ADR 0005; revisit if a no-rebuild need appears |
| **Reshaped** | NAVI-70 (trust/signing → `security.md`), NAVI-71 (layout → package layout), NAVI-73 (`navi init` → navi repo scaffold), NAVI-256 (config → defaults-in-code) |
| **Re-homed** | NAVI-89 (messaging → connectors / portals / thought-system), NAVI-59 (tool catalog → `tool` contract), NAVI-97 (providers → `Models` collection) |
| **Absorbed** | NAVI-339 (thought scoping) → M3/M4 |
| **Orthogonal** (proceeds independently) | NAVI-50, NAVI-183, NAVI-79, NAVI-82, NAVI-39, and standalone follow-ups |

Interface-affected epics are linked `is-blocked-by` the milestone that provides
their new contract (NAVI-59/89/97 ← M1, NAVI-256 ← M2) and `relates-to` where they
are merely adjacent (NAVI-266 ↔ M4).

## Tracking

Huly epic **NAVI-341** ("OpenNAVI SDK composition rewrite"); milestones M0–M7
are sub-issues (table above) wired with blocked-by relations. Issue bodies link
back to the architecture docs and ADRs. Status, assignee, and blockers are
maintained in Huly only.

## Related

- [overview.md](./overview.md)
- [runtime.md](./runtime.md)
- [security.md](./security.md)
- [extension-model.md](./extension-model.md)
- [`docs/adr/`](../adr/)