---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100200, 100201, 100202, 100203]
---

# 02 Rate Limiter

> Token bucket rate limiter with per-endpoint, per-credential, and global scopes, plus rate limit header exposure.

## 00 Per-Endpoint Rate Limiting

Each unique API endpoint path maintains its own rate limit bucket, allowing fine-grained control over request frequency to different parts of a provider API.

### Requirement:

- The system SHALL support per-endpoint rate limiting (FR-3.1).

### Scenario:

- GIVEN a per-endpoint rate limit of 10 requests per second, WHEN 11 requests are sent to the same endpoint within one second, THEN the 11th request blocks until the next window (TC-4).
- GIVEN a per-endpoint rate limit, WHEN requests are sent to different endpoints, THEN each endpoint has its own independent bucket.

### Design Notes

The `endpoint` scope maintains a token bucket per unique endpoint path. This is useful when different API endpoints have different rate limits (e.g., Stripe's `/v1/charges` may have a different limit than `/v1/customers`).

```go
type RateLimiter interface {
    Wait(ctx context.Context, endpoint string) error
    Metrics() RateLimitMetrics
}

type RateLimitConfig struct {
    Scope    string `yaml:"scope" json:"scope"`          // "endpoint" | "credential" | "global"
    Requests int    `yaml:"requests" json:"requests"`     // max requests
    Per      string `yaml:"per" json:"per"`               // "second" | "minute" | "hour"
    Burst    int    `yaml:"burst" json:"burst"`           // burst allowance
}
```

Implementation uses a token bucket algorithm per scope. The `endpoint` scope maintains a bucket indexed by endpoint path.

---

## 01 Per-Credential Rate Limiting

All requests using the same credential share a single rate limit bucket, regardless of the endpoint being called. This is useful when a provider enforces limits per API key.

### Requirement:

- The system SHALL support per-credential rate limiting (FR-3.2).

### Scenario:

- GIVEN a per-credential rate limit of 100 requests per minute, WHEN requests to different endpoints exceed 100 within one minute, THEN subsequent requests block until the next window.
- GIVEN two adapters with different credentials, WHEN each adapter sends requests, THEN each credential has its own independent bucket.

### Design Notes

The `credential` scope shares a bucket across all endpoints for a given credential. This is the most common provider pattern -- API keys have a shared rate limit across all endpoints.

---

## 02 Global Rate Limiting

A single rate limit bucket applies to all requests across the adapter, regardless of endpoint or credential. This serves as a safety net to prevent excessive overall request volume.

### Requirement:

- The system SHALL support global rate limiting across all endpoints (FR-3.3).

### Scenario:

- GIVEN a global rate limit of 1000 requests per hour, WHEN the adapter exceeds 1000 requests across all endpoints within one hour, THEN subsequent requests block until the next hour.
- GIVEN per-endpoint, per-credential, and global rate limits configured, WHEN a request is made, THEN all applicable limits are checked in order (most restrictive applies).

### Design Notes

The `global` scope limits all requests across the adapter. When multiple scopes are configured, the most restrictive limit applies. The rate limiter checks all configured scopes sequentially.

---

## 03 Rate Limit Headers

The rate limiter exposes current rate limit state through Prometheus metrics and optionally populates rate limit headers (remaining, reset, retry-after) for downstream consumers.

### Requirement:

- The system SHALL expose rate limit headers (remaining, reset, retry-after) (FR-3.4, Should).

### Scenario:

- GIVEN a rate-limited request, WHEN the request is processed, THEN the rate limiter metrics show remaining, reset time, and total limit.
- GIVEN a rate limit that is about to be exceeded, WHEN `Metrics()` is called, THEN the returned metrics show the remaining count approaching zero and the reset time.

### Design Notes

```go
type RateLimitMetrics struct {
    Scope       string
    Remaining   int
    ResetAt     time.Time
    TotalLimit  int
}
```

Rate limit metrics are exposed via Prometheus for observability. The `Metrics()` method returns the current state of the rate limiter, which can be used by health check endpoints and monitoring dashboards.

### Rate Limiter Edge Cases

| Edge Case | Expected Behavior |
|-----------|-------------------|
| Burst within limit | All requests succeed immediately |
| Rate exceeded | Request blocks until next window |
| Context timeout | Wait returns context.DeadlineExceeded |
| Concurrent requests | Token bucket handles concurrent access safely |
| Rate limit headers | Mock server returns 429 with Retry-After; adapter reads Retry-After and waits |

### Scenario:

- GIVEN a context deadline shorter than the rate limit wait time, WHEN `Wait` is called, THEN it returns `context.DeadlineExceeded`.
- GIVEN 10 goroutines sending requests simultaneously, WHEN the rate limiter processes them, THEN the token bucket handles concurrent access safely.

---

## Implementation Notes

### Phase 1 Tasks

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Implement `RateLimiter` (token bucket, 3 scopes) | P1 | M | HttpAdapter |

### Testing

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Write unit tests for rate limiter covering all edge cases | P1 | M | Rate limiter |

### Test Coverage

The rate limiter tests cover:
- Burst within limit: send N requests where N <= burst, all succeed immediately
- Rate exceeded: send N+1 requests where N > requests per window, (N+1)th blocks
- Global scope: requests across multiple endpoints enforce global limit
- Per-endpoint scope: each endpoint has its own bucket
- Context timeout: context deadline < wait time returns DeadlineExceeded
- Concurrent requests: 10 goroutines, token bucket handles concurrent access
- Rate limit headers: mock server returns 429 with Retry-After

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-3 Rate Limiter |