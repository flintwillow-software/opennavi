---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - tool-catalog
priority: high
spec-refs: [130200, 130201, 130202, 130203, 130204]
---

# Git Tools

> The `git_diff`, `git_log`, `git_status`, and `git_commit` tools — git repository inspection and commit creation with typed schemas and recovery strategies.

---

## 00 `git_diff`

Shows uncommitted changes in the working tree. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `git_diff` |
| **inputSchema** | `{ "path"?: string, "staged"?: bool }` |
| **outputFormat** | `diff` |
| **access** | read-only |
| **timeout** | 15s |
| **recovery** | N/A (pure read) |

### Requirement: Git Diff

The system SHALL provide a `git_diff` tool that returns uncommitted changes, optionally scoped to a path or staged changes only.

#### Scenario: Showing uncommitted changes

- **GIVEN** a git repository with uncommitted changes
- **WHEN** the agent calls `git_diff`
- **THEN** the tool SHALL return the diff of the working tree

#### Scenario: Staged-only diff

- **GIVEN** staged and unstaged changes in the working tree
- **WHEN** the agent calls `git_diff` with `staged: true`
- **THEN** the tool SHALL return only the staged changes

## 01 `git_log`

Shows commit history. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `git_log` |
| **inputSchema** | `{ "path"?: string, "limit"?: int, "branch"?: string }` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 15s |
| **recovery** | N/A (pure read) |

### Requirement: Git Log

The system SHALL provide a `git_log` tool that returns commit history with optional path, limit, and branch filters.

#### Scenario: Showing recent commits

- **GIVEN** a git repository
- **WHEN** the agent calls `git_log` with a limit
- **THEN** the tool SHALL return the most recent commits up to that limit

## 02 `git_status`

Shows working tree status. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `git_status` |
| **inputSchema** | `{}` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 15s |
| **recovery** | N/A (pure read) |

### Requirement: Git Status

The system SHALL provide a `git_status` tool that returns the working tree status — modified, added, deleted, and untracked files, plus branch and merge state.

#### Scenario: Checking repository state

- **GIVEN** a git repository with local changes
- **WHEN** the agent calls `git_status`
- **THEN** the tool SHALL report the current branch and all working tree changes

## 03 `git_commit`

Commits staged changes. Write access.

| Field | Value |
|-------|-------|
| **name** | `git_commit` |
| **inputSchema** | `{ "message": string, "stageAll"?: bool }` |
| **outputFormat** | `text` |
| **access** | write |
| **timeout** | 15s |
| **recovery** | Rollback on failure |

### Requirement: Git Commit

The system SHALL provide a `git_commit` tool that commits staged changes with a message, optionally staging all changes first, and rolling back on failure.

#### Scenario: Committing staged changes

- **GIVEN** staged changes in the working tree
- **WHEN** the agent calls `git_commit` with a message
- **THEN** the tool SHALL create a commit with the staged changes

#### Scenario: Commit failure rollback

- **GIVEN** a commit operation that fails (e.g., no staged changes, hook failure)
- **WHEN** the agent calls `git_commit`
- **THEN** the tool SHALL leave the working tree unchanged
- **AND** the tool SHALL return the failure reason to the LLM

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S130200 | 0.2.0 | Initial git tools spec |