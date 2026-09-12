---
status: active
created: '2026-08-15'
updated: '2026-09-12'
tags:
  - spec
  - tool-catalog
priority: high
spec-refs: [130300, 130301, 130302, 130303, 130304, 130305]
---

# Search, Test & Code Intelligence Tools

> The `file_grep`, `file_find`, `test_run`, `code_ast_parse`, and `code_symbol_search` tools — file search, test execution, and source code understanding with typed schemas and recovery strategies.

---

## 00 `file_grep`

Searches file contents with patterns. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `file_grep` |
| **inputSchema** | `{ "pattern": string, "path"?: string, "include"?: [string], "exclude"?: [string], "ignoreCase"?: bool }` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 30s |
| **recovery** | N/A (pure read) |

### Requirement: Search File Contents

The system SHALL provide a `file_grep` tool that searches file contents with a pattern, supporting path scope, include/exclude globs, and case-insensitive matching.

#### Scenario: Searching a directory

- **GIVEN** a directory tree with source files
- **WHEN** the agent calls `file_grep` with a pattern
- **THEN** the tool SHALL return matching lines with file paths and line numbers

#### Scenario: Scoping a search

- **GIVEN** a large repository
- **WHEN** the agent calls `file_grep` with a path and include filter
- **THEN** the tool SHALL restrict the search to matching files under the path

## 01 `file_find`

Finds files by name or pattern. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `file_find` |
| **inputSchema** | `{ "path"?: string, "name"?: string, "type"?: "file" \| "dir", "maxDepth"?: int }` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 30s |
| **recovery** | N/A (pure read) |

### Requirement: Find Files

The system SHALL provide a `file_find` tool that locates files by name or glob pattern, with optional type filter and depth limit.

#### Scenario: Finding a file by name

- **GIVEN** a project tree
- **WHEN** the agent calls `file_find` with a name pattern
- **THEN** the tool SHALL return the matching paths

## 02 `test_run`

Runs the test suite (or a subset). Execute access.

| Field | Value |
|-------|-------|
| **name** | `test_run` |
| **inputSchema** | `{ "command"?: string, "path"?: string, "timeout"?: int }` |
| **outputFormat** | `text` |
| **access** | execute |
| **timeout** | 300s (default, configurable per call) |
| **recovery** | Capture output, timeout |

### Requirement: Run Tests

The system SHALL provide a `test_run` tool that runs the project test suite, capturing output and the pass/fail summary, and terminating on timeout.

#### Scenario: Running the test suite

- **GIVEN** a project with a test suite
- **WHEN** the agent calls `test_run`
- **THEN** the tool SHALL run the suite and return the output with pass/fail summary

#### Scenario: Test timeout

- **GIVEN** a test run that exceeds the timeout
- **WHEN** the tool hits the timeout
- **THEN** the tool SHALL terminate the run
- **AND** the tool SHALL return the partial output with a timeout error

## 03 `code_ast_parse`

Parses source code into an abstract syntax tree. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `code_ast_parse` |
| **inputSchema** | `{ "path": string, "lang"?: string }` |
| **outputFormat** | `json` |
| **access** | read-only |
| **timeout** | 30s |
| **recovery** | N/A (pure read) |

### Requirement: Parse Source to AST

The system SHALL provide a `code_ast_parse` tool that parses a source file into an abstract syntax tree, with optional language hint for polyglot projects.

#### Scenario: Parsing a source file

- **GIVEN** a source file in a supported language
- **WHEN** the agent calls `code_ast_parse` with the path
- **THEN** the tool SHALL return the AST as structured JSON

#### Scenario: Unsupported language

- **GIVEN** a file in an unsupported language
- **WHEN** the agent calls `code_ast_parse`
- **THEN** the tool SHALL return an error identifying the unsupported language

## 04 `code_symbol_search`

Finds symbol definitions across the codebase. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `code_symbol_search` |
| **inputSchema** | `{ "name": string, "kind"?: string, "path"?: string }` |
| **outputFormat** | `json` |
| **access** | read-only |
| **timeout** | 30s |
| **recovery** | N/A (pure read) |

### Requirement: Search Symbol Definitions

The system SHALL provide a `code_symbol_search` tool that finds symbol definitions (functions, types, variables) by name, with optional kind and path scope, returning file and position for each match.

#### Scenario: Finding a function definition

- **GIVEN** a codebase with a function named `handleRequest`
- **WHEN** the agent calls `code_symbol_search` with that name
- **THEN** the tool SHALL return the defining file, line, and signature

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S130300 | 0.2.0 | Initial search, test & code intelligence tools spec |
| 2026-09-12 | S130300 | 0.2.1 | Renamed tools to namespaced names per S130400 §04 (file_grep, file_find, code_ast_parse, code_symbol_search) |