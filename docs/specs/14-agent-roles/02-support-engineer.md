---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140200, 140201, 140202, 140203]
---

# Support Engineer

> The user-facing role — handles user reports, initial triage, common fixes, user communication, and feature request intake.

| Field | Value |
|-------|-------|
| **name** | `support-engineer` |
| **owns** | User reports, triage, comms, feature intake |
| **pullsFrom** | Triage column (label: support), user chat platforms |

## 00 User Report Handling

The Support Engineer pulls support-labeled items from Triage, investigates, communicates with the user, and either fixes directly or hands off to a role engineer.

### Requirement: Handle User Reports

The system SHALL have the Support Engineer pull support-labeled items, investigate the issue, communicate status to the user via configured platforms, and either resolve or route to the appropriate role.

#### Scenario: Password reset issue

- **GIVEN** a user report that a password reset email never arrives
- **WHEN** the Support Engineer pulls the item
- **THEN** the Support Engineer SHALL investigate the delivery pipeline, notify the user of the status, and open a follow-up item for any systemic fix

## 01 Feature Intake

The Support Engineer captures feature requests and support patterns from user interactions, feeding the creation pipeline.

### Requirement: Feature Intake

The system SHALL have the Support Engineer convert recurring user requests into board items with feature or improvement classification.

#### Scenario: Pattern detection

- **GIVEN** multiple users report the same search relevance problem
- **WHEN** the Support Engineer detects the pattern
- **THEN** the Support Engineer SHALL create a triage item for the improvement with supporting evidence

## 02 User Communication

The Support Engineer is the primary user-facing communicator — status updates, announcements, and incident comms flow through this role.

### Requirement: User Communication

The system SHALL route user-facing communication (status updates, release announcements, incident updates) through the Support Engineer role via configured platform adapters.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140200 | 0.2.0 | Initial support engineer role spec |