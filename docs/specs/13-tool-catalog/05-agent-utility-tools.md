---
status: active
created: '2026-09-12'
updated: '2026-09-12'
tags:
  - spec
  - tool-catalog
  - agent-utility-tools
priority: medium
spec-refs: [130500, 130501, 130502, 130503, 130504, 130505, 130506, 130507]
---

# Agent Utility Tools

> The utility affordances every production agent relies on — durable todo tracking, wall-clock awareness, a verbatim notes scratchpad, and outbound HTTP CRUD — with typed schemas, per-navi persistence, and per-tool recovery strategies.

---

## 00 `todo_read`

Returns the navi's durable todo list. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `todo_read` |
| **inputSchema** | `{ "status"?: "pending" \| "in_progress" \| "done", "limit"?: int }` |
| **outputFormat** | `json` |
| **access** | read-only |
| **timeout** | 5s |
| **recovery** | N/A (pure read) |
| **mode** | parallel |

### Requirement: Read Todo List

The system SHALL provide a `todo_read` tool that returns the navi's todo items with their ids, text, status, priority, and timestamps, optionally filtered by status.

#### Scenario: Listing all todos

- **GIVEN** a navi with persisted todo items
- **WHEN** the agent calls `todo_read` with no arguments
- **THEN** the tool SHALL return all items ordered by priority, then most recent activity

#### Scenario: Filtering by status

- **GIVEN** todos in mixed states
- **WHEN** the agent calls `todo_read` with `status: "in_progress"`
- **THEN** the tool SHALL return only items in that status

## 01 `todo_write`

Replaces the entire todo list with a new plan. Write access.

| Field | Value |
|-------|-------|
| **name** | `todo_write` |
| **inputSchema** | `{ "items": [{ "text": string, "status"?: "pending" \| "in_progress" \| "done", "priority"?: int }] }` |
| **outputFormat** | `json` |
| **access** | write |
| **timeout** | 10s |
| **recovery** | Atomic transaction, rollback on failure |
| **mode** | sequential |

### Requirement: Replace Todo List

The system SHALL provide a `todo_write` tool that atomically replaces the navi's entire todo list with the given items, assigning stable ids, and returns the persisted items.

#### Scenario: Writing a plan

- **GIVEN** a multi-step task
- **WHEN** the agent calls `todo_write` with an ordered item list
- **THEN** the tool SHALL replace all existing items atomically
- **AND** the tool SHALL return the items with their assigned ids

#### Scenario: Failed write rollback

- **GIVEN** a write that fails partway (e.g. store error)
- **WHEN** the tool fails
- **THEN** the system SHALL leave the previous todo list intact
- **AND** the tool SHALL return the failure reason to the LLM

## 02 `todo_update`

Applies partial updates to todo items. Write access.

| Field | Value |
|-------|-------|
| **name** | `todo_update` |
| **inputSchema** | `{ "updates": [{ "id": string, "text"?: string, "status"?: "pending" \| "in_progress" \| "done", "priority"?: int, "remove"?: bool }] }` |
| **outputFormat** | `json` |
| **access** | write |
| **timeout** | 10s |
| **recovery** | Atomic transaction, rollback on failure |
| **mode** | sequential |

### Requirement: Update Todo Items

The system SHALL provide a `todo_update` tool that applies partial updates — status transitions, text edits, priority changes, and removal — to todo items by id, atomically.

#### Scenario: Transitioning a task

- **GIVEN** a todo item with id
- **WHEN** the agent calls `todo_update` with `status: "done"` for that id
- **THEN** the tool SHALL update the item's status and timestamps
- **AND** the tool SHALL return the updated item

#### Scenario: Unknown id

- **GIVEN** an update referencing an id that does not exist
- **WHEN** the agent calls `todo_update`
- **THEN** the tool SHALL reject the batch atomically
- **AND** the tool SHALL return an error identifying the unknown id

## 03 `time_read`

Returns the current time. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `time_read` |
| **inputSchema** | `{ "timezone"?: string, "epoch"?: bool }` |
| **outputFormat** | `text` |
| **access** | read-only |
| **timeout** | 5s |
| **recovery** | N/A (pure read) |
| **mode** | parallel |

### Requirement: Current Time

The system SHALL provide a `time_read` tool that returns the current time in the requested IANA timezone (default: the navi's configured zone) as ISO 8601 with UTC offset, plus epoch seconds when requested.

#### Scenario: Reading current time

- **GIVEN** a navi with a configured timezone
- **WHEN** the agent calls `time_read` with no arguments
- **THEN** the tool SHALL return the current time in ISO 8601 with UTC offset and epoch seconds

#### Scenario: Timezone conversion

- **GIVEN** an IANA timezone name
- **WHEN** the agent calls `time_read` with that timezone
- **THEN** the tool SHALL return the current time converted to that zone

#### Scenario: Invalid timezone

- **GIVEN** an unknown timezone string
- **WHEN** the agent calls `time_read`
- **THEN** the tool SHALL return an error identifying the invalid timezone

## 04 `note_read`

Returns notes from the navi's durable scratchpad. Read-only access.

| Field | Value |
|-------|-------|
| **name** | `note_read` |
| **inputSchema** | `{ "id"?: string, "tag"?: string, "query"?: string, "limit"?: int }` |
| **outputFormat** | `json` |
| **access** | read-only |
| **timeout** | 10s |
| **recovery** | N/A (pure read) |
| **mode** | parallel |

### Requirement: Read Notes

The system SHALL provide a `note_read` tool that returns notes by id, by tag, or matching a literal substring query, with bodies truncated per the note size cap and a truncation notice.

#### Scenario: Reading a note by id

- **GIVEN** a persisted note with id
- **WHEN** the agent calls `note_read` with that id
- **THEN** the tool SHALL return the note's title, body, tags, and timestamps

#### Scenario: Searching notes

- **GIVEN** notes across several topics
- **WHEN** the agent calls `note_read` with a literal substring query
- **THEN** the tool SHALL return notes whose title or body contain the substring
- **AND** the tool SHALL NOT perform semantic matching (notes are verbatim; semantic recall belongs to the memory system)

## 05 `note_write`

Creates, updates, or removes notes in the navi's durable scratchpad. Write access.

| Field | Value |
|-------|-------|
| **name** | `note_write` |
| **inputSchema** | `{ "id"?: string, "title": string, "body": string, "tags"?: [string], "remove"?: bool }` |
| **outputFormat** | `json` |
| **access** | write |
| **timeout** | 10s |
| **recovery** | Atomic transaction, rollback on failure |
| **mode** | sequential |

### Requirement: Upsert Notes

The system SHALL provide a `note_write` tool that stores a note verbatim (title, body, tags), assigning a new id when omitted and updating the existing note when the id matches, and removing the note when `remove` is true.

#### Scenario: Writing a new note

- **GIVEN** a title and body under the size cap (64KB body)
- **WHEN** the agent calls `note_write` without an id
- **THEN** the tool SHALL create the note atomically and return its assigned id

#### Scenario: Body exceeding size cap

- **GIVEN** a body larger than the 64KB cap
- **WHEN** the agent calls `note_write`
- **THEN** the tool SHALL reject the write with an error identifying the size limit
- **AND** the tool SHALL leave existing notes unchanged

#### Scenario: Removing a note

- **GIVEN** a persisted note with id
- **WHEN** the agent calls `note_write` with that id and `remove: true`
- **THEN** the tool SHALL delete the note atomically

## 06 `http_request`

Performs an outbound HTTP request with full CRUD methods. Execute access.

| Field | Value |
|-------|-------|
| **name** | `http_request` |
| **inputSchema** | `{ "method": "GET" \| "POST" \| "PUT" \| "PATCH" \| "DELETE", "url": string, "headers"?: {string: string}, "query"?: {string: string}, "body"?: string, "authRef"?: string, "timeout"?: int, "maxResponseBytes"?: int }` |
| **outputFormat** | `json` |
| **access** | execute |
| **timeout** | 30s (default, configurable per call) |
| **recovery** | Capture response on failure, truncation notice |
| **mode** | parallel |

### Requirement: Outbound HTTP Request

The system SHALL provide an `http_request` tool that performs outbound HTTP requests restricted to CRUD methods and returns the response status, filtered headers, and body (truncated at `maxResponseBytes`, default 256KB) to the LLM.

#### Scenario: Successful request

- **GIVEN** an external API endpoint
- **WHEN** the agent calls `http_request` with a method and URL
- **THEN** the tool SHALL perform the request and return the status, headers, and body

#### Scenario: Non-2xx response

- **GIVEN** an endpoint that returns HTTP 404
- **WHEN** the tool completes the request
- **THEN** the tool SHALL return the 404 status and response body as normal output
- **AND** the tool SHALL NOT treat the non-2xx status as a tool error

#### Scenario: Response truncation

- **GIVEN** a response body larger than `maxResponseBytes`
- **WHEN** the tool captures the response
- **THEN** the tool SHALL truncate the body at the cap
- **AND** the tool SHALL mark the output partial with a truncation notice

### Requirement: SSRF Protection

The system SHALL deny requests to private, loopback, link-local, and multicast address ranges by default, re-validate redirects per hop (max 3), and permit per-navi allowlist configuration for internal services.

#### Scenario: Private address denied

- **GIVEN** a URL resolving to a private address (e.g. `10.x.x.x`, `127.0.0.1`)
- **WHEN** the agent calls `http_request` against that URL
- **THEN** the tool SHALL reject the call with an SSRF denial error
- **WHEN** the same URL is called from a navi whose config allowlists that range
- **THEN** the tool SHALL perform the request

#### Scenario: Redirect re-validation

- **GIVEN** a request that receives an HTTP redirect to a private address
- **WHEN** the tool follows the redirect
- **THEN** the tool SHALL re-validate the destination against the SSRF rules
- **AND** the tool SHALL deny the redirect if the destination is not permitted

### Requirement: Credential Reference Auth

The system SHALL resolve the `authRef` parameter through the credential resolver (S100400), injecting the resolved secret into the request server-side, and SHALL NOT return the secret or echoed credential headers to the LLM.

#### Scenario: Authenticated request

- **GIVEN** a credential-resolver entry named by `authRef`
- **WHEN** the agent calls `http_request` with that `authRef`
- **THEN** the tool SHALL resolve and inject the credential server-side
- **AND** the response headers SHALL be filtered to strip `Authorization` and `Set-Cookie` echoes before returning to the LLM

## 07 Stateful Tool Storage Seam

Stateful catalog tools (`todo_*`, `note_*`) receive a per-navi store handle at registration time and persist through the existing store layer (modernc SQLite, CGO_ENABLED=0), scoped per navi identity. Todo and note state survive restarts and sessions. Stateless catalog tools are unaffected.

### Requirement: Stateful Tool Persistence

The system SHALL provide stateful catalog tools a per-navi store handle at registration, so todo and note state persists across restarts and sessions, scoped per navi identity.

#### Scenario: State survives restart

- **GIVEN** a navi with persisted todos and notes
- **WHEN** the daemon restarts the navi
- **THEN** `todo_read` and `note_read` SHALL return the same persisted state

#### Scenario: Per-navi scoping

- **GIVEN** two navis on the same host
- **WHEN** one navi writes todos
- **THEN** the other navi's `todo_read` SHALL NOT return the first navi's items

### Requirement: Todo State Visibility

The system SHALL surface the navi's todo state in the navi health/status view, so a human can observe what the navi is working on.

#### Scenario: Health view shows todos

- **GIVEN** a navi with in-progress todo items
- **WHEN** the health/status view renders
- **THEN** it SHALL include the current todo states

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-09-12 | S130500 | 0.2.0 | Initial agent utility tools spec (todo, time, note, http namespaces + stateful storage seam) |