---
status: draft
created: '2026-09-23'
updated: '2026-09-23'
tags:
  - change-log
  - tool-catalog
  - thought-management-tools
spec-refs: [130600, 130601, 130602, 130603, 130604]
---

# 0.2.0 — feat: thought management tools (`thoughts_*`)

> Derived from the spec diff of S130600–130604 and informed by the codebase analysis (2026-09-23). The navi manages its own thought queue through catalog tools: self-scheduling follow-ups, deferred pickup, and queue reshaping.

## Summary

Add the `thoughts_*` namespace to the tool catalog: four queue-attached tools (`thoughts_read`, `thoughts_create`, `thoughts_update`, `thoughts_delete`) that receive the navi's own thought-queue handle at registration and operate on the same queue the runtime runs on. This gives the navi self-directed agency in the house model (S010000) — scheduling follow-up thoughts and deferring work — while everything stays registry-governed (schema validation, active set, IAM access levels) and durable (deferred thoughts are real queue records).

## Changes

- **`internal/thought` (deferred pickup)** — `defer_until` support: `Thought` gains a not-before field; `Dequeue`/claimability skips thoughts not yet due; crash recovery preserves deferral across restarts
  - `internal/thought/thought.go` — field + clamp helper for self-scheduled priority
  - `internal/thought/queue.go` — claimability check + `Enqueue` parameter
- **`internal/thought` (queue handle seam)** — a queue-handle type for tool registration (analogous to the S130500 stateful store seam): the daemon hands tools the navi's own queue handle; handlers get the queue per dispatch, respecting the `q.mu` re-entry discipline (no re-entry from event handlers; reads go through snapshots)
  - new file alongside `internal/thought/queue.go`
- **`tools/thoughts/` (new, public SDK)** — the four tools registered through `registry.Register` with the queue handle:
  - `thoughts_read` (AccessReadOnly, parallel) — queue snapshot with status filter, priority/recency ordering
  - `thoughts_create` (write, sequential) — prompt/context/metadata/priority, optional `defer_until`; tags `origin: tool`
  - `thoughts_update` (write, sequential) — priority change (clamped), pause/resume/cancel transitions, context append
  - `thoughts_delete` (write, sequential) — record removal; running thoughts rejected with a lifecycle error (cancel first)
- **`internal/daemon` (registration)** — `Assemble` registers the namespace against the runtime queue with the safety rails (origin tagging, priority clamp defaults); runtime-config hook for the clamp band if needed
  - `internal/daemon/daemon.go` — registration + `Runtime` plumbing (queue handle already exposed)
- **`internal/ops/metrics` (visibility)** — self-created thoughts visible via the existing `origin` metadata on snapshots; no new series required (reuse per-status gauges)

## Spec Refs

S130600, S130601, S130602, S130603, S130604 (with S010002 queue ops, S130400 registration, S040200 access levels)

## Code Context

- **Existing patterns**: the queue exposes `Enqueue`/`BumpPriority`/`Pause`/`Resume`/`Cancel`/`Complete`/`Fail`/`Retry` and `GetThoughts` snapshots — the tools are thin, governed wrappers over these; the stateful-tool seam (NAVI-267) and bridge (`tools/bridge`) are the registration/governance patterns to mirror; CI gates via `just ci`, CGO_ENABLED=0, no new dependencies.
- **Existing code vs. spec**: queue operations exist; `defer_until` claimability is the one new queue mechanism; the queue handle for tools is the analogous seam to `RegisterStateful`.
- **Dependencies**: none new.

## Notes

- **Step 4 grouping**: own epic — NAVI-321 ("Thought management tools — the navi manages its own queue (S130600)", parent NAVI-59) with two sub-issues: NAVI-322 defer_until queue mechanism + queue-handle registration seam (blocks the tools package), NAVI-323 tools/thoughts package + daemon registration.
- **Dependency interaction with the thought-loop epic**: the queue-handle registration variant is the new seam; `RouteMessage` (thought-loop issue NAVI-317) shares the same "never re-enter under q.mu" discipline.
- Tool names follow the namespace_action pattern (S130400 §04): `thoughts_read`, `thoughts_create`, `thoughts_update`, `thoughts_delete`.

## Tracked

- Epic: Thought management tools — the navi manages its own queue — NAVI-321 [priority: high]
  - Issue: defer_until queue mechanism + queue-handle registration seam — NAVI-322 [priority: high, blocks: NAVI-323]
  - Issue: tools/thoughts package + daemon registration — NAVI-323 [priority: medium, blocked-by: NAVI-322]