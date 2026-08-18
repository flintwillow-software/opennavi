---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140100, 140101, 140102, 140103]
---

# PM Agent

> The board manager — triages incoming work, maintains priorities, monitors WIP and SLA, coordinates releases.

| Field | Value |
|-------|-------|
| **name** | `pm-agent` |
| **owns** | Triage queue, backlog, WIP, SLA, releases |
| **pullsFrom** | Incoming webhooks, alerts, roadmap |

## 00 Triage

The PM agent creates items in Triage for all incoming work — monitoring alerts, user reports, CVE scanners, roadmap items — and assigns severity, track, role label, and class of service.

### Requirement: Triage Incoming Work

The system SHALL have the PM agent triage all incoming work into the board's Triage column, assigning track (creation/operations), severity, role label, and class of service before moving items to Ready.

#### Scenario: Triaging an alert

- **GIVEN** a monitoring alert webhook arrives
- **WHEN** the PM agent processes it
- **THEN** the PM agent SHALL create an item in Triage with severity, role label, and class of service

## 01 Priority Management

The PM agent balances creation and operations dynamically: a P0 incident preempts feature work; a P2 bug outranks a nice-to-have feature; scheduled releases get fixed-date priority.

### Requirement: Dynamic Priority

The system SHALL have the PM agent order Ready items by class of service (critical → expedite → fixed-date → standard), escalating to the Lead Engineer when SLA breach approaches.

#### Scenario: P0 preempts feature work

- **GIVEN** a P0 incident and in-flight feature work
- **WHEN** the P0 arrives
- **THEN** the PM agent SHALL move the P0 to the front of Ready with critical class
- **AND** feature work SHALL be deprioritized until the incident is resolved

## 02 WIP & SLA Monitoring

The PM agent monitors column WIP, stale items, and SLA thresholds, taking action (notification, escalation) when limits are hit.

### Requirement: WIP and SLA Monitoring

The system SHALL have the PM agent monitor board WIP limits, stale items, and SLA thresholds, triggering configured actions when limits are breached.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140100 | 0.2.0 | Initial PM agent role spec |