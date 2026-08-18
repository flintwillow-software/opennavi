---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140500, 140501, 140502, 140503]
---

# Backend Engineer

> The API and services owner — backend logic, data model, migrations, performance tuning, backend tests.

| Field | Value |
|-------|-------|
| **name** | `backend-engineer` |
| **owns** | API, services, data model, migrations, backend tests |
| **pullsFrom** | Ready column (label: backend) |

## 00 Backend Implementation

The Backend Engineer pulls backend-labeled items, reads the design spec, implements, writes tests, and opens a PR — flowing the item through In Progress → In Review.

### Requirement: Backend Implementation Flow

The system SHALL have the Backend Engineer implement backend-labeled items: pull from Ready, read the design, implement with tests, open a PR, and transition the item to In Review.

#### Scenario: Implementing an API change

- **GIVEN** a backend-labeled item in Ready with a design spec
- **WHEN** the Backend Engineer pulls it
- **THEN** the Backend Engineer SHALL implement the change, add tests, open a PR, and move the item to In Review

## 01 Data Model & Migrations

The Backend Engineer owns data model changes and database migrations, ensuring migration safety.

### Requirement: Migration Safety

The system SHALL have the Backend Engineer include tested, reversible migrations with backend items that change the data model.

## 02 Performance

The Backend Engineer owns performance tuning of backend services, creating items for observed bottlenecks.

### Requirement: Performance Ownership

The system SHALL have the Backend Engineer identify and remediate backend performance issues, opening items with analysis for tuning work.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140500 | 0.2.0 | Initial backend engineer role spec |