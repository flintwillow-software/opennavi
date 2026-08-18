---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - agent-roles
priority: high
spec-refs: [140000, 140001, 140002, 140003]
---

# Role Framework

> What a role is, how roles compose from skills/tools/permissions, and how roles interact with the board and chat.

---

## 00 Role Definition

A role is a navi definition with a fixed skill set, tool set, and permission set, plus board labels it pulls and chat channels it participates in. Roles are defined in `navis/` directories and compiled into binaries ([S000300](../00-navi-core/03-builder-compiler.md)).

| Field | Description |
|-------|-------------|
| `name` | Role name (e.g. `backend-engineer`) |
| `binary` | Compiled binary name |
| `owns` | The domains the role owns (API, UI, infra, reliability) |
| `pullsFrom` | Board column + label filters for work discovery |
| `skills` | Skill set compiled into the role ([S020000](../02-agent-config/00-skills-system.md)) |
| `tools` | Tool set granted to the role ([S130000](../13-tool-catalog/00-tool-catalog-overview.md)) |
| `permissions` | IAM-style policy for what the role can do ([S040200](../04-multi-agent/02-iam-idle-scheduling.md)) |

### Requirement: Role Definition Schema

The system SHALL define agent roles with name, binary, ownership domains, board pull filters, skills, tools, and permissions.

#### Scenario: Defining a role

- **GIVEN** a role definition with a name, skills, tools, and permissions
- **WHEN** the navi is built
- **THEN** the binary SHALL be compiled with exactly those skills, tools, and permissions

## 01 Board Interaction

Roles pull work from the shared board. The pull behavior is role-driven:

```
1. Critical item for my role? → pull immediately (bypass WIP)
2. Expedite item? → pull next (can exceed WIP)
3. At my WIP limit? → don't pull
4. Standard item? → pull if I have capacity
5. No items → back off (exponential, up to 30m)
```

### Requirement: Role-Based Pull Behavior

The system SHALL drive each role's work discovery from the board: pulling by priority (critical, expedite, standard), respecting per-role WIP limits, and backing off with exponential backoff when no work is available.

#### Scenario: Pulling work by role

- **GIVEN** a role with a label filter and available capacity
- **WHEN** the idle scheduler fires
- **THEN** the role SHALL pull the highest-priority item matching its label

## 02 Chat Participation

Roles participate in P2P chat with permission-scoped channels. The IAM model ([S040200](../04-multi-agent/02-iam-idle-scheduling.md)) grants each role the peers it may message — e.g. PM talks to all roles, Reviewer talks to engineers, Support talks to PM and Lead.

### Requirement: Role Chat Permissions

The system SHALL enforce role-scoped chat permissions, so each role can only message peers granted by its IAM policy.

#### Scenario: PM broadcasting to the team

- **GIVEN** a PM role with team-wide chat permission
- **WHEN** the PM broadcasts a critical incident notification
- **THEN** all role agents SHALL receive the signed message

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S140000 | 0.2.0 | Initial role framework spec |