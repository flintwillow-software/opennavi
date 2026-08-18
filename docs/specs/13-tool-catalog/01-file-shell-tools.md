---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - tool-catalog
priority: high
spec-refs: [130100, 130101, 130102, 130103, 130104]
---

# File & Shell Tools

> The `read`, `write`, `edit`, and `bash` tools — file system operations and shell execution with per-tool schemas and recovery strategies.

---

## 00 `read`

Reads a file's contents. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `read` |
| **inputSchema** | `{ "path": string, "offset"?: int, "limit"?: int }` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 10s |
| **recovery** | Partial output on truncation |

### Requirement: Read File

The system SHALL provide a `read` tool that returns file contents, supporting optional offset and limit for partial reads.

#### Scenario: Reading a file

- **GIVEN** a file at a path the navi can access
- **WHEN** the agent calls `read` with that path
- **THEN** the tool SHALL return the file's contents

#### Scenario: Reading a large file with offset

- **GIVEN** a file larger than the output limit
- **WHEN** the agent calls `read` with an offset
- **THEN** the tool SHALL return the requested slice
- **AND** the tool SHALL mark the output partial if the file extends beyond the limit

## 01 `write`

Writes content to a file. Write access.

| Field | Value |
|-------|-------|
| **name** | `write` |
| **inputSchema** | `{ "path": string, "content": string }` |
| **outputFormat** | `text` |
| **access** | write |
| **timeout** | 10s |
| **recovery** | Atomic write, backup on conflict |

### Requirement: Write File

The system SHALL provide a `write` tool that writes content to a path using an atomic write, preserving a backup when a file already exists.

#### Scenario: Writing a new file

- **GIVEN** a path that does not exist
- **WHEN** the agent calls `write` with content
- **THEN** the tool SHALL create the file atomically with the given content

#### Scenario: Overwriting an existing file

- **GIVEN** a path that already exists
- **WHEN** the agent calls `write` with new content
- **THEN** the tool SHALL write the new content atomically
- **AND** the tool SHALL keep a backup of the previous content for recovery

## 02 `edit`

Applies precise text edits to a file. Write access.

| Field | Value |
|-------|-------|
| **name** | `edit` |
| **inputSchema** | `{ "path": string, "edits": [{ "oldText": string, "newText": string }] }` |
| **outputFormat** | `diff` |
| **access** | write |
| **timeout** | 10s |
| **recovery** | Unchanged if no match, partial on overlap |

### Requirement: Edit File

The system SHALL provide an `edit` tool that applies exact text replacements, leaving the file unchanged when a pattern does not match.

#### Scenario: Applying a matching edit

- **GIVEN** a file containing the exact `oldText`
- **WHEN** the agent calls `edit` with that replacement
- **THEN** the tool SHALL apply the replacement and return a diff

#### Scenario: Non-matching edit

- **GIVEN** a file that does not contain the `oldText`
- **WHEN** the agent calls `edit`
- **THEN** the tool SHALL leave the file unchanged
- **AND** the tool SHALL return an error identifying the non-matching edit

## 03 `bash`

Executes shell commands, capturing stdout, stderr, and exit status. Execute access.

| Field | Value |
|-------|-------|
| **name** | `bash` |
| **inputSchema** | `{ "cmd": string, "timeout"?: int, "cwd"?: string }` |
| **outputFormat** | `bash` |
| **access** | execute |
| **timeout** | 60s (default, configurable per call) |
| **recovery** | Capture stdout/stderr, timeout |

### Requirement: Execute Shell Command

The system SHALL provide a `bash` tool that executes a shell command, captures stdout and stderr, and returns both along with the exit status.

#### Scenario: Running a command

- **GIVEN** a shell command
- **WHEN** the agent calls `bash` with that command
- **THEN** the tool SHALL execute it in the configured working directory
- **AND** the tool SHALL return stdout, stderr, and exit status

#### Scenario: Command timeout

- **GIVEN** a command that runs longer than the timeout
- **WHEN** the tool hits the timeout
- **THEN** the tool SHALL terminate the process
- **AND** the tool SHALL return the partial output with a truncation notice and a timeout error

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S130100 | 0.2.0 | Initial file & shell tools spec |