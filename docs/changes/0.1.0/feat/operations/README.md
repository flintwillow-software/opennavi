# 0.1.0 — feat: operations

> Derived from initial spec tree component 08 (Operations). Implements operational infrastructure for running navi agents in production.

## Summary

Implement the operations framework: structured logging, metrics collection, health endpoints, configuration management, secret handling, rate limiting, circuit breaker, retry logic, and deployment lifecycle management for navi agents.

## Changes

- **`pkg/ops/logging/`** — Structured logging: Log format (JSON), levels, context injection, log shipping.
- **`pkg/ops/metrics/`** — Metrics: OpenTelemetry metrics export, counters/histograms for agent operations.
- **`pkg/ops/health/`** — Health checks: Readiness and liveness endpoints, dependency health probes.
- **`pkg/ops/retry/`** — Retry logic: Exponential backoff, jitter, max-retries, circuit breaker integration.
- **`pkg/ops/circuit/`** — Circuit breaker: State machine (closed/open/half-open), failure threshold, recovery timeout.
- **`pkg/ops/ratelimit/`** — Rate limiting: Token bucket, per-provider limits, per-tool limits.
- **`pkg/ops/deploy/`** — Deployment lifecycle: Start/stop/restart hooks, graceful shutdown, upgrade migration.

## Spec Refs

[080000, 080100, 080200, 080300, 080400, 080500, 080600, 080700, 080800, 080900, 081000]

## Code Context

- **Project structure**: Go SDK, cross-cutting concern used by all other components
- **Dependencies**: OpenTelemetry SDK, structured logging library (logrus/zap)
- **Existing patterns**: Cross-cutting — every component uses ops infrastructure

## Notes

- Epic: "Agent Operations Infrastructure"
- Independent of other components — can be built in parallel
- Health checks are required by smial deployment (monitoring/health specs)
- Circuit breaker and rate limiting are critical for integration adapters

## Tracked

- Epic: Agent Operations Infrastructure (#190) — https://github.com/flintwillow-software/opennavi/issues/190 [priority: P1]
  - Issue: Implement structured logging (#191) — https://github.com/flintwillow-software/opennavi/issues/191 [priority: P1]
  - Issue: Implement metrics (OpenTelemetry) (#192) — https://github.com/flintwillow-software/opennavi/issues/192 [priority: P1]
  - Issue: Implement health checks (#193) — https://github.com/flintwillow-software/opennavi/issues/193 [priority: P1]
  - Issue: Implement retry logic (#195) — https://github.com/flintwillow-software/opennavi/issues/195 [priority: P1]
  - Issue: Implement circuit breaker (#196) — https://github.com/flintwillow-software/opennavi/issues/196 [priority: P1]
  - Issue: Implement rate limiting (#197) — https://github.com/flintwillow-software/opennavi/issues/197 [priority: P1]
  - Issue: Implement deployment lifecycle (#198) — https://github.com/flintwillow-software/opennavi/issues/198 [priority: P1]
