# 0.1.0 — feat: tool-catalog

> Derived from initial spec tree component 13 (Tool Catalog). Implements the built-in tool set that ships with every navi binary.

## Summary

Implement the tool catalog: file and shell operations (read, write, edit, bash), git tools (diff, log, status, commit), and developer tools (grep, find, AST parse, symbol search, test runner). These tools are compiled into every navi binary as the default tool set.

## Changes

- **`pkg/tools/file/`** — File operations: read, write, edit tools. Write with atomic file operations and backup on conflict. Edit with exact text matching.
- **`pkg/tools/shell/`** — Shell tool: bash execution with stdout/stderr capture, timeout, partial output on failure.
- **`pkg/tools/git/`** — Git tools: diff, log, status, commit via libgit2 bindings.
- **`pkg/tools/search/`** — Search tools: grep (pattern search), find (file pattern search), AST parse, symbol search.
- **`pkg/tools/test/`** — Test runner: Detect project type (Go, Node, Python, Rust), run test suite, capture output.
- **`pkg/tools/registry/`** — Tool registry: Schema definitions, access control metadata, registration with the runtime tool system.

## Spec Refs

[130000, 130100, 130200, 130300]

## Code Context

- **Project structure**: Go SDK, builds on agent-runtime tool system
- **Dependencies**: libgit2 (git operations), tree-sitter (AST parsing for multiple languages), test detection per language
- **Existing patterns**: Each tool implements a typed schema (name, description, parameters, access control, error recovery)

## Notes

- Epic: "Core Tool Catalog"
- Depends on: agent-runtime (tool schema, dispatch, error recovery)
- Tools are independent — file ops, git, search, test can be built in parallel once the tool schema interface is defined
- git tools via libgit2 is preferred over shelling out to avoid CLI parsing fragility

## Tracked

- Epic: Core Tool Catalog (#199) — https://github.com/flintwillow-software/opennavi/issues/199 [priority: P1]
  - Issue: Implement file operations (#200) — https://github.com/flintwillow-software/opennavi/issues/200 [priority: P1]
  - Issue: Implement shell tool (#201) — https://github.com/flintwillow-software/opennavi/issues/201 [priority: P1]
  - Issue: Implement git tools (#202) — https://github.com/flintwillow-software/opennavi/issues/202 [priority: P1]
  - Issue: Implement search tools (#203) — https://github.com/flintwillow-software/opennavi/issues/203 [priority: P1]
  - Issue: Implement test runner (#204) — https://github.com/flintwillow-software/opennavi/issues/204 [priority: P1]
  - Issue: Implement tool registry (#205) — https://github.com/flintwillow-software/opennavi/issues/205 [priority: P1]
