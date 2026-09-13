---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec-tree
  - component
  - agent-roles
priority: high
spec-refs: [140000, 140100, 140200, 140300, 140400, 140500, 140600, 140700, 140800, 140900]
---

# 14 Agent Roles

> The standard role-based agent definitions for product lifecycle teams — nine specialized roles (PM, Support, SRE, Lead, Backend, Frontend, DevOps, QA, Reviewer) that coordinate via P2P chat and pull work from a shared board.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S140000 Role Framework](00-role-framework.md) | Role Framework | draft | Role composition, board access, chat permissions |
| [S140100 PM Agent](01-pm-agent.md) | PM Agent | draft | Triage, priority, WIP, SLA, releases |
| [S140200 Support Engineer](02-support-engineer.md) | Support Engineer | draft | User reports, triage, comms, feature intake |
| [S140300 SRE Engineer](03-sre-engineer.md) | SRE Engineer | draft | Reliability, incident coordination, post-mortems |
| [S140400 Lead Engineer](04-lead-engineer.md) | Lead Engineer | draft | Runbooks, ADRs, standards, escalation |
| [S140500 Backend Engineer](05-backend-engineer.md) | Backend Engineer | draft | API, services, data model, migrations |
| [S140600 Frontend Engineer](06-frontend-engineer.md) | Frontend Engineer | draft | UI, UX, frontend tests, a11y |
| [S140700 DevOps Engineer](07-devops-engineer.md) | DevOps Engineer | draft | CI/CD, infra, packaging, monitoring config |
| [S140800 QA Engineer](08-qa-engineer.md) | QA Engineer | draft | Verification, regression, acceptance testing |
| [S140900 Reviewer](09-reviewer.md) | Reviewer | draft | Code review across domains |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Each role is a compiled navi binary with its own identity key, presence, chat permissions, and idle schedule
- Roles pull work from the shared board by label (backend, frontend, devops, sre, support) or column (Verify, In Review)
- Role behavior is composed from skills ([S020000](../02-agent-config/00-skills-system.md)) and tools ([S130000](../13-tool-catalog/00-tool-catalog-overview.md))
- Role definitions are code, not config — a navi role is a Go navi definition (compiled via `sdk.Build`, per S000200) plus skills, no new runtime machinery
- Board mechanics (columns, WIP, classes of service, SLA) live in the board system, not in the role specs

## Related

- [Agent Config](../02-agent-config/README.md)
- [Tool Catalog](../13-tool-catalog/README.md)
- [Multi-Agent](../04-multi-agent/README.md)
- [Operations](../08-operations/README.md)