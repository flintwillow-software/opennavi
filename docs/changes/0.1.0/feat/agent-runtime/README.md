# 0.1.0 — feat: agent-runtime

> Derived from initial spec tree component 01 (Agent Runtime). Implements the core agent loop, session management, and tool execution system that every navi uses.

## Summary

Implement the agent runtime: think → act → observation loop, session lifecycle (create, run, pause, resume, close), tool schema registration and execution, thought queue for long-running tasks, and tool error recovery with partial output salvage.

## Changes

- **`pkg/runtime/`** — Agent loop: Core reasoning → action → observation cycle with configurable LLM provider.
- **`pkg/runtime/session/`** — Session management: Create, pause, resume, close sessions. Session persistence to SQLite.
- **`pkg/runtime/tools/`** — Tool system: Tool schema registry (typed schemas), tool dispatch, access control (read/write/execute), error recovery with partial output.
- **`pkg/runtime/think/`** — Thought queue: Deferred/in-progress thought management for long-running tasks.
- **`pkg/runtime/compact/`** — Context window compaction: Summarization, pruning, deferral strategies when the context window fills.
- **`internal/store/sqlite/`** — SQLite-backed session store with at-rest encryption.

## Spec Refs

[010000, 010100, 010200, 010300, 010400]

## Code Context

- **Project structure**: Go SDK, builds on navi-core (navi.yaml parsing, project layout)
- **Dependencies**: SQLite (go-sqlite3 or modernc.org/sqlite), LLM provider SDK (Anthropic, OpenAI)
- **Existing patterns**: N/A — greenfield build

## Notes

- Epic: "Agent Runtime & Session System"
- Depends on: navi-core (navi.yaml parsing for tool config)
- Session persistence and context compaction can be built in parallel once the session data model is settled
- Tool error recovery is a critical UX concern — invest in robust testing here

## Tracked

- Epic: Agent Runtime & Session System (#147) — https://github.com/flintwillow-software/opennavi/issues/147 [priority: P0]
  - Issue: Implement agent loop (think->act->observation) (#148) — https://github.com/flintwillow-software/opennavi/issues/148 [priority: P0]
  - Issue: Implement session management (#149) — https://github.com/flintwillow-software/opennavi/issues/149 [priority: P0]
  - Issue: Implement tool system (#150) — https://github.com/flintwillow-software/opennavi/issues/150 [priority: P0]
  - Issue: Implement thought queue (#151) — https://github.com/flintwillow-software/opennavi/issues/151 [priority: P0]
  - Issue: Implement context window compaction (#152) — https://github.com/flintwillow-software/opennavi/issues/152 [priority: P0]
  - Issue: Implement SQLite session store (#153) — https://github.com/flintwillow-software/opennavi/issues/153 [priority: P0]
