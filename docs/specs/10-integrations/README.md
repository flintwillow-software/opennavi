---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - integrations
priority: high
spec-refs: [100000, 100100, 100200, 100300, 100400, 100500]
---

# 10 Integrations

> External API integrations framework — banking, accounting, SIEM, ticketing, payment, creative, calendar, IoT, communication.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S100000 Adapter Interface](00-adapter-interface.md) | Adapter Interface | planned | IntegrationAdapter interface, layered composition |
| [S100100 Auth Layer](01-auth-layer.md) | Auth Layer | planned | OAuth 2.0, API key, mTLS, token refresh |
| [S100200 Rate Limiter](02-rate-limiter.md) | Rate Limiter | planned | Per-endpoint, per-credential, global rate limiting |
| [S100300 Retry & Circuit Breaker](03-retry-circuit-breaker.md) | Retry & Circuit Breaker | planned | Exponential backoff, circuit breaker, health checks |
| [S100400 Credential Resolver](04-credential-resolver.md) | Credential Resolver | planned | Env vars, encrypted files, Vault, priority ordering |
| [S100500 Webhook Listener](05-webhook-listener.md) | Webhook Listener | planned | Webhook reception, payload normalization, signature verification |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Common IntegrationAdapter interface with composable layers
- Multi-provider auth with OAuth 2.0, API key, mTLS
- Rate limiting, retry with exponential backoff, and circuit breaker

## Related

- [Credential Vault](../08-operations/09-credential-vault.md)
