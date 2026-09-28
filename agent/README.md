# agent — layout map

The public agent runtime: the think → act → observation loop, its streaming
event bus, and tool execution. See
[`docs/architecture/runtime.md`](../docs/architecture/runtime.md) for how it
fits the navi composition root.

## Files

| File | Role |
|---|---|
| `agent.go` | `Agent` struct, `NewAgent`, and the public runtime API (`Prompt`, `Continue`, `Abort`, `Subscribe`, `State`); also hosts the `ProviderInterface` seam |
| `types.go` | `AgentMessage`, `AgentEvent`, `AgentTool`, `AgentConfig`, `AgentState`, `Role`, and related enums |
| `events.go` | Event types, the event bus, and the agent-end barrier |
| `loop.go` | The core execution loop (`runLoop`) |
| `transform.go` | The context transformation pipeline (`TransformContext`) |
| `executor.go` | `ToolExecutor` — tool-call dispatch with parallel/sequential modes and a parallelism limit |
| `tools.go` | Built-in tool implementations (`file_read`, `shell_exec`) with byte caps, plus `RegisterBuiltinTools` binding them to a working directory |
| `queue.go` | `SteeringQueue`, `FollowUpQueue` |
| `compaction.go` | Context-window compaction: LLM summaries replace older messages near the token budget, with a tool-call-integrity-preserving cut point |
| `provider.go` | Adapter seam between the agent's `Message`/`Event` types and the concrete provider layer |

## Notes

- **State lives on `Agent`** — there is no separate state file. Copy-on-write
  mutation is a method on `Agent` (`State()`, `setState`); a distinct state
  file only pays off once mutation logic outgrows the struct.
- **Executor and tools are split** — "what runs tools" and "which tools exist"
  are different axes; the split keeps the executor testable with mock tools
  (`mock_test.go`) and lets the tool catalog slot in without touching dispatch.
- **Compaction and provider are single-concern files** — context budgeting and
  provider translation are independent of the loop, so they live on their own.