---
status: draft
created: '2026-09-12'
updated: '2026-09-12'
tags:
  - case-study
  - tool-catalog
  - agent-utility-tools
priority: medium
---

# Agent Utility Tools

> Add the everyday agent affordances to the tool catalog: a durable todo list for plan tracking, wall-clock awareness (`time_read`), a persistent notes scratchpad (`note_read`/`note_write`), and a general outbound HTTP client (`http_request`) capable of full CRUD against external APIs.

## Summary

The standard tool catalog (13-tool-catalog) covers file, shell, git, code, and test tools. Every production agent harness also ships a small set of *utility* tools that agents use constantly regardless of domain: task/progress tracking, current time, a persistent scratchpad, and the ability to call external HTTP APIs. This case study proposes adding four tool namespaces to the catalog:

| Namespace | Tools | Access | Stateful? |
|-----------|-------|--------|-----------|
| `todo` | `todo_read`, `todo_write`, `todo_update` | read-only / write | per-navi durable (store layer) |
| `time` | `time_read` | read-only | stateless |
| `note` | `note_read`, `note_write` | read-only / write | per-navi durable (store layer) |
| `http` | `http_request` | execute (decision point) | stateless |

All seven tools implement the public SDK `ToolDefinition` contract (S130400) and register through the same registry as the existing catalog.

## Quick Reference

- **Case study folder**: `docs/case-study/agent-utility-tools/`
- **Status**: Draft
- **Target spec component**: `13-tool-catalog` (new features) + touches `01-agent-runtime` (storage seam)
- **Downstream**: d3-specs → d3-changes → d3-issues

## Ownership Map

| Module | Role | Related case study files |
|--------|------|--------------------------|
| `tools/todo/` (new, public SDK) | todo_read/write/update implementations | SOLUTION.md §todo |
| `tools/time/` (new, public SDK) | time_read implementation | SOLUTION.md §time |
| `tools/note/` (new, public SDK) | note_read/note_write implementations | SOLUTION.md §note |
| `tools/http/` (new, public SDK) | http_request implementation, SSRF guard | SOLUTION.md §http |
| `store/` (existing) | durable per-navi persistence for todos and notes | SOLUTION.md §storage |
| `internal/agent` (existing) | registry wiring, default active set | SOLUTION.md §registration |
| credential resolver (S100400) | `http_request` auth reference resolution | SOLUTION.md §http |

## Status History

| Date | Change |
|------|--------|
| 2026-09-12 | Initial case study — todo, time, note, http namespaces |

## Related

- [13 Tool Catalog spec component](../specs/13-tool-catalog/README.md)
- [S130400 Tool Registry & Discovery](../specs/13-tool-catalog/04-tool-registry.md)
- [S010400 Tool Error Recovery](../specs/01-agent-runtime/04-tool-error-recovery.md)
- [S040200 IAM / permission model](../specs/04-multi-agent/02-iam-idle-scheduling.md)
- [S100400 Credential Resolver](../specs/10-integrations/04-credential-resolver.md)