---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140600, 140601, 140602, 140603]
---

# Frontend Engineer

> The UI/UX owner — frontend architecture, frontend tests, accessibility, theme/config.

| Field | Value |
|-------|-------|
| **name** | `frontend-engineer` |
| **owns** | UI, UX, frontend architecture, frontend tests, a11y |
| **pullsFrom** | Ready column (label: frontend) |

## 00 Frontend Implementation

The Frontend Engineer pulls frontend-labeled items, implements the UI/UX per design, adds frontend tests, and opens a PR.

### Requirement: Frontend Implementation Flow

The system SHALL have the Frontend Engineer implement frontend-labeled items: pull from Ready, implement per design, add tests, open a PR, and transition to In Review.

#### Scenario: Building a login page

- **GIVEN** a frontend-labeled item in Ready
- **WHEN** the Frontend Engineer pulls it
- **THEN** the Frontend Engineer SHALL implement the UI, add frontend tests, open a PR, and move the item to In Review

## 01 Accessibility

The Frontend Engineer owns accessibility standards, ensuring UI work meets a11y requirements.

### Requirement: Accessibility Standards

The system SHALL have the Frontend Engineer apply accessibility standards to frontend items, flagging a11y violations in review.

## 02 Frontend Tests

The Frontend Engineer owns the frontend test suite — component, integration, and e2e tests.

### Requirement: Frontend Test Ownership

The system SHALL have the Frontend Engineer maintain frontend test coverage, adding tests with each frontend change.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140600 | 0.2.0 | Initial frontend engineer role spec |