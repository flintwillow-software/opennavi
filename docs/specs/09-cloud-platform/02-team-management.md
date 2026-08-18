---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - cloud-platform
priority: high
spec-refs: [090200, 090201, 090202, 090203]
---

# 02 Team Management

> Organization and team hierarchies, role-based access control, usage and cost reporting, and team health monitoring for managing navi teams at scale.

## 00 Organization & Team Hierarchy

The platform supports multi-level organizational structures with teams, enabling navi team management at scale.

### Requirement:

- The system SHALL support organization and team hierarchies (FR-3.1).

### User Stories

- **US-006**: As Sam (Team Lead), I want to manage a team of navis per team, so that my org runs agents at scale with controls.

### Design Notes

The Team Manager service supports:
- Organizations as top-level entities
- Teams within organizations, each with their own navi team
- Hierarchical cost attribution and usage tracking
- Navis can be assigned to specific teams or shared across the org
- RBAC roles are scoped to organization and team levels

---

## 01 Role-Based Access Control

Access to navis, teams, and platform features is governed by a role-based access control model.

### Requirement:

- The system SHALL provide role-based access control (owner, admin, member, viewer) (FR-3.2).

### Design Notes

RBAC roles:
- **Owner** — full access to org settings, billing, team management, all navis
- **Admin** — manage teams, navis, and members within the org
- **Member** — deploy, configure, and manage navis in assigned teams
- **Viewer** — read-only access to navi status, logs, and reports

Permissions are enforced at the API level by the Auth service, which issues JWT tokens with role claims.

---

## 02 Usage & Cost Reporting

Team leads and administrators can view detailed usage and cost reports per navi, per team, and per organization.

### Requirement:

- The system SHALL provide per-navi and per-team usage and cost reporting (FR-3.3).

### Scenario:

- GIVEN a team with 5 navis, WHEN the team lead views the cost report, THEN per-navi and per-team costs are shown (TC-9).

### User Stories

- **US-007**: As Sam, I want cost and usage reports per navi and per team, so that I can manage budget.

### Design Notes

Reports include:
- Runtime hours per navi
- Storage consumption (GB)
- LLM API usage (tokens, cost)
- Marketplace subscription costs
- Per-team aggregated costs
- Cost attribution breakdown

The Billing Engine provides the data pipeline; the Team Dashboard renders the reports.

---

## 03 Health Monitoring & Alerting

The platform monitors navi team health and provides proactive alerting for incidents.

### Requirement:

- The system SHALL support navi team health monitoring and alerting (FR-3.4, Should).

### Design Notes

- Health checks run on every navi at configurable intervals
- Status indicators: running, degraded, stopped, crashed, updating
- Alerting for: navi crashes, resource exhaustion, API key expiry, backup failures
- Integration with Prometheus metrics and Grafana dashboards
- Edge case: navi crashes — auto-restart up to 3 times, then alert user

---

## Implementation Notes

### Phase 4: Team Management — Dashboard, Teams, Analytics

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Web dashboard (navi list, status, logs) | P1 | L | Phase 3 |
| Organization and team management | P1 | M | Auth service |
| Role-based access control (RBAC) | P1 | M | Team management |
| Usage and cost reporting | P1 | M | Billing engine |
| Team health monitoring and alerting | P2 | M | Dashboard |
| Audit log viewer | P2 | S | Dashboard |

### Integration Points

- **Auth Service** — JWT-based authentication and RBAC enforcement
- **Billing Engine** — cost data for usage and cost reporting
- **Prometheus + Grafana** — monitoring and alerting dashboards
- **PostgreSQL** — organization, team, and RBAC data

### Order of Operations

- Phase 4 depends on Phase 3 (Hosted Runtime) being operational
- Team management dashboard is a new interface — no migration from CLI-only management

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-3 Team Management |