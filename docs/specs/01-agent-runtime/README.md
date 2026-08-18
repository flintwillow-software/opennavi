---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - agent-runtime
priority: high
spec-refs: [010000, 010100, 010200, 010300, 010400]
---

# 01 Agent Runtime

> Core execution engine — the agent loop, thought queue, tool execution, error recovery, and session management.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S010000 Thoughts & Queue](00-thought-queue.md) | Thoughts & Queue | draft | Thought structure, queue, priority management |
| [S010100 Workers & Navi Communication](01-workers-navi-comm.md) | Workers & Navi Communication | draft | Workers, inter-navi communication, lifecycle |
| [S010200 Sessions & Implementation](02-sessions-implementation.md) | Sessions & Implementation | draft | Session logging, integration, implementation order |
| [S010300 Agent Runtime](03-agent-runtime.md) | Agent Runtime | draft | Agent loop, state, events, tools, steering |
| [S010400 Tool Error Recovery](04-tool-error-recovery.md) | Tool Error Recovery | draft | Partial output, truncation, failure strategies |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Per-thought session management with SQLite + JSONL
- Priority-based thought queue with slot management and crash recovery
- Parallel/sequential tool execution with event streaming
- Graceful tool error handling with partial output and truncation recovery

## Related

- [Navi Core](../00-navi-core/README.md)
- [Agent Config](../02-agent-config/README.md)
- [Session Context](../03-session-context/README.md)
- [Multi-Agent](../04-multi-agent/README.md)
