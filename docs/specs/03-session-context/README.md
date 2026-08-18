---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - session-context
priority: high
spec-refs: [030000, 030100, 030200]
---

# 03 Session Context

> Session persistence, context window management, and webhook response schemas.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S030000 Webhook Response Schema](00-webhook-response-schema.md) | Webhook Response Schema | draft | Webhook patterns, error schema, HMAC verification |
| [S030100 Session Management](01-session-management.md) | Session Management | draft | SQLite sessions, branching, compaction, export |
| [S030200 Context Window Management](02-context-window-management.md) | Context Window Management | draft | Token budgeting, compaction, overflow handling |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- SQLite + JSONL dual-format session storage
- Per-thread context window with token budgeting and overflow handling
- Webhook ID deduplication and HMAC signature verification

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
- [Memory System](../06-memory/README.md)
