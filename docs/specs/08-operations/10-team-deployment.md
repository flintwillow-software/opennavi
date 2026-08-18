---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - operations
priority: high
spec-refs: [081000, 081001, 081002, 081003]
---

# Team Deployment

> Deploying a team of role-based navi agents as systemd services — building and signing each agent binary, running them as services, and scaling by role. Extends [S080500 Deployment & Operations](../08-operations/05-deployment-and-operations.md).

---

## 00 Build & Sign Each Agent

Each team agent is built as a signed binary from its role definition ([S140000 Agent Roles](../14-agent-roles/00-role-framework.md)):

```bash
opennavi build ./navis/pm-agent            → pm-agent-linux-amd64
opennavi build ./navis/sre-engineer        → sre-engineer-linux-amd64
opennavi build ./navis/backend-engineer    → backend-engineer-linux-amd64
# ... one per role

opennavi sign pm-agent-linux-amd64 --key ./keys/deploy.key
# ... and every other binary
```

### Requirement: Per-Role Builds

The system SHALL build and sign one binary per role definition in the team, so each role runs as an independent signed executable.

#### Scenario: Building a team

- **GIVEN** a team with nine role definitions
- **WHEN** the team is deployed
- **THEN** nine signed binaries SHALL be produced, one per role

## 01 Systemd Services

Each binary runs as its own systemd service with its own keypair, presence, and thought queue. The PM agent discovers other agents via presence ([S040000 P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md)).

```bash
systemctl enable opennavi@pm-agent
systemctl enable opennavi@backend-engineer
# ... one per role
```

### Requirement: Per-Role Services

The system SHALL run each role binary as an independent systemd service with its own identity key, presence, and thought queue.

#### Scenario: Starting the team

- **GIVEN** a deployed team
- **WHEN** the host boots
- **THEN** each role's systemd service SHALL start its binary with its own identity and presence

## 02 Horizontal Scaling

Roles can be scaled horizontally — multiple instances of a role (e.g. 3 backend engineers) share the role's pull label and distribute work by per-role WIP limits ([S040200 IAM & Idle Scheduling](../04-multi-agent/02-iam-idle-scheduling.md)).

```bash
systemctl enable opennavi@backend-engineer-1
systemctl enable opennavi@backend-engineer-2
systemctl enable opennavi@backend-engineer-3
```

### Requirement: Horizontal Scaling

The system SHALL support running multiple instances of a role, with each instance having its own keypair, presence, and thought queue, and work distribution enforced by the role's WIP limits.

#### Scenario: Scaling reviewers

- **GIVEN** the In Review column is always full
- **WHEN** a second reviewer instance starts
- **THEN** the additional reviewer SHALL pull from the same In Review column with the same class-priority behavior

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S081000 | 0.2.0 | Initial team deployment spec |