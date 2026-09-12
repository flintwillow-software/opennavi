---
status: active
created: '2026-08-15'
updated: '2026-09-12'
tags:
  - spec
  - tool-catalog
priority: high
spec-refs: [130000, 130001, 130002, 130003]
---

# Tool Catalog Overview

> The catalog concept — what a tool is, the schema every tool declares, access control levels, and how tools are registered into a navi.

---

## 00 Overview

The tool catalog is the standard set of tools compiled into every navi binary. Each tool is a named instance of the `AgentTool` interface ([S010301](../01-agent-runtime/03-agent-runtime.md)) that declares:

| Field | Description |
|-------|-------------|
| `name` | Unique tool name (e.g. `file_read`, `shell_exec`) |
| `description` | LLM-facing description of what the tool does |
| `inputSchema` | Typed JSON schema for arguments |
| `outputFormat` | Format hint for rendering (`text`, `json`, `diff`, `markdown`) |
| `access` | Read-only, write, or execute |
| `timeout` | Category-appropriate timeout (from S010400) |
| `recovery` | Error recovery strategy (from S010400) |
| `mode` | Parallel or sequential execution capability |

### Requirement: Catalog Tool Schema

The system SHALL define every catalog tool with a typed schema covering name, description, input parameters, output format, access level, timeout, recovery strategy, and execution mode.

#### Scenario: Declaring a catalog tool

- **GIVEN** a navi definition includes a catalog tool
- **WHEN** the tool is registered with the agent runtime
- **THEN** the tool SHALL expose its typed input schema, description, access level, and recovery strategy to the LLM

## 01 Access Control Levels

Tools are classified into three access levels, enforced by the IAM permission model ([S040200](../04-multi-agent/02-iam-idle-scheduling.md)):

| Level | Definition | Catalog Tools |
|-------|------------|---------------|
| **Read-only** | Observe state; no modification | file_read, file_grep, file_find, git_diff, git_log, git_status, code_ast_parse, code_symbol_search, todo_read, time_read, note_read |
| **Write** | Modify files, git state, or config | file_write, file_edit, git_commit, todo_write, todo_update, note_write |
| **Execute** | Run external processes | shell_exec, test_run, http_request |

### Requirement: Tool Access Control

The system SHALL enforce access control on catalog tools, allowing read-only tools to run without write permission and requiring explicit write or execute permission for mutating tools.

#### Scenario: Read-only tool without write permission

- **GIVEN** a navi with read-only permissions only
- **WHEN** the agent calls a read-only tool such as `file_read` or `file_grep`
- **THEN** the tool SHALL execute normally (e.g. `file_read` or `file_grep`)

#### Scenario: Write tool without write permission

- **GIVEN** a navi without write permission
- **WHEN** the agent calls a write tool such as `file_edit`
- **THEN** the system SHALL deny the call and return a permission error to the LLM

## 02 Tool Registration

Tools are registered into the navi at build time. The navi definition lists which catalog tools to include; unused tools are compiled out to reduce binary size and attack surface ([S000300](../00-navi-core/03-builder-compiler.md)).

```go
navi := sdk.Build(naviDef{
    Tools: []ToolDefinition{
        {Name: "file_read", Enabled: true},
        {Name: "shell_exec", Enabled: true},
        {Name: "git_commit", Enabled: true},
    },
})
```

### Requirement: Tool Registration

The system SHALL register catalog tools at build time, compiling only the tools enabled in the navi definition into the binary.

#### Scenario: Enabling a subset of tools

- **GIVEN** a navi definition enables only `file_read` and `shell_exec`
- **WHEN** the navi is built
- **THEN** only those two tools SHALL be available to the agent
- **AND** the binary SHALL not include the other catalog tools

## 03 Recovery Strategy Mapping

Each catalog tool declares a recovery strategy from [S010400](../01-agent-runtime/04-tool-error-recovery.md):

| Strategy | Applied To |
|----------|-----------|
| Partial output on truncation | file_read (large files), shell_exec (streaming, timeout) |
| Atomic write + backup on conflict | file_write |
| No-match returns unchanged; partial on overlap | file_edit |
| Rollback on failure | git_commit |
| Capture stdout/stderr + timeout | shell_exec, test_run |
| Atomic transaction + rollback on failure | todo_write, todo_update, note_write |
| Capture response on failure | http_request |
| N/A (pure reads) | file_grep, file_find, git_diff, git_log, git_status, code_ast_parse, code_symbol_search, todo_read, time_read, note_read |

### Requirement: Per-Tool Recovery Strategy

The system SHALL apply the declared recovery strategy per tool, so a failed tool returns its error AND any partial output to the LLM for recovery decisions.

#### Scenario: Bash timeout partial output

- **GIVEN** a `shell_exec` tool call that times out after producing partial output
- **WHEN** the tool fails
- **THEN** the system SHALL return the partial output with a truncation notice and the timeout error

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S130000 | 0.2.0 | Initial tool catalog overview spec |
| 2026-09-12 | S130000 | 0.2.1 | Renamed tools to namespaced names per S130400 §04 (file_read, shell_exec, file_grep, file_find, code_ast_parse, code_symbol_search) |
| 2026-09-12 | S130000 | 0.2.2 | Added agent utility tools to access-control and recovery mappings (S130500) |