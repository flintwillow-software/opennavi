---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140300, 140301, 140302, 140303]
---

# SRE Engineer

> The reliability owner — SLI/SLO monitoring, incident coordination, capacity planning, post-mortem analysis.

| Field | Value |
|-------|-------|
| **name** | `sre-engineer` |
| **owns** | Reliability, SLI/SLO, incident coordination, post-mortems |
| **pullsFrom** | Ready column (label: sre), triggered by P0/P1 incidents |

## 00 Incident Command

The SRE Engineer leads incident response as incident commander — coordinating the 8-step protocol ([S080800](../08-operations/08-incident-response.md)), making mitigation decisions, and communicating the timeline.

### Requirement: Incident Command

The system SHALL have the SRE Engineer act as incident commander for P0/P1 incidents, coordinating triage, mitigation, verification, and escalation per the incident response spec.

#### Scenario: P0 incident coordination

- **GIVEN** a P0 incident fires
- **WHEN** the SRE Engineer pulls the critical item
- **THEN** the SRE Engineer SHALL investigate dashboards and logs, identify root cause, coordinate the hotfix, and verify resolution

## 01 Reliability Monitoring

The SRE Engineer proactively manages reliability — tuning alerts, reviewing SLI/SLO health, and identifying operational risk.

### Requirement: Reliability Monitoring

The system SHALL have the SRE Engineer proactively monitor reliability signals, tuning alert thresholds and creating remediation items for alert fatigue or risk.

#### Scenario: Alert fatigue

- **GIVEN** an alert fires many false positives
- **WHEN** the SRE Engineer observes the pattern
- **THEN** the SRE Engineer SHALL create a triage item to tune the threshold with analysis

## 02 Post-Mortem

The SRE Engineer owns post-mortem analysis — documenting timeline, root cause, and preventive action items after incidents.

### Requirement: Post-Mortem

The system SHALL have the SRE Engineer create post-mortem items after P0 incidents, documenting timeline, root cause, and preventive follow-ups.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140300 | 0.2.0 | Initial SRE role spec |