# internal/agent — layout map

This package implements the agent runtime (`docs/specs/01-agent-runtime/03-agent-runtime.md`).
The spec's §09 file structure is the original sketch; the layout below is the
actual one, which diverged during implementation (#148, PR #217). The review
concluded the actual layout is better kept — this document maps the two so
the spec stays honest without renaming working code.

## Spec §09 → actual files

| Spec §09 entry | Actual location | Notes |
|---|---|---|
| `agent.go` — Agent struct, NewAgent, public API | `agent.go` | matches; also hosts the `ProviderInterface` seam |
| `state.go` — AgentState, copy-on-write mutations | **folded into `agent.go`** | no separate state file; `Agent` holds state directly — a distinct file only paid off once state outgrew the struct, and it hasn't |
| `events.go` — Event types, EventBus, AgentEndBarrier | `events.go` | matches |
| `loop.go` — Core execution loop | `loop.go` | matches (`runLoop`, spec section 07) |
| `tools.go` — ToolExecutor, before/after hooks | **split**: `executor.go` + `tools.go` | see below |
| `queue.go` — SteeringQueue, FollowUpQueue | `queue.go` | matches |
| `transform.go` — Context transformation pipeline | `transform.go` | matches (`TransformContext`, spec section 06) |
| `types.go` — AgentMessage, AgentEvent, AgentTool, AgentConfig | `types.go` | matches, plus `Role` and related enums |

## Files the spec didn't anticipate

| File | Role |
|---|---|
| `executor.go` | `ToolExecutor` — tool-call dispatch with parallel/sequential modes and a parallelism limit. Split out of `tools.go` because dispatch semantics (mode, maxParallel, hooks) are independent of which tools exist |
| `tools.go` | Built-in tool implementations (`file_read`, `shell_exec`) with byte caps, plus `RegisterBuiltinTools` binding them to a working directory |
| `compaction.go` | Context-window compaction (`docs/specs/03-session-context/02-context-window-management.md` §3.3): LLM summaries replace older messages near the token budget, with a tool-call-integrity-preserving cut point |
| `provider.go` | Adapter seam between the agent's Message/Event types and the concrete LLM provider layer (`internal/llm`, #187). The run loop is untouched by provider changes — only this file moves |

## Rationale for the deviations

1. **No `state.go`** — the copy-on-write state file was speculative; the
   `Agent` struct carries state fine at current size. Reintroduce a state
   file if mutation logic grows.
2. **Executor/tool split** — "what runs tools" and "which tools exist" are
   different axes; the split keeps the executor testable with mock tools
   (`mock_test.go`) and lets the tool catalog (S130400 registry) slot in
   without touching dispatch.
3. **Compaction and provider files** — §09 predates the session-context and
   provider-layer work; both are clean single-concern files that earned
   their place.
