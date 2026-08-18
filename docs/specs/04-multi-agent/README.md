---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - multi-agent
priority: high
spec-refs: [040000, 040100, 040200, 040300, 040400, 040500, 040600]
---

# 04 Multi-Agent

> Multi-agent collaboration — council mode, sub-agents, P2P chat, presence, team orchestration.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S040000 P2P Chat & Presence](00-p2p-chat-presence.md) | P2P Chat & Presence | draft | Chat model, message format, presence detection |
| [S040100 Trust, Identity & Sessions](01-trust-identity-sessions.md) | Trust, Identity & Sessions | draft | Public key verification, unified identity, P2P sessions |
| [S040200 IAM & Idle Scheduling](02-iam-idle-scheduling.md) | IAM & Idle Scheduling | draft | IAM permissions, idle scheduling, SDLC pipeline |
| [S040300 Config & Security](03-config-security-design.md) | Config & Security | draft | Navi configuration, security events, audit |
| [S040400 Team Orchestration](04-team-of-teams-orchestration.md) | Team Orchestration | draft | IAM hierarchy, team registry, observability, cost |
| [S040500 Council Termination](05-council-termination.md) | Council Termination | draft | Termination conditions, patterns, edge cases |
| [S040600 Multi-Agent System](06-multi-agent-system.md) | Multi-Agent System | draft | Agent config, ChatSession, self-improvement, lifecycle |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- P2P chat with public key verification and signed messages
- Council mode via ChatSession with shared session context
- IAM hierarchy for team-level access control
- Self-improvement via tool call with user notification

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
- [Messaging](../05-messaging/README.md)
