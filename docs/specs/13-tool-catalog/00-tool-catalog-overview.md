---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
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
| `name` | Unique tool name (e.g. `read`, `bash`) |
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
| **Read-only** | Observe state; no modification | read, grep, find, git_diff, git_log, git_status, ast_parse, symbol_search |
| **Write** | Modify files, git state, or config | write, edit, git_commit |
| **Execute** | Run external processes | bash, test_run |

### Requirement: Tool Access Control

The system SHALL enforce access control on catalog tools, allowing read-only tools to run without write permission and requiring explicit write or execute permission for mutating tools.

#### Scenario: Read-only tool without write permission

- **GIVEN** a navi with read-only permissions only
- **WHEN** the agent calls a read-only tool such as `read` or `grep`
- **THEN** the tool SHALL execute normally

#### Scenario: Write tool without write permission

- **GIVEN** a navi without write permission
- **WHEN** the agent calls a write tool such as `edit`
- **THEN** the system SHALL deny the call and return a permission error to the LLM

## 02 Tool Registration

Tools are registered into the navi at build time. The navi definition lists which catalog tools to include; unused tools are compiled out to reduce binary size and attack surface ([S000300](../00-navi-core/03-builder-compiler.md)).

```go
navi := sdk.Build(naviDef{
    Tools: []ToolDefinition{
        {Name: "read", Enabled: true},
        {Name: "bash", Enabled: true},
        {Name: "git_commit", Enabled: true},
    },
})
```

### Requirement: Tool Registration

The system SHALL register catalog tools at build time, compiling only the tools enabled in the navi definition into the binary.

#### Scenario: Enabling a subset of tools

- **GIVEN** a navi definition enables only `read` and `bash`
- **WHEN** the navi is built
- **THEN** only those two tools SHALL be available to the agent
- **AND** the binary SHALL not include the other catalog tools

## 03 Recovery Strategy Mapping

Each catalog tool declares a recovery strategy from [S010400](../01-agent-runtime/04-tool-error-recovery.md):

| Strategy | Applied To |
|----------|-----------|
| Partial output on truncation | read (large files), bash (streaming, timeout) |
| Atomic write + backup on conflict | write |
| No-match returns unchanged; partial on overlap | edit |
| Rollback on failure | git_commit |
| Capture stdout/stderr + timeout | bash, test_run |
| N/A (pure reads) | grep, find, git_diff, git_log, git_status, ast_parse, symbol_search |

### Requirement: Per-Tool Recovery Strategy

The system SHALL apply the declared recovery strategy per tool, so a failed tool returns its error AND any partial output to the LLM for recovery decisions.

#### Scenario: Bash timeout partial output

- **GIVEN** a `bash` tool call that times out after producing partial output
- **WHEN** the tool fails
- **THEN** the system SHALL return the partial output with a truncation notice and the timeout error

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S130000 | 0.2.0 | Initial tool catalog overview spec |