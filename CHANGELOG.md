# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Daemon tool wiring (S130400, NAVI-272): the daemon assembles a tool registry — builtins (`file_read`, `shell_exec`) register with complete metadata through the new public `tools/builtin` package — and the default active set is composed from the runtime `ToolPolicy` (allowed/blocked lists, per-tool timeout overrides); each thought run's executor is rebuilt from the registry's active set via `tools/bridge`, so the LLM-facing tool surface is registry-governed; `navi daemon` gains `--navi-id` (per-navi scoping key for stateful tool storage) and `navi health` surfaces the navi's todo counts by status (pending/in_progress/done) from the stateful store (NAVI-272)
- `tools/bridge` — public runtime bridge between the tool registry and the agent runtime (S130400 runtime-integration): binds the registry's active, access-permitted tools into an `agent.ToolExecutor`, applying allowed/blocked policy and per-tool timeout overrides; every call dispatches through `registry.Dispatch` so schema validation and active-set enforcement stay in the registry, and calls are aborted with a timeout error even against ctx-ignoring handlers (NAVI-75)
- Stateful tool storage seam (S130500 §07): `store.StatefulToolStore` contract with per-navi scoping on every method (`ListTodos`/`ReplaceTodos`/`UpdateTodos`/`ReadNotes`/`UpsertNote`/`RemoveNote`), `Todo`/`Note`/`TodoUpdate`/`NoteQuery` types, and `ErrTodoNotFound`/`ErrNoteNotFound` sentinels (NAVI-267)
- `store/sqlite`: migration 6 adds `todos` + `notes` tables keyed `(navi_id, id)` with navi-scoped indexes; `StatefulStore` implementation with transactional full-replace and atomic batch updates (unknown id rejects the whole batch)
- `tools/registry.RegisterStateful` — registration variant binding a per-navi store handle; stateful handlers receive `StateContext{NaviID, Store}` per dispatch; stateless `Register` untouched (NAVI-267)

### Added
- `sdk/` public package (S000800): `NaviDef` authoring types (name, version, personality, skills, tools, interfaces, extensions, permissions) and the `Build()` entry point with full definition validation; the S000300 builder pipeline remains pending, so `Build` fails with `ErrBuilderUnavailable` (NAVI-273)
- Starter templates per S000313: `templates/navi-code/` (CLI coding agent) and `templates/navi-daemon/` (daemon-style) as nested Go modules pinned to the published SDK, each with an author tool registered through `tools/registry` and an example runtime config (NAVI-273)
- CI: starter-template build job against the SDK on main and release tags (S000805 guardrail) (NAVI-273)

### Changed
- `internal/config`: removed YAML navi-definition parsing (dead per S000207/go-first); thought-queue tuning and cron schedules moved to runtime config under `.config/navi/<name>/`; `navi daemon` drops the `-definition` flag; `RuntimeConfig.Validate()` checks self-contained invariants instead of cross-checking a definition (NAVI-275)
- Replaced bare `.gitignore` patterns (`navi`, `navi.db`) with anchored artifact-dir entries (`/bin/`, `/data/`) — bare patterns match at any directory level and silently ignore new files under `cmd/navi/` (#246)
- Specs are Go-only for navi definitions: removed the `naviconfig.yaml` alternative (S000207/S000100/S000300), added the in-repo starter template + `navi init` scaffolding spec (S000312) and the SDK-surface template guardrail (S000805)
- Documented the actual `internal/agent/` file layout vs the spec §09 sketch (`internal/agent/README.md` + spec implementation note) — docs-only, no code renames (NAVI-259)

### Added
- `tools/registry` — public tool registry per S130400: complete `ToolDefinition` metadata descriptor (access, timeout, recovery, mode, provenance, prompt metadata), `Register`/`Get`/`List`/`Find`/`AllNames` discovery, registered-vs-active sets with runtime `SetActive`, JSON-schema argument validation before dispatch, and the `prepareArguments` back-compat hook (NAVI-184)
- `justfile` with CI-gate parity (`build` → `bin/navi`, `vet`, `fmt-check`, `test-race`, `ci`) and run wrappers (`daemon`, `health`, `enqueue`) that keep runtime state under `data/navi.db`; artifact-dir convention documented in `AGENTS.md` (#246)
- Migrated `docs/spec/` to the spec-tree standard (`docs/specs/`) — 13 numbered components, spec codes (6-digit `component.feature.subfeature`), `spec-refs` frontmatter, SHALL + GIVEN/WHEN/THEN requirement grammar, and Status History on every feature
- Added `docs/changes/` versioned change tracker, shipped `docs/templates/` skeletons, and synced the `specs-*` skills into `.pi/skills/`
- Deprecated product-spec files (REQUIREMENTS/DESIGN/IMPLEMENTATION/TESTING) folded into numbered feature files
- Renamed catalog tools to namespaced names per S130400 §04 across the spec tree (`file_read`, `shell_exec`, `file_grep`, `code_ast_parse`, …) and promoted the tool-catalog specs from draft to active

### Added
- Feature README entry points with ownership maps, success criteria, and status history
- CONTEXT.md, SOLUTION.md, RESULTS.md for product-lifecycle case study
- Out of Scope sections to all spec and case study documents
- RESULTS.md with measurable outcomes and lessons learned
- S130400 tool registry & discovery spec and S000800 SDK surface spec; 0.1.0 change-log entry derived for the tool catalog
- S130500 agent utility tools spec: `todo_read`/`todo_write`/`todo_update`, `time_read`, `note_read`/`note_write`, and `http_request` (CRUD with SSRF guards and credential-reference auth), plus the stateful tool storage seam; case study captured in `docs/case-study/agent-utility-tools/`
- Agent utility tools change-log entry (`docs/changes/0.1.0/feat/agent-utility-tools/`) tracked as Huly epic NAVI-266 with six sub-issues and dependency relations

- GitHub Action workflow enforcing changelog entries in PRs (#255)
### Added
- Public SDK surface per S000800 §01: `llm/`, `store/` (+ `store/sqlite`), and `agent/` promoted from `internal/`; the session data model (Session, SessionEntry, Store interface, MemoryStore) moved to public `store/` while manager/compaction/snapshot behavior stays internal behind type aliases (NAVI-276, NAVI-277)
- `tools/sdk-consumer/` — an external-consumer module (its own go.mod/replace) plus a CI job that imports every public package and exercises the runtime end to end, gating any leak of internal types into a public signature (NAVI-277)

## [1.0.0] - YYYY-MM-DD

### Added
- Initial release

### Changed
- 

### Fixed
- 

[Unreleased]: 
[1.0.0]: 
