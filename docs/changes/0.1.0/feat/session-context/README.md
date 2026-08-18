# 0.1.0 — feat: session-context

> Derived from initial spec tree component 03 (Session Context). Manages session lifecycle, context window construction, and webhook response schema for gateway-mode operation.

## Summary

Implement session context management: session create/resume/close lifecycle, dynamic context window construction from project context + session history + relevant files + git state, and webhook response schema for the Gateway interface.

## Changes

- **`pkg/runtime/context/`** — Context window builder: Dynamic assembly of the LLM context window from multiple sources (system prompt, project context, session history, file contents, git diff).
- **`pkg/runtime/context/compactor/`** — Context compaction: Summarization, pruning, and deferral strategies when window fills.
- **`pkg/gateway/webhook/`** — Webhook response schema: Structured response format for the Gateway interface (async webhook delivery of tool results).
- **`internal/store/sessions/`** — Session persistence store: Session CRUD, full-text search of session history, export/import.

## Spec Refs

[030000, 030100, 030200]

## Code Context

- **Project structure**: Go SDK, depends on agent-runtime (session model) and navi-core (project layout)
- **Dependencies**: SQLite, text summarization via LLM
- **Existing patterns**: Builds on the session model from agent-runtime

## Notes

- Epic: "Agent Runtime & Session System" (same as agent-runtime)
- Depends on: agent-runtime (session model), messaging (webhook delivery)
- Context compaction strategy is a key quality differentiator — worth dedicated research/design