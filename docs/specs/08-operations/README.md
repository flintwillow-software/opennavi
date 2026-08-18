---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - operations
priority: high
spec-refs: [080000, 080100, 080200, 080300, 080400, 080500, 080600, 080700, 080800, 080900, 081000]
---

# 08 Operations

> Cross-cutting concerns — concurrency, logging, deployment, API versioning, incident response, and credential management.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S080000 Concurrency & Logging](00-concurrency-logging.md) | Concurrency & Logging | draft | Concurrency model, error handling, logging |
| [S080100 Daemon & Cron](01-daemon-cron.md) | Daemon & Cron | draft | Autonomous mode, daemon lifecycle, cron scheduler |
| [S080200 Testing, Security & Versioning](02-testing-security-versioning.md) | Testing, Security & Versioning | draft | Testing, resource management, plugin security, API versioning |
| [S080300 Webhooks & User Channels](03-webhooks-user-channels.md) | Webhooks & User Channels | draft | Webhook events, user interaction channels, message flow |
| [S080400 Council & Health](04-council-health-impl.md) | Council & Health | draft | Council from chat, health monitoring, auth, multi-profile |
| [S080500 Deployment & Operations](05-deployment-and-operations.md) | Deployment & Operations | draft | Service management, Docker, updates, health, backup |
| [S080600 API Versioning](06-api-versioning.md) | API Versioning | draft | REST, WebSocket, plugin, config versioning strategies |
| [S080700 Autonomous Mode Interaction](07-autonomous-mode-interaction.md) | Autonomous Mode Interaction | draft | Daemon lifecycle, webhooks, user channels, council |
| [S080800 Incident Response](08-incident-response.md) | Incident Response | draft | 8-step protocol, escalation rules, runbook system |
| [S080900 Credential Vault](09-credential-vault.md) | Credential Vault | draft | File encryption, env vars, HashiCorp Vault |
| [S081000 Team Deployment](10-team-deployment.md) | Team Deployment | draft | Per-role builds, systemd services, horizontal scaling |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Structured concurrency with bounded goroutine pools
- Autonomous mode with daemon lifecycle and cron scheduler
- API versioning for REST, WebSocket, plugin, and config
- 8-step incident response protocol with escalation and runbooks

## Related

- [Navi Core](../00-navi-core/README.md)
- [Agent Runtime](../01-agent-runtime/README.md)
