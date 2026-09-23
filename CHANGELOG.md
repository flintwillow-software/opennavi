# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `internal/thought` defer_until mechanism + queue-handle registration seam (S130600 §00/§02, NAVI-322): `Thought` gains the durable not-before field `DeferUntil` (persisted via a new `thoughts.defer_until` column, crash recovery preserves it — S010002); `Dequeue` skips thoughts until their deferral passes so a deferred entry never blocks due work; `Enqueue` applies the self-scheduled priority clamp for origin-tagged thoughts (`origin: self`/`tool` → conservative band, `EffectiveSelfPriority`); `RegisterQueueTools` binds catalog tools to the navi's own queue handle (per-dispatch `QueueHandle`, same governance as any catalog tool — complete metadata, schema validation, active set, access levels — with snapshot-only reads per the #280 `q.mu` discipline)
- `internal/thought` — queue-level message routing (S010312, NAVI-317): `RouteMessage(id, msg)` delivers by lifecycle state — pending thoughts get a Context append (picked up at claim), running thoughts steer via the agent's turn-boundary steering queue (bridge wired through `SetSteeringRouter`; invoked outside q.mu so bridges may snapshot via `GetThought`), paused thoughts resume (paused → pending) with the message attached to Context and checkpoint intact, and failed/terminal thoughts are rejected with a reason (retry hint for failed per S010002). Every attempt — delivered or rejected, unknown id included — emits a `message_routed` delivery-ack event (`QueueEvent.Delivery` + outcome) so portal clients (S000406) observe delivery and rejections
- Foreground slot reservation (S010003, NAVI-317): `Config.ForegroundSlots` reserves capacity (disjoint from priority slots) claimable only by foreground-tagged thoughts; foreground thoughts prefer their reserved slot, fall back to open slots, and compete by the same priority/preemption rules as daemon work — interactive sessions never starve behind daemon backlog
- Specs (0.2.0, thought-loop): the queue is the navi runtime's main loop in every run mode — house model revises the two-execution-model statement (S010003, S010104); TUI re-spec'd as thought-queue portal with per-thought actions, live thought view, steering, and message granularity (S000406); gateway gains remote portal attachment with authorization (S000407); queue-level message routing by thought id with delivery acknowledgments (S010312). Case study + spec-updates in `docs/case-study/thought-loop/`
- Case study: thought loop — one house, one queue, many doors (TUI/connectors/tools are doors; queue is the runtime's main loop whether daemon or binary run), plus the d3 spec-updates proposal (S010003, S010104, S000406, S000407, S010312) pending approval (`docs/case-study/thought-loop/`)
- `internal/ops/metrics` — thought-queue metrics with Prometheus text exposition (NAVI-258): counters (`thoughts_queued_total`, `thoughts_parked_total`, `thoughts_resumed_total`, `thoughts_preempted_total`, `thought_persist_failures_total`), gauges (`thoughts_in_queue`, `thoughts_by_status{status}`), and a `thought_processing_seconds` histogram (0.1/0.5/1/2.5/5/10/+Inf buckets). Collector rides the `ThoughtQueue.OnEvent` fanout (zero-dep, no client_golang/OTel SDK); in-queue/status gauges are exact lifecycle deltas seeded once from a queue snapshot, so kill-9 → restart recovery is visible at wire time; per-thought breakdown ships as a capped ring gauge (`thought_recent_processing_seconds{thought_id}`, 100-entry FIFO) instead of unbounded per-thought labels on cumulative series. Daemon wires it via `--metrics-addr` (default OFF — zero cost unset), serving `/metrics` through `Runtime.Run` with graceful shutdown; concurrency-tested (parallel scrapes vs event handling)
- Daemon registration of the http tool + `tool_caps` config surface (NAVI-272 wiring): `Assemble` registers `http_request` (SSRF-guarded egress; `AuthResolver` stays nil until S100400 lands, so `authRef` calls fail closed) and a new `tool_caps` runtime-config section tunes the note caps (`max_body_bytes`/`max_navi_bytes`) and http egress (`max_response_bytes`/`allowlist`/`denylist`); the merge chain carries the section (with validation `>= 0`); assembly tests cover cap rejection, denylist enforcement, and the full 9-tool default active set (NAVI-272)
- `tools/http` — stateless `http_request` tool (S130500 §06): CRUD methods (GET/POST/PUT/PATCH/DELETE) returning status, filtered headers, and body; non-2xx responses are normal output; SSRF guard denies private/loopback/link-local/multicast destinations — every DNS-resolved address is checked at validation AND again at dial time via a pinned transport dialer (closes the DNS-rebinding TOCTOU), with per-navi allowlist/denylist (hostname, IP-literal, or CIDR entries), deny-wins precedence, and per-hop redirect re-validation (max 3); `authRef` credential references resolve server-side through the `AuthResolver` interface stub (S100400 planned) and fail closed without one; `Authorization`/`Set-Cookie` echoes stripped from responses; 256 KiB response cap (and method/timeout/cap) configurable per call and via `Options`, with per-call timeout clamped to the 30s definition ceiling (NAVI-271)
- Daemon registration of the time tool (NAVI-272 wiring): `Assemble` registers the stateless `time_read` via `time.RegisterTools`; assembly tests extend dispatch coverage to time_read (IANA zone conversion + invalid-zone error) (NAVI-272)

### Changed
- `tools/time`: embed the IANA tzdata database (`_ "time/tzdata"`) — `time.LoadLocation` previously depended on the host's zoneinfo and would degrade to UTC-only in scratch/minimal containers (CGO_ENABLED=0 daemons included) (NAVI-272)
- `tools/time` — stateless `time_read` tool (S130500 §03): current time in the requested IANA timezone (default UTC until the runtime zone-config lands) as ISO 8601 with UTC offset plus epoch seconds (on by default, `epoch: false` suppresses); invalid zones rejected with an error naming the zone; pure `time` stdlib, registered via plain `registry.Register` (NAVI-269)
- Daemon registration of the note tools (NAVI-272 wiring): `Assemble` registers `note_read`/`note_write` for the navi's identity through the stateful seam (default 64 KiB / 10 MiB caps; per-navi total cap configurable via `note.Options` when a config surface lands); assembly tests extend the end-to-end dispatch coverage to note_write→note_read (NAVI-272)
- Daemon registration of the todo tools (NAVI-272 wiring): `Assemble` registers `todo_read`/`todo_write`/`todo_update` for the navi's identity through the stateful seam (`todo.RegisterTools`, default `--navi-id` scope `default`) and includes them in the default active set; assembly tests verify end-to-end todo_write→todo_read dispatch through the assembled registry (NAVI-272)
- `tools/todo` — durable todo tools per S130500 §00–02: `todo_read` (status filter + limit, ordered by priority then activity), `todo_write` (atomic full-replace returning assigned ids; rollback-on-failure leaves the prior list intact), and `todo_update` (partial updates by id — status/text/priority/remove — with atomic batch rejection on unknown ids). Registered through the stateful storage seam; per-navi persistence via `store.StatefulToolStore` (NAVI-268)
- Daemon tool wiring (S130400, NAVI-272): the daemon assembles a tool registry — builtins (`file_read`, `shell_exec`) register with complete metadata through the new public `tools/builtin` package — and the default active set is composed from the runtime `ToolPolicy` (allowed/blocked lists, per-tool timeout overrides); each thought run's executor is rebuilt from the registry's active set via `tools/bridge`, so the LLM-facing tool surface is registry-governed; `navi daemon` gains `--navi-id` (per-navi scoping key for stateful tool storage) and `navi health` surfaces the navi's todo counts by status (pending/in_progress/done) from the stateful store (NAVI-272)
- `tools/note` — stateful note tools (S130500 §04–05): `note_read` (by id / tag / literal substring, never semantic; over-cap bodies truncated with a notice) and `note_write` (verbatim upsert by id, `remove: true` deletion, 64 KiB per-note and 10 MiB per-navi caps both configurable at registration), registered via `registry.RegisterStateful` with complete metadata (NAVI-270)
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
