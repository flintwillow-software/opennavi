---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140900, 140901, 140902, 140903]
---

# Reviewer

> The cross-domain code review gate — reviews PRs across all engineering domains, prioritizes by class of service.

| Field | Value |
|-------|-------|
| **name** | `reviewer` |
| **owns** | Code review across domains |
| **pullsFrom** | In Review column |

## 00 Review

The Reviewer pulls items from the In Review column (WIP 2), reviews the PR against standards and acceptance criteria, and either approves (→ Verify) or requests changes (→ In Progress).

### Requirement: Review Flow

The system SHALL have the Reviewer pull items from In Review, review the PR per standards, and either approve to Verify or request changes back to In Progress.

#### Scenario: Reviewing a standard PR

- **GIVEN** a PR in In Review with a linked item
- **WHEN** the Reviewer pulls it
- **THEN** the Reviewer SHALL review the diff, approve on pass, or request changes with specific feedback

## 01 Class-Priority Review

The Reviewer prioritizes by class of service — critical items get immediate emergency review; expedite items review next; standard items flow normally.

### Requirement: Class-Priority Review

The system SHALL have the Reviewer prioritize In Review items by class of service, performing emergency review for critical items within the SLA.

#### Scenario: Emergency review

- **GIVEN** a critical (P0) hotfix PR enters In Review
- **WHEN** the Reviewer sees the critical class
- **THEN** the Reviewer SHALL review it immediately, bypassing standard queue order

## 02 Cross-Domain Coverage

The Reviewer reviews across all domains — backend, frontend, devops, infrastructure — using per-domain standards.

### Requirement: Cross-Domain Review

The system SHALL have the Reviewer apply domain-specific review standards when reviewing backend, frontend, devops, and infrastructure PRs.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140900 | 0.2.0 | Initial reviewer role spec |