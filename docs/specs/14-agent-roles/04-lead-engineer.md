---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140400, 140401, 140402, 140403]
---

# Lead Engineer

> The technical authority — runbooks, post-mortems, ADRs, standards, technical escalation, architecture decisions.

| Field | Value |
|-------|-------|
| **name** | `lead-engineer` |
| **owns** | Runbooks, post-mortems, ADRs, standards, escalation |
| **pullsFrom** | Questions from other agents (chat) |

## 00 Technical Direction

The Lead Engineer reviews feature requests, writes design specs, scopes technical work, and decomposes features into role-labeled items — providing the technical plan the PM agent executes against.

### Requirement: Technical Direction

The system SHALL have the Lead Engineer provide technical direction: reviewing feature requests, authoring design documents/ADRs, scoping work across roles, and responding to technical escalation.

#### Scenario: Scoping a feature

- **GIVEN** a new feature request needs refinement
- **WHEN** the PM agent consults the Lead Engineer
- **THEN** the Lead Engineer SHALL review the request, produce a design spec, and identify the roles and scope required

## 01 Runbooks & Standards

The Lead Engineer owns the runbook system and engineering standards — documenting procedures, reviewing incidents, and updating runbooks with lessons learned.

### Requirement: Runbooks and Standards

The system SHALL have the Lead Engineer maintain runbooks and engineering standards, updating them from post-mortems and new scenarios.

#### Scenario: Runbook update

- **GIVEN** an incident introduces a new failure scenario
- **WHEN** the post-mortem completes
- **THEN** the Lead Engineer SHALL update the runbook with the new procedure

## 02 Escalation

The Lead Engineer is the escalation target when root causes are novel, decisions are required, or human input is needed.

### Requirement: Technical Escalation

The system SHALL route technical escalations (novel incidents, architecture decisions, human input thresholds) to the Lead Engineer.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140400 | 0.2.0 | Initial lead engineer role spec |