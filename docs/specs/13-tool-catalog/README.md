---
status: active
created: '2026-08-15'
updated: '2026-09-12'
tags:
  - spec-tree
  - component
  - tool-catalog
priority: high
spec-refs: [130000, 130100, 130200, 130300, 130400]
---

# 13 Tool Catalog

> The standard built-in tool set shipped with every navi — file operations, shell, git, search, test, and code-intelligence tools with typed schemas, access control, per-tool error recovery, and the SDK registry/discovery surface.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S130000 Tool Catalog Overview](00-tool-catalog-overview.md) | Tool Catalog Overview | active | Catalog concept, schema, access control, registration |
| [S130100 File & Shell Tools](01-file-shell-tools.md) | File & Shell Tools | active | file_read/write/edit, shell_exec schemas + recovery |
| [S130200 Git Tools](02-git-tools.md) | Git Tools | active | git_diff, git_log, git_status, git_commit schemas + recovery |
| [S130300 Search, Test & Code Intelligence](03-search-test-code-intel.md) | Search, Test & Code Intelligence | active | file_grep, file_find, test_run, code_ast_parse, code_symbol_search schemas + recovery |
| [S130400 Tool Registry & Discovery](04-tool-registry.md) | Tool Registry & Discovery | active | SDK ToolDefinition, registered vs active, provenance, discovery queries, namespace naming, validation |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Tools implement the public SDK `ToolDefinition` from [S130400 Tool Registry](04-tool-registry.md) — the catalog defines named, self-describing instances with schemas, provenance, and active-set membership
- The tool packages are the first public SDK surface (S000800) — `tools/registry` + catalog packages importable by navi authors, distinct from author/application tools by namespace and provenance
- CGO_ENABLED=0 constraint holds across the catalog: git tools use go-git (pure Go), AST/code tools use the Go stdlib, no cgo bindings
- Three access levels: read-only, write, execute — enforced by IAM permissions ([S040200](../04-multi-agent/02-iam-idle-scheduling.md))
- Error recovery strategies reuse the framework from [S010400 Tool Error Recovery](../01-agent-runtime/04-tool-error-recovery.md)
- Tools are compiled into the binary at build time ([S000300 Builder & Compiler](../00-navi-core/03-builder-compiler.md)); runtime standplugin extensions register through the same registry (S000600)
- Custom/author tools ship via the SDK registry or the extension contract — the catalog covers the standard set

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
- [Navi Core](../00-navi-core/README.md)
- [Multi-Agent](../04-multi-agent/README.md)