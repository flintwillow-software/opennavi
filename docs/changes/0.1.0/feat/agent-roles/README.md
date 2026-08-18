# 0.1.0 — feat: agent-roles

> Derived from initial spec tree component 14 (Agent Roles). Implements the role-based agent definitions for the autonomous lifecycle team.

## Summary

Implement the agent role system: role definitions (PM, Support, Lead Engineer, Backend Engineer, Frontend Engineer, DevOps Engineer, SRE, QA Engineer, Reviewer), role-specific tool subsets, role-based access control, and the deployment configuration for role-specific agent binaries.

## Changes

- **`pkg/roles/`** — Role definitions: Data model for role types, associated tool permissions, default idle schedules, and chat permissions.
- **`pkg/roles/pm/`** — PM Agent: Board management tools, triage logic, WIP monitoring, SLA checking, release coordination.
- **`pkg/roles/support/`** — Support Engineer: User report ingestion, triage, communication templates, feature intake.
- **`pkg/roles/lead/`** — Lead Engineer: Runbook management, post-mortem templates, ADR workflow, standards enforcement.
- **`pkg/roles/backend/`** — Backend Engineer: API, services, data model, migration tool permissions.
- **`pkg/roles/frontend/`** — Frontend Engineer: UI, UX, frontend test, a11y tool permissions.
- **`pkg/roles/devops/`** — DevOps Engineer: CI/CD, infra, packaging, monitoring config tool permissions.
- **`pkg/roles/sre/`** — SRE: Incident response tools, reliability checks, SLI/SLO management.
- **`pkg/roles/qa/`** — QA Engineer: Test verification, regression detection, acceptance testing tools.
- **`pkg/roles/reviewer/`** — Reviewer: Code review tools, diff analysis, style checking, cross-domain review.

## Spec Refs

[140000, 140100, 140200, 140300, 140400, 140500, 140600, 140700, 140800, 140900]

## Code Context

- **Project structure**: Go SDK, builds on agent-runtime and multi-agent
- **Dependencies**: Same as navi-core (each role is a compiled binary using the same SDK)
- **Existing patterns**: Each role is a separate compiled binary built from the same SDK with a role-specific tool set

## Notes

- Epic: "Role-Based Agent System" (feeds into autonomous lifecycle team)
- Depends on: agent-runtime (agent loop), multi-agent (P2P), messaging (notifications), tool-catalog (shared tools)
- Role binaries share the same SDK — the difference is in tool set, permissions, and behavior presets
- The PM Agent is the most complex role (board management, triage logic) and should be built first