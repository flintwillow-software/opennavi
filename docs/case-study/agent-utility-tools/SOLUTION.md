---
status: draft
created: '2026-09-12'
updated: '2026-09-12'
tags:
  - case-study
  - tool-catalog
  - agent-utility-tools
priority: medium
---

# Solution

Seven tools across four namespaces, all implementing the S130400 `ToolDefinition` contract and registering through the public registry. Names follow the `namespace_action` convention (S130400 §04).

## `todo` namespace — durable plan tracking

**Data model** (persisted per navi via the store layer):

```go
type TodoItem struct {
    ID        string    // stable id
    Text      string    // imperative task statement
    Status    string    // "pending" | "in_progress" | "done"
    Priority  int       // lower = more urgent (0 = unset)
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

| Tool | Access | Input schema | Behavior |
|------|--------|--------------|----------|
| `todo_read` | read-only | `{ "status"?: string, "limit"?: int }` | Returns items, newest activity first; full list by default |
| `todo_write` | write | `{ "items": [{ "text": string, "status"?: string, "priority"?: int }] }` | **Full replace** — the plan-writing affordance (make a plan before starting work); returns the assigned ids |
| `todo_update` | write | `{ "updates": [{ "id": string, "text"?: string, "status"?: string, "priority"?: int, "remove"?: bool }] }` | Partial updates: status transitions, reorders, edits, removal |

Design decisions:
- Three tools rather than one mega-tool: read is cheap and common; full-replace write keeps "rewrite the plan" one call; `todo_update` covers transitions without resending the whole list.
- Single `in_progress` recommendation is prompt-level guidance (promptGuidelines), not schema enforcement — parallel work is allowed but discouraged.
- Todo state renders into the navi health/status surface so humans can observe progress.

## `time` namespace — wall-clock awareness

| Tool | Access | Input schema | Output |
|------|--------|--------------|--------|
| `time_read` | read-only | `{ "timezone"?: string, "epoch"?: bool }` | Current time in the requested zone (default: navi's configured zone), ISO 8601 + epoch seconds, UTC offset |

Stateless, trivially parallel (`mode: parallel`), zero timeout risk. Also computes nothing beyond formatting — duration math stays with the LLM.

## `note` namespace — verbatim scratchpad

**Data model** (persisted per navi via the store layer):

```go
type Note struct {
    ID        string
    Title     string
    Body      string   // verbatim text, size-capped
    Tags      []string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

| Tool | Access | Input schema | Behavior |
|------|--------|--------------|----------|
| `note_read` | read-only | `{ "id"?: string, "tag"?: string, "query"?: string, "limit"?: int }` | Get by id, list by tag, or literal substring search (not semantic) |
| `note_write` | write | `{ "id"?: string, "title": string, "body": string, "tags"?: [string], "remove"?: bool }` | Upsert by id (new id assigned when omitted); `remove: true` deletes |

Distinct from 06-memory: notes are verbatim, append-heavy, LLM-curated; memory is semantic, system-curated.

## `http` namespace — outbound CRUD

| Tool | Access | Input schema |
|------|--------|--------------|
| `http_request` | execute (decision point — external side effects; see Open Questions) | `{ "method": "GET"\|"POST"\|"PUT"\|"PATCH"\|"DELETE", "url": string, "headers"?: {string: string}, "query"?: {string: string}, "body"?: string, "authRef"?: string, "timeout"?: int, "maxResponseBytes"?: int }` |

Output: `{ "status": int, "headers": {...}, "body": string }` — body truncated at `maxResponseBytes` (default 256KB) with a truncation notice; non-2xx responses are returned normally (the LLM decides recovery), only transport failures are tool errors.

Security model:
- **SSRF guard**: private, loopback, link-local, and multicast ranges denied by default; per-navi allowlist config for internal services. Redirects limited (default 3) and re-validated per hop.
- **Auth via reference only**: `authRef` names a credential-resolver entry (S100400); the secret is injected into the request server-side and never returned to the LLM. Response header filtering strips `Authorization`/`Set-Cookie` echoes.
- **Egress policy**: per-navi domain allowlist/denylist hooking the IAM permission model (S040200).
- TLS verification always on; response size caps; method restricted to the CRUD set.

## Storage & registration

- **`todo` and `note`** persist through the existing store layer (modernc SQLite, CGO_ENABLED=0), scoped per navi identity, surviving restarts and sessions. Introduces a small *stateful-tool seam*: stateful catalog tools receive a store handle at registration; the file/git tools stay stateless.
- **`time` and `http`** are stateless.
- All seven register through `tools/registry` with `OriginBuiltin`; the navi definition selects which are compiled in (S000300) and the default active set is wired like the rest of the catalog.
- Recovery strategies: todo/note writes → atomic transaction + rollback; `http_request` → return response body/headers even on transport failure where available; `time_read` → N/A.

## Open Questions (settle at spec time)

1. `http_request` access level — `execute` (proposed: external side effects) or a new `network` level in S130000's access taxonomy?
2. Should `todo_write` full-replace semantics include an `append` mode, or is replace + `todo_update` sufficient?
3. Note size cap default (proposal: 64KB/body) and per-navi total cap.
4. Does `todo` state surface in the council-health/health-subcommand views, and how?