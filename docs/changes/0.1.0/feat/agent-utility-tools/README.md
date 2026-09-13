# 0.1.0 — feat: agent-utility-tools

> Derived from the S130500 spec update (agent utility tools) and informed by the codebase analysis. Adds the everyday agent affordances to the tool catalog: durable todo tracking, wall-clock awareness, a verbatim notes scratchpad, and outbound HTTP CRUD — including the new stateful-tool storage seam.

## Summary

Implement four new tool namespaces as public SDK packages — `todo` (durable plan tracking), `time` (wall-clock), `note` (verbatim scratchpad), `http` (outbound CRUD) — and the stateful-tool storage seam that lets catalog tools persist per-navi state through the existing SQLite store layer. All seven tools register through the public tool registry (S130400) with complete `ToolDefinition` metadata and hook into IAM access control (S040200) and per-tool recovery (S010400).

## Changes

- **`tools/todo/`** (public SDK) — `todo_read`, `todo_write`, `todo_update`: durable todo list with `pending`/`in_progress`/`done` statuses and priorities; `todo_write` is an atomic full-replace with rollback; `todo_update` applies partial updates by id with atomic batch rejection on unknown ids. (S130500 §00–02)
- **`tools/time/`** (public SDK) — `time_read`: current time as ISO 8601 + epoch, IANA timezone parameter with invalid-zone error, defaulting to the navi's configured zone. Stateless, parallel-safe. (S130500 §03)
- **`tools/note/`** (public SDK) — `note_read`, `note_write`: verbatim note scratchpad — read by id/tag/literal substring (no semantic matching), upsert with 64KB body cap + remove. (S130500 §04–05)
- **`tools/http/`** (public SDK) — `http_request`: outbound HTTP restricted to CRUD methods; non-2xx returned as normal output; response truncation with notice; SSRF guard (private/loopback/link-local/multicast denied by default, redirect re-validation with max 3 hops, per-navi allowlist); `authRef` resolved server-side via the credential resolver with `Authorization`/`Set-Cookie` stripped from responses. (S130500 §06)
- **Stateful tool storage seam** — stateful catalog tools (`todo_*`, `note_*`) receive a per-navi store handle at registration; new `todos` and `notes` tables via the `internal/store/sqlite/schema.go` migration pattern; per-navi scoping enforced at the store layer. (S130500 §07)
  - Sub: `internal/store/sqlite/schema.go` — migrations for `todos` + `notes` tables (id, text/title, body, status/tags, priority, timestamps, navi identity column)
  - Sub: `tools/registry` (S130400) — registration variant passing the store handle to stateful tools
- **`internal/agent`** — wire the utility tools into the `ToolExecutor` (`RegisterTool`, `executor.go`), convert registered `ToolDefinition`s to provider tool definitions, and extend the default active set composition. Absorbs naming: catalog tools register via the public registry per the 0.1.0 tool-catalog change.
- **`internal/ops`** (health/status view) — surface the navi's current todo state in the health output (complements the `navi health` subcommand probes, #254). (S130500 §07)
- **`internal/config`** — per-navi configuration for `http_request`: SSRF allowlist/denylist, egress policy hook into IAM, default timeout/response caps; note store cap (10MB default).

## Spec Refs

[130000, 130400, 130500, 130501, 130502, 130503, 130504, 130505, 130506, 130507]

## Code Context

- **Project structure**: Go module; public SDK packages live at repo root (`tools/` per the tool-catalog change — not yet created); runtime code under `internal/` (`agent`, `store/sqlite`, `ops`, `config`, `cron`)
- **Existing patterns**: `internal/agent/executor.go` `ToolExecutor.RegisterTool(tool AgentTool, handler)` + `internal/agent/tools.go` inline `file_read`/`shell_exec`; `internal/store/sqlite/thought_store.go` as the store-package pattern; numbered migrations in `schema.go` (`migrate`, `schema_migrations` table)
- **Dependencies**: no new deps — `net/http` stdlib for `http_request`, `time` stdlib, existing `modernc.org/sqlite` for persistence; CGO_ENABLED=0 holds
- **Gaps vs code**: nothing utility-related exists yet; all seven tools and the storage seam are new. `http_request`'s SSRF guard and credential-resolver auth depend on S100400 (credential resolver, planned — may need a minimal `authRef` interface stub)

## Notes

- **Epic: "Agent Utility Tools"** — one epic; sub-issues per namespace plus the storage seam and wiring
- **Build order / dependency**: tool registry (#205 / NAVI-184) MUST land first — every utility tool hangs off `ToolDefinition`; then storage seam (blocks todo + note tools); todo/note/time/http tools parallelize after their prereqs; wiring last
- `time_read` is the smallest standalone win (pure stdlib, no store) — good first issue
- `http_request` carries the most security surface (SSRF + auth filtering) — deserves the most test coverage; egress policy config touches `internal/config`
- Related change: 0.1.0 feat/tool-catalog (registry + catalog packages); this change extends the same registry

## Tracked

- Epic: Agent Utility Tools (NAVI-266) — Huly NAVI project
  - Issue: Implement stateful tool storage seam (NAVI-267) — blocked by registry (NAVI-184)
  - Issue: Implement todo tools (todo_read, todo_write, todo_update) (NAVI-268) — blocked by NAVI-184, NAVI-267
  - Issue: Implement time tool (time_read) (NAVI-269) — blocked by NAVI-184
  - Issue: Implement note tools (note_read, note_write) (NAVI-270) — blocked by NAVI-184, NAVI-267
  - Issue: Implement http tool (http_request) (NAVI-271) — blocked by NAVI-184
  - Issue: Wire utility tools into agent runtime + health view (NAVI-272) — blocked by NAVI-184, NAVI-268, NAVI-269, NAVI-270, NAVI-271