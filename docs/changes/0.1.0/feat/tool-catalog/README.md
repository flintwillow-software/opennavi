# 0.1.0 — feat: tool-catalog

> Derived from spec tree components 00 (SDK Surface, S000800) and 13 (Tool Catalog). Implements the first public SDK surface: the tool registry + the default tool set that ships with every navi binary.

## Summary

Implement the tool registry and catalog as the **first public SDK surface** (S000800): self-describing `ToolDefinition` metadata, registered-vs-active registry with provenance, and the standard tool set — file operations (`file_read`/`file_write`/`file_edit`), shell (`shell_exec`), git (`git_diff`/`git_log`/`git_status`/`git_commit`), search & code intelligence (`file_grep`/`file_find`/`code_ast_parse`/`code_symbol_search`), and test runner (`test_run`). Tools compile into every navi binary; author and extension tools register through the same public registry.

## Changes

- **`tools/registry/`** (public SDK) — `ToolDefinition` metadata contract: name, description, inputSchema, outputFormat, access, timeout, recovery, mode, namespace, tags, version, promptSnippet, promptGuidelines, sourceInfo (provenance). Registry with `Get`/`List`/`Find`/`AllNames`/`Active`/`SetActive`, duplicate rejection, schema validation before dispatch, `prepareArguments` hook. (S130400)
- **`tools/file/`** (public SDK) — `file_read`, `file_write`, `file_edit`, `file_find`, `file_grep`. Write with atomic file operations and backup on conflict. Edit with exact text matching. Absorbs existing `internal/agent/tools.go` `file_read` + `shell_exec`.
- **`tools/shell/`** (public SDK) — `shell_exec`: bash execution with stdout/stderr capture, timeout, partial output on failure. (`shell_zsh_exec`/`shell_fish_exec` variants slot in later.)
- **`tools/git/`** (public SDK) — `git_diff`, `git_log`, `git_status`, `git_commit` via **go-git (pure Go, no cgo)**.
- **`tools/search/`** (public SDK) — `file_grep` (pattern search), `file_find` (file pattern search), `code_ast_parse`, `code_symbol_search` via **Go stdlib (`go/parser`), no tree-sitter/cgo**.
- **`tools/test/`** (public SDK) — `test_run`: detect project type (Go, Node, Python, Rust), run test suite, capture output.

## Spec Refs

[000800, 130000, 130100, 130200, 130300, 130400]

## Code Context

- **Project structure**: opennavi module is the SDK — public `tools/` packages plus `sdk/`, `agent/`, `store/`, `llm/`; `internal/` stays closed (S000800 §01)
- **Dependencies**: go-git (pure Go git), Go stdlib only for search/AST — **NO cgo** (project-wide CGO_ENABLED=0: modernc sqlite, CI, bastilli Pi, Docker cross-build)
- **Existing patterns**: `internal/agent/tools.go` currently ships `file_read` + `shell_exec` — absorbed into the catalog, keeping the `namespace_action` naming (`file_read`/`shell_exec` already conform)
- **Runtime seam**: registry plugs into `internal/agent` `ToolExecutor` (`RegisterTool`/`Tools()`) — the catalog registers via the public registry; executor converts to provider tool definitions

## Notes

- Epic: "Core Tool Catalog"
- Depends on: agent-runtime (tool schema, dispatch, error recovery), SDK Surface (S000800) for the public boundary
- **Build order**: registry (#205) first — defines `ToolDefinition`/provenance/active-set every other tool hangs off; then file+shell (#200/#201, absorb existing); then git (#202); then search (#203) + test (#204); wire as default active set
- No cgo: libgit2 → go-git; tree-sitter → Go stdlib (S000800 consistency + CGO_ENABLED=0 constraint)
- Naming: `namespace_action`, fully descriptive (`file_read` not `read`); namespaces: `file`, `shell`, `git`, `code`, `test`, and author domains (`github`, `plane`, …)
- Author vs agent tools: distinguished by namespace + provenance, not Go visibility — catalog is public so navi authors can compose/override

## Tracked

- Epic: Core Tool Catalog (#199) — https://github.com/flintwillow-software/opennavi/issues/199 [priority: P1]
  - Issue: Implement tool registry (#205) — https://github.com/flintwillow-software/opennavi/issues/205 [priority: P1]
  - Issue: Implement file operations (#200) — https://github.com/flintwillow-software/opennavi/issues/200 [priority: P1]
  - Issue: Implement shell tool (#201) — https://github.com/flintwillow-software/opennavi/issues/201 [priority: P1]
  - Issue: Implement git tools (#202) — https://github.com/flintwillow-software/opennavi/issues/202 [priority: P1]
  - Issue: Implement search tools (#203) — https://github.com/flintwillow-software/opennavi/issues/203 [priority: P1]
  - Issue: Implement test runner (#204) — https://github.com/flintwillow-software/opennavi/issues/204 [priority: P1]