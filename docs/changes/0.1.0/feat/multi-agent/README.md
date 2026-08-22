# 0.1.0 — feat: multi-agent

> Derived from initial spec tree component 04 (Multi-Agent). Implements peer-to-peer agent communication, trust identity, team-of-teams orchestration, and the multi-agent coordination system.

## Summary

Implement multi-agent capabilities: P2P chat with presence detection, trust and identity key management, IAM with idle scheduling, team-of-teams orchestration, council-based termination, and the full multi-agent system coordination layer.

## Changes

- **`pkg/p2p/`** — P2P chat & presence: Direct agent-to-agent messaging, presence announcement/discovery, heartbeat, connection management.
- **`pkg/p2p/identity/`** — Trust & identity: Key pair generation, identity certificate, peer verification, session establishment.
- **`pkg/iam/`** — IAM & idle scheduling: Agent identity management, permission grants, idle detection, sleep/wake scheduling.
- **`pkg/orchestration/team/`** — Team-of-teams: Hierarchical agent group management, cross-team message routing, delegation protocol.
- **`pkg/orchestration/council/`** — Council termination: Multi-agent consensus protocol for terminating decisions (e.g., "has this task been completed?").
- **`pkg/orchestration/`** — Multi-agent system: Coordination framework, task distribution, result aggregation, conflict resolution.

## Spec Refs

[040000, 040100, 040200, 040300, 040400, 040500, 040600]

## Code Context

- **Project structure**: Go SDK, builds on agent-runtime and session-context
- **Dependencies**: libp2p or custom P2P over WebRTC/QUIC, ed25519 (identity), protobuf (wire format)
- **Existing patterns**: N/A — greenfield

## Notes

- Epic: "Multi-Agent Coordination"
- Depends on: agent-runtime (agent loop), trust/signing from navi-core
- Team-of-teams and council termination are advanced features that can be deferred to a later phase
- P2P layer is foundational — all other multi-agent features depend on reliable agent-to-agent messaging

## Tracked

- Epic: Multi-Agent Coordination (#164) — https://github.com/flintwillow-software/opennavi/issues/164 [priority: P1]
  - Issue: Implement P2P chat & presence (#165) — https://github.com/flintwillow-software/opennavi/issues/165 [priority: P1]
  - Issue: Implement trust & identity (#166) — https://github.com/flintwillow-software/opennavi/issues/166 [priority: P1]
  - Issue: Implement IAM & idle scheduling (#167) — https://github.com/flintwillow-software/opennavi/issues/167 [priority: P1]
  - Issue: Implement team-of-teams orchestration (#168) — https://github.com/flintwillow-software/opennavi/issues/168 [priority: P1]
  - Issue: Implement council termination (#169) — https://github.com/flintwillow-software/opennavi/issues/169 [priority: P1]
  - Issue: Implement multi-agent coordination framework (#170) — https://github.com/flintwillow-software/opennavi/issues/170 [priority: P1]
