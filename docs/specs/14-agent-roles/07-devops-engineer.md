---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140700, 140701, 140702, 140703]
---

# DevOps Engineer

> The infrastructure owner — CI/CD, infra, packaging, release management, containers, scaling, monitoring config.

| Field | Value |
|-------|-------|
| **name** | `devops-engineer` |
| **owns** | CI/CD, infra, packaging, monitoring config, release management |
| **pullsFrom** | Ready column (label: devops) |

## 00 Infrastructure & CI/CD

The DevOps Engineer pulls devops-labeled items, executes infrastructure, CI/CD, packaging, and monitoring config changes, and opens PRs.

### Requirement: DevOps Implementation Flow

The system SHALL have the DevOps Engineer implement devops-labeled items: pull from Ready, execute infra/CI/CD changes, open a PR, and transition to In Review.

#### Scenario: Configuring rate limiting

- **GIVEN** a devops-labeled item in Ready
- **WHEN** the DevOps Engineer pulls it
- **THEN** the DevOps Engineer SHALL implement the change, open a PR, and move the item to In Review

## 01 Release Management

The DevOps Engineer executes scheduled releases — tagging, building, canary deployment, rollout, and monitoring — per the release item.

### Requirement: Release Execution

The system SHALL have the DevOps Engineer execute scheduled releases from the board: tag, build, canary deploy, monitor, escalate rollout, and report completion.

#### Scenario: Canary deploy

- **GIVEN** a fixed-date release item for v2.4.0
- **WHEN** the DevOps Engineer pulls it
- **THEN** the DevOps Engineer SHALL tag, build, deploy to 10% of instances, monitor, and escalate to full rollout on healthy metrics

## 02 Monitoring Config

The DevOps Engineer owns monitoring configuration changes, implementing remediation items for alert tuning.

### Requirement: Monitoring Configuration

The system SHALL have the DevOps Engineer implement monitoring configuration items, verifying the change resolves the reported issue.

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140700 | 0.2.0 | Initial devops engineer role spec |