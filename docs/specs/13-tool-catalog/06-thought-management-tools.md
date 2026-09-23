---
status: draft
created: '2026-09-23'
updated: '2026-09-23'
tags:
  - spec
  - tool-catalog
  - thought-management-tools
priority: medium
spec-refs: [130600, 130601, 130602, 130603, 130604]
---

# Thought Management Tools

> The navi's tools for managing its own thought queue: scheduling follow-up thoughts, deferring work, and reading or reshaping what is queued. The house model (S010000) makes the queue the navi's main loop; these tools make the navi itself a door into it.

## 00 Overview

### 1.1 Queue-Attached Tools

The `thoughts_*` namespace lets a navi observe and manage its own thought queue through the standard catalog mechanism: the tools are catalog tools (S130000) registered through the public registry (S130400) with complete metadata, and enforce their access levels through the same IAM mapping (S040200) as every other tool.

The tools differ from the stateful storage tools (S130500 §07) in their handle: instead of a per-navi store handle, they receive **the navi's own thought-queue handle** at registration — the same queue the runtime runs on. Registration through the registry (S130400) keeps governance identical: schema validation before dispatch, active-set membership, access levels.

#### Scenario: Queue-attached registration

GIVEN a navi runtime assembling its tool registry
WHEN the thought-management tools are registered
THEN the system SHALL bind them to the navi's own thought-queue handle through the registry, with the same metadata validation, active-set, and access-level governance as any catalog tool.

### 1.2 Safety Rails

A navi managing its own queue is powerful — self-perpetuation and priority escalation are the risks. Constraints:

- **Origin tagging**: thoughts created through `thoughts_create` SHALL be tagged in `Metadata` (`origin: self` vs. external doors) so humans and dashboards can distinguish self-directed from connector-sourced work.
- **Priority clamp**: self-scheduled thoughts SHALL NOT preempt running thoughts above the queue's external-work threshold unless the runtime config grants it; default priority assignment is clamped to a conservative band.
- **Deferred work is durable**: a deferred thought is a real queued thought (persistence, crash recovery, lifecycle) — not an in-memory timer.

## 01 `thoughts_read`

**Access:** read-only · **Mode:** parallel · **Recovery:** none

### Requirement: Queue Observation

`thoughts_read` SHALL return a snapshot of the navi's thought queue: id, status, priority, prompt summary, timestamps, and metadata per thought, optionally filtered by status.

#### Scenario: List the queue

GIVEN the navi has thoughts in multiple states
WHEN `thoughts_read` is called without a filter
THEN it SHALL return every thought with its status, priority, and timestamps, ordered by priority then recency.

#### Scenario: Filter by status

GIVEN `thoughts_read` is called with `status: paused`
THEN it SHALL return only paused thoughts.

## 02 `thoughts_create`

**Access:** write · **Mode:** sequential · **Recovery:** none (enqueue is idempotent at the queue)

### Requirement: Schedule Follow-Up Work

`thoughts_create` SHALL enqueue a new thought for the navi: `prompt` (required), optional `context`, `metadata`, and `priority`.

#### Scenario: Follow-up thought

GIVEN a running thought concludes that follow-up work is needed
WHEN the navi calls `thoughts_create` with a prompt and context
THEN the system SHALL enqueue a new durable thought tagged `origin: tool`, schedulable by the same priority rules as any other thought.

### Requirement: Deferred Pickup

`thoughts_create` SHALL accept an optional `defer_until` timestamp: the thought is queued immediately (durable, visible in `thoughts_read` as deferred/pending) but SHALL NOT be claimable by the pool until the time arrives.

#### Scenario: Pick work up later

GIVEN `thoughts_create` is called with `defer_until` in the future
WHEN the queue presents claimable thoughts to workers
THEN the deferred thought SHALL be skipped until `defer_until` passes, surviving restarts (durability, S010002).

#### Scenario: Recurring work stays with cron

GIVEN the navi needs recurring (not one-shot) follow-up
WHEN it wants scheduled work
THEN recurring schedules SHALL be created through the cron mechanism (runtime config / cron tooling), not by repeatedly calling `thoughts_create` — the tool defers one-shot work only.

## 03 `thoughts_update`

**Access:** write · **Mode:** sequential

### Requirement: Reshape Queued Work

`thoughts_update` SHALL apply partial updates by thought id: priority change (clamped per §1.2), status transitions (pause a running/queued thought; resume a paused one; cancel a non-terminal thought), and context append.

#### Scenario: Bump priority

GIVEN a queued thought is blocking higher-value work
WHEN `thoughts_update` raises its priority
THEN the system SHALL apply the same reordering/preemption rules as `BumpPriority` (S010002), clamped per §1.2.

#### Scenario: Pause and resume own work

GIVEN a thought the navi wants to defer mid-flight
WHEN `thoughts_update` pauses it
THEN the park-with-checkpoint semantics (S010002) apply; a later resume call re-queues it.

## 04 `thoughts_delete`

**Access:** write · **Mode:** sequential

### Requirement: Remove Queued Work

`thoughts_delete` SHALL remove a thought record entirely by id. A running thought SHALL NOT be deletable in place — it must be paused or cancelled first (the tool returns a state-specific error identifying the lifecycle constraint).

#### Scenario: Delete a deferred thought

GIVEN a deferred thought the navi no longer needs
WHEN `thoughts_delete` is called with its id
THEN the system SHALL remove the thought record from the queue and persistence.

#### Scenario: Running thought is protected

GIVEN a running thought
WHEN `thoughts_delete` is called on it
THEN the system SHALL reject with an error naming the lifecycle state (cancel via `thoughts_update` first).

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-09-23 | S130600–130604 | 0.2.0 | Drafted: thought-management namespace (`thoughts_read/create/update/delete`) — queue-attached tools for self-scheduling, deferred pickup, and queue reshaping (thought-loop case study) |