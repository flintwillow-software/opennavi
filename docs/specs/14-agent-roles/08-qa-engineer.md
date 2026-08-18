---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140800, 140801, 140802, 140803]
---

# QA Engineer

> The quality gate — verification, regression testing, deployment validation, acceptance criteria verification.

| Field | Value |
|-------|-------|
| **name** | `qa-engineer` |
| **owns** | Verification, regression, acceptance testing, staging validation |
| **pullsFrom** | Verify column |

## 00 Verification

The QA Engineer pulls items from the Verify column, validates the fix or feature in staging, and either passes or returns with feedback.

### Requirement: Verification Flow

The system SHALL have the QA Engineer pull items from the Verify column, validate acceptance criteria in staging, and either pass (move to Deployed) or return with feedback.

#### Scenario: Verifying a hotfix

- **GIVEN** a P0 hotfix item in Verify
- **WHEN** the QA Engineer pulls it
- **THEN** the QA Engineer SHALL validate the fix against the reproduction in staging
- **AND** the QA Engineer SHALL move the item to Deployed on success, or back to In Progress on failure

## 01 Regression

The QA Engineer owns the regression test suite, running it after each verification to catch side effects.

### Requirement: Regression Testing

The system SHALL have the QA Engineer run the regression test suite after each verification, blocking deployment if regressions occur.

## 02 Acceptance Criteria

The QA Engineer validates that acceptance criteria are met before marking items as verified.

### Requirement: Acceptance Criteria Validation

The system SHALL have the QA Engineer validate acceptance criteria against the item description before passing verification.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140800 | 0.2.0 | Initial QA engineer role spec |