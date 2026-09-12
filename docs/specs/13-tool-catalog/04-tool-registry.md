---
status: active
created: '2026-09-08'
updated: '2026-09-12'
tags:
  - spec
  - tool-catalog
  - tool-registry
  - sdk
priority: high
spec-refs: [130400, 130401, 130402, 130403, 130404, 130405, 130406]
---

# Tool Registry & Discovery

> The SDK tool surface: the metadata contract every tool declares, registration into the runtime, the active set offered to the LLM, and the discovery queries agents use to find, match, and enforce tools.

---

## 00 Overview

The tool registry is the single, in-binary record of every tool available to an agent (S000800 §03). Each catalog tool (S130100–S130300), author tool, and extension tool (S000600) ships a **metadata descriptor** the registry stores and exposes. The registry answers: *what tools exist, what each does, what access it requires, who provided it, and which are active.*

The registry is **not** a user-to-user sharing or marketplace component — that is the Extension Contract (S000600) and Marketplace (`09-cloud-platform/00-marketplace.md`). The registry is the local, compiled-in metadata catalog plus its discovery surface.

### Requirement: Registry Scope

The system SHALL provide an in-binary tool registry that stores the metadata descriptor of every registered tool — catalog, author, and extension — and exposes discovery queries to the agent runtime, independent of any external sharing or marketplace mechanism.

#### Scenario: Registry holds the compiled tool set

- **GIVEN** a navi compiled with a set of catalog tools
- **WHEN** the agent runtime starts
- **THEN** the registry SHALL contain the metadata descriptor for each compiled tool
- **AND** discovery queries SHALL return only tools present in the registry

---

## 01 Tool Metadata Schema

Every tool declares a metadata descriptor. The public `ToolDefinition` type is self-describing — registration IS the metadata declaration, and the descriptor is the single source of truth for both the LLM-facing tool list and execution enforcement.

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique, namespace-qualified (`file_read`, `github_issue_read`) |
| `description` | string | LLM-facing description of what the tool does (capability keywords) |
| `inputSchema` | JSON Schema | Typed schema for arguments (validated before dispatch) |
| `outputFormat` | string | Format hint for rendering: `text`, `json`, `diff`, `markdown` |
| `access` | enum | `read-only`, `write`, or `execute` (S130000 §01) |
| `timeout` | duration | Per-call timeout applied by the runtime (S010400) |
| `recovery` | string | Recovery strategy id from [S010400](../01-agent-runtime/04-tool-error-recovery.md) |
| `mode` | enum | `parallel` or `sequential` execution capability |
| `namespace` | string | Derived from the name prefix (the `domain` of `domain_action`); §04 |
| `tags` | list[string] | Discovery keywords beyond the name |
| `version` | string | Tool schema/behavior version for author & extension tools |
| `promptSnippet` | string | One-line LLM-facing entry for the "available tools" prompt section |
| `promptGuidelines` | list[string] | Tool-specific bullets for the system prompt guidelines |
| `sourceInfo` | struct | Provenance: `origin` (`builtin`/`sdk`/`author`/`extension`), plus path/source metadata |

### Requirement: Complete Metadata Descriptor

The system SHALL define every tool with a complete metadata descriptor covering name, description, input schema, output format, access level, timeout, recovery strategy, execution mode, namespace, discovery tags, prompt metadata, and provenance.

#### Scenario: Declaring a catalog tool with full metadata

- **GIVEN** a for-inclusion catalog tool such as `file_write`
- **WHEN** the tool is declared for the registry
- **THEN** the descriptor SHALL include name, description, input schema, output format (`text`), access (`write`), timeout, recovery strategy, mode, namespace (`file`), tags, prompt snippet, and provenance (`builtin`)

#### Scenario: Minimal descriptor incompleteness

- **GIVEN** a tool declared with only name and description
- **WHEN** the registry validates the descriptor
- **THEN** the system SHALL reject registration with a clear error listing the missing metadata fields

### Requirement: Provenance

The system SHALL record the origin of every tool (`builtin`, `sdk`, `author`, or `extension`) so tools can be audited, filtered, and trusted appropriately.

#### Scenario: Filtering by provenance

- **GIVEN** a registry with builtin, author, and extension tools
- **WHEN** a caller queries tools by `sourceInfo.origin == "builtin"`
- **THEN** the result SHALL include only builtin tools

## 02 Registration

Tools register themselves at build/init time (catalog, author) or when an extension is discovered (S000600). Registration stores the metadata descriptor and binds the implementation handler.

```go
registry.Register(ToolDefinition{
    Name:         "file_write",
    Description:  "Write content to a file within the working directory.",
    InputSchema:  jsonSchema(...),
    OutputFormat: "text",
    Access:       AccessWrite,
    Timeout:      10 * time.Second,
    Recovery:     RecoveryAtomicWriteBackup,
    Mode:         ToolModeSequential,
    Namespace:    "file",
    Tags:         []string{"file", "write", "fs"},
    PromptSnippet: "Write a file (file_write)",
    SourceInfo:    SourceInfo{Origin: OriginBuiltin},
}, handler)
```

### Requirement: Registration With Metadata

The system SHALL register a tool only with its complete metadata descriptor AND its implementation handler, storing both for later lookup, discovery, and execution.

#### Scenario: Registering a catalog tool

- **GIVEN** a tool implementation and its metadata descriptor
- **WHEN** `register` is invoked
- **THEN** the tool SHALL be queryable via discovery and callable by name

#### Scenario: Duplicate name rejection

- **GIVEN** a tool already registered under a name
- **WHEN** a second registration uses the same name
- **THEN** the system SHALL reject the duplicate with an error naming the conflicting tool
- **AND** the original registration SHALL remain intact

## 03 Discovery API

Agents find tools through registry queries. The registry exposes lookup by identity, filtered search, and the runtime active set.

| Operation | Signature | Purpose |
|-----------|-----------|---------|
| `Get(name)` | `(ToolDefinition, bool)` | Exact lookup by tool name |
| `List()` | `[]ToolDefinition` | All registered tools, in registration order |
| `Find(filter)` | `[]ToolDefinition` | Search by namespace, access, mode, tags, and/or origin |
| `AllNames()` | `[]string` | Names only (for LLM-facing tool lists) |
| `Active()` | `[]ToolDefinition` | The current active set offered to the LLM |
| `SetActive(names)` | error | Adjust the active set at runtime |

```go
type FindFilter struct {
    Namespace string
    Access    ToolAccess // zero value = any
    Mode      ToolMode   // zero value = any
    Tags      []string   // ALL must match
    Origin    ToolOrigin // zero value = any
}
```

### Requirement: Registry Discovery Operations

The system SHALL provide discovery operations to get a tool by name, list all tools, find tools by namespace/access/mode/tags/origin, enumerate names, query the active set, and adjust the active set.

#### Scenario: Finding tools by namespace

- **GIVEN** a registry with `file_read`, `git_diff`, and `github_issue_read` registered
- **WHEN** an agent queries `Find({Namespace: "file"})`
- **THEN** the result SHALL include `file_read` only

#### Scenario: Finding tools by access level

- **GIVEN** a registry with read-only, write, and execute tools
- **WHEN** an agent or role configuration queries `Find({Access: read-only})`
- **THEN** the result SHALL include only read-only tools

#### Scenario: Finding tools by tag

- **GIVEN** `git_diff` and `git_log` tagged `git`, `read`
- **WHEN** an agent queries `Find({Tags: ["git"]})`
- **THEN** the result SHALL include both git tools

### Requirement: Registered vs Active

The system SHALL maintain a registry of all registered tools and a separate active set; only tools in the active set SHALL be offered to the LLM, and the active set SHALL be adjustable at runtime without re-registration.

#### Scenario: Register a tool that is not active

- **GIVEN** a registry with a tool registered but not in the active set
- **WHEN** the runtime asks for the LLM-facing tool list
- **THEN** the inactive tool SHALL NOT be sent to the provider

#### Scenario: Activating a tool at runtime

- **GIVEN** a tool registered but inactive
- **WHEN** `SetActive` adds it to the active set
- **THEN** subsequent LLM-facing tool lists SHALL include it

### Requirement: Discovery Result Consistency

The system SHALL return discovery results consistent with the current registry state, reflecting registrations, active-set membership, and any access-control filtering applied to the querying agent.

#### Scenario: Access-filtered discovery

- **GIVEN** an agent with only read-only permission
- **WHEN** the agent queries the registry
- **THEN** the registry SHALL return only tools the agent is permitted to call (S130000 §01)
- **AND** SHALL NOT expose write/execute tools in the result set

## 04 Namespace & Naming

Tool names follow a `namespace_action` pattern so tools group by domain and never collide across domains. The namespace is the segment before the first underscore; the action is the remainder. Names are fully descriptive without being cumbersome: `file_read`/`file_write`/`file_edit` rather than `read`/`write`/`edit`.

| Namespace | Example Tools |
|-----------|---------------|
| `file` | `file_read`, `file_write`, `file_edit`, `file_find`, `file_grep` |
| `shell` | `shell_exec`, `shell_zsh_exec`, `shell_fish_exec` |
| `git` | `git_diff`, `git_log`, `git_status`, `git_commit` |
| `code` | `code_ast_parse`, `code_symbol_search` |
| `test` | `test_run` |
| `todo` | `todo_read`, `todo_write`, `todo_update` |
| `time` | `time_read` |
| `note` | `note_read`, `note_write` |
| `http` | `http_request` |
| `github` | `github_issue_read`, `github_issue_write` |
| `plane` | `plane_issue_read`, `plane_issue_write` |

A name may contain multiple underscores; the namespace is always the first segment (`github_issue_read` → namespace `github`, action `issue_read`).

### Requirement: Namespace-Unique Tool Names

The system SHALL require tool names to be unique per binary and to follow the `namespace_action` pattern, deriving the namespace from the first underscore-separated segment for grouping and filtering.

#### Scenario: Cross-domain same-action tools

- **GIVEN** `github_issue_read` and `plane_issue_read` both need to exist
- **WHEN** both are registered
- **THEN** both SHALL coexist because their namespaces differ
- **AND** `Find({Namespace: "github"})` SHALL return only the GitHub tool

#### Scenario: Namespace collision rejection

- **GIVEN** a registration attempt for `file_read` when `file_read` already exists
- **WHEN** the registry validates the name
- **THEN** the system SHALL reject it as a duplicate (see §02)

### Requirement: Description Carries Discovery Keywords

The system SHALL require each tool description to state, in LLM-readable terms, what the tool does and its subject domain, so description-based matching by the agent is unambiguous alongside tag-based filtering.

#### Scenario: Description supports capability matching

- **GIVEN** two tools in different namespaces
- **WHEN** an agent matches its intent against tool descriptions
- **THEN** the descriptions SHALL distinguish the tools by domain and action enough that the agent selects the correct one

## 05 Agent Discovery Behavior

The agent runtime builds the LLM-facing tool list from the registry's **active set** (access-filtered). Only active, permitted tools are sent to the provider as tool definitions (`name`, `description`, `inputSchema`), plus prompt snippets/guidelines in the system prompt. The remaining metadata (`timeout`, `recovery`, `mode`, `access`) drives execution behavior and enforcement.

### Requirement: LLM-Facing Tool List From Registry

The system SHALL derive the tool definitions sent to the LLM provider from the registry's active set, using the tool name, description, and input schema fields.

#### Scenario: Provider tool definitions

- **GIVEN** an agent with active, permitted tools `file_read` and `shell_exec`
- **WHEN** the runtime prepares a turn
- **THEN** the provider receives tool definitions with those names, their descriptions, and their input schemas
- **AND** the provider SHALL NOT receive tools outside the active, permitted set

### Requirement: Execution Metadata Enforcement

The system SHALL enforce the registered timeout and recovery strategy when executing a tool call, and the registered mode when batching parallel calls.

#### Scenario: Per-tool timeout applied

- **GIVEN** a tool with a registered 30s timeout
- **WHEN** the tool executes and exceeds the timeout
- **THEN** the runtime SHALL terminate the call, apply the recovery strategy, and return partial output plus the timeout error to the LLM (S010400)

### Requirement: Prompt Metadata in System Prompt

The system SHALL include each active tool's one-line prompt snippet in the available-tools section of the system prompt, and include prompt guidelines only for active tools.

#### Scenario: Snippet and guidelines exposure

- **GIVEN** an active tool with a prompt snippet and guidelines
- **WHEN** the system prompt is assembled
- **THEN** the snippet SHALL appear in the available-tools section
- **AND** the guidelines SHALL appear only while the tool is active

## 06 Schema Validation & Argument Preparation

Tools are invoked only after their arguments pass JSON-schema validation against `inputSchema`. Optionally, a tool declares a `prepareArguments` hook that runs before validation, for backward compatibility with changed schemas (e.g. resuming an old session whose stored arguments no longer match the current schema). The public schema stays strict; compatibility lives in the hook.

### Requirement: Validation Before Dispatch

The system SHALL validate tool-call arguments against the tool's input schema before dispatching to the handler, returning an invalid-arguments error to the LLM on failure.

#### Scenario: Invalid arguments rejected

- **GIVEN** a tool call whose arguments violate the input schema
- **WHEN** the runtime dispatches the call
- **THEN** the runtime SHALL return an invalid-arguments error without invoking the handler

### Requirement: Argument Preparation Hook

The system SHALL run a tool's `prepareArguments` hook (when declared) before validation, so older accepted argument shapes can be migrated to the current schema.

#### Scenario: Old session arguments

- **GIVEN** a resumed session containing a tool call with an older argument shape
- **WHEN** the runtime dispatches the call
- **THEN** the runtime SHALL run `prepareArguments` to map the old shape to the current schema
- **AND** the handler SHALL receive arguments matching the current schema

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-09-08 | S130400 | 0.2.0 | Initial tool registry & discovery spec |
| 2026-09-12 | S130400 | 0.2.1 | Status promoted draft → active |
| 2026-09-12 | S130400 | 0.2.2 | Added todo/time/note/http namespaces to §04 table (S130500) |