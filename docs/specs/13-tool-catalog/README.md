---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec-tree
  - component
  - tool-catalog
priority: high
spec-refs: [130000, 130100, 130200, 130300]
---

# 13 Tool Catalog

> The standard built-in tool set shipped with every navi — file operations, shell, git, search, test, and code-intelligence tools with typed schemas, access control, and per-tool error recovery strategies.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S130000 Tool Catalog Overview](00-tool-catalog-overview.md) | Tool Catalog Overview | draft | Catalog concept, schema, access control, registration |
| [S130100 File & Shell Tools](01-file-shell-tools.md) | File & Shell Tools | draft | read, write, edit, bash schemas + recovery |
| [S130200 Git Tools](02-git-tools.md) | Git Tools | draft | git_diff, git_log, git_status, git_commit schemas + recovery |
| [S130300 Search, Test & Code Intelligence](03-search-test-code-intel.md) | Search, Test & Code Intelligence | draft | grep, find, test_run, ast_parse, symbol_search schemas + recovery |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Tools implement the `AgentTool` interface from [Agent Runtime](../01-agent-runtime/03-agent-runtime.md) — the catalog defines named instances with schemas
- Three access levels: read-only, write, execute — enforced by IAM permissions ([S040200](../04-multi-agent/02-iam-idle-scheduling.md))
- Error recovery strategies reuse the framework from [S010400 Tool Error Recovery](../01-agent-runtime/04-tool-error-recovery.md)
- Tools are compiled into the binary at build time ([S000300 Builder & Compiler](../00-navi-core/03-builder-compiler.md))
- Custom tools ship via the extension contract ([S000600](../00-navi-core/06-extension-contract.md)) — the catalog covers the standard set

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
- [Navi Core](../00-navi-core/README.md)
- [Multi-Agent](../04-multi-agent/README.md)