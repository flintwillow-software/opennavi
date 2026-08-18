---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100300, 100301, 100302]
---

# 03 Retry & Circuit Breaker

> Configurable retry with exponential backoff and jitter, circuit breaker pattern with open/half-open/closed states, and health check endpoints.

## 00 Retry with Exponential Backoff

When a request fails with a retryable error (429, 5xx, or network error), the adapter retries with exponential backoff and jitter to avoid thundering herd problems on service recovery.

### Requirement:

- The system SHALL support configurable retry with exponential backoff and jitter (FR-4.1).

### Scenario:

- GIVEN a retry config with MaxRetries=3 and BaseDelay=1s, WHEN the first request fails with a 500 error, THEN the adapter retries up to 3 times with increasing delays (TC-5).
- GIVEN a retry config with non-retryable errors (e.g., 400), WHEN the request fails with a 400 error, THEN no retry is attempted and the error is returned immediately.

### Design Notes

```go
type RetryPolicy struct {
    MaxRetries      int           `yaml:"max_retries" json:"max_retries"`               // default: 3
    BaseDelay       time.Duration `yaml:"base_delay" json:"base_delay"`                 // default: 1s
    MaxDelay        time.Duration `yaml:"max_delay" json:"max_delay"`                   // default: 30s
    Jitter          float64       `yaml:"jitter" json:"jitter"`                         // default: 0.2 (20%)
    RetryableErrors []int         `yaml:"retryable_errors" json:"retryable_errors"`     // HTTP status codes to retry
}
```

Retry flow:
1. Execute request
2. If response is a retryable error (429, 5xx, network error), check circuit breaker state
3. If closed or half-open, retry with exponential backoff + jitter
4. If open, fast-fail

Exponential backoff uses full jitter strategy: `delay = random(0, min(maxDelay, baseDelay * 2^attempt))`. This avoids the thundering herd problem when multiple clients retry simultaneously after a service outage.

| Decision | Context | Chosen | Consequences |
|----------|---------|--------|--------------|
| Exponential backoff with jitter | Retry storm prevention | Full jitter strategy | Avoids thundering herd on service recovery |

### Scenario:

- GIVEN a retry attempt with attempt=2, baseDelay=1s, and jitter=0.2, WHEN the delay is calculated, THEN the delay is between 0 and min(maxDelay, 4s) with jitter applied.
- GIVEN a context cancellation during a retry delay, WHEN the context is cancelled, THEN the retry loop stops and the context error is returned.

---

## 01 Circuit Breaker

The circuit breaker monitors consecutive failures and opens the circuit when a configurable threshold is exceeded, preventing requests from reaching a failing service. After a recovery time, the circuit transitions to half-open and allows probe requests.

### Requirement:

- The system SHALL support a circuit breaker pattern (open, half-open, closed states) (FR-4.2).

### Scenario:

- GIVEN a circuit breaker with threshold=5, WHEN 5 consecutive requests fail, THEN the circuit transitions to open and subsequent requests fast-fail without calling the service (TC-6).
- GIVEN a circuit breaker in open state, WHEN the recovery time (30s) elapses, THEN the circuit transitions to half-open and allows one probe request.
- GIVEN a circuit breaker in half-open state, WHEN the probe request succeeds, THEN the circuit transitions to closed and normal operation resumes.
- GIVEN a circuit breaker in half-open state, WHEN the probe request fails, THEN the circuit transitions back to open and the recovery timer restarts.

### Design Notes

```go
type CircuitBreakerState int
const (
    CircuitClosed   CircuitBreakerState = iota // normal operation
    CircuitHalfOpen                            // probe requests allowed
    CircuitOpen                                // fast-fail all requests
)

type CircuitBreaker struct {
    Threshold    int           // consecutive failures before opening (default: 5)
    RecoveryTime time.Duration // time before transitioning to half-open (default: 30s)
    HalfOpenMax  int           // max probe requests in half-open state (default: 1)
}
```

The circuit breaker is a classic implementation with three states:
- **Closed**: Normal operation. Requests are sent to the service. Failures are counted.
- **Open**: Fast-fail all requests. After `RecoveryTime`, transitions to half-open.
- **Half-Open**: Allows a limited number of probe requests. Success transitions to closed; failure transitions back to open.

Circuit breaker state transitions are logged at warn level for observability.

### Scenario:

- GIVEN a circuit breaker that just opened, WHEN a request arrives immediately, THEN it fast-fails without calling the service.
- GIVEN a circuit breaker in open state, WHEN the recovery time has not elapsed, THEN all requests fast-fail.

---

## 02 Health Check Endpoints

Each integration adapter exposes a health check endpoint that reports the adapter's connectivity to its external service and the current state of the circuit breaker.

### Requirement:

- The system SHALL expose health check endpoints for each integration (FR-4.3, Should).

### Scenario:

- GIVEN an adapter with a healthy connection, WHEN `Health` is called, THEN it returns nil.
- GIVEN an adapter whose service is unreachable, WHEN `Health` is called, THEN it returns an error describing the failure.

### Retry & Circuit Breaker Edge Cases

| Edge Case | Test Approach | Expected Behavior |
|-----------|---------------|-------------------|
| Transient failure | Mock returns 500 then 200 | Retry succeeds, circuit stays closed |
| Persistent failure | Mock returns 500 for all retries | Returns error after MaxRetries exhausted |
| Circuit opens | 5 consecutive failures | Circuit transitions to open, fast-fail |
| Circuit half-open | Wait RecoveryTime, then send request | Circuit transitions to half-open, probe sent |
| Circuit recovers | Half-open probe succeeds | Circuit transitions to closed, normal operation |
| Non-retryable error | Mock returns 400 | No retry, error returned immediately |
| Backoff timing | Verify delay between retries | Delay follows exponential backoff + jitter |
| Context cancellation | Cancel context mid-retry | Retry loop stops, context error returned |

---

## Implementation Notes

### Phase 1 Tasks

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Implement `RetryPolicy` + `CircuitBreaker` | P1 | M | HttpAdapter |

### Testing

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Write unit tests for retry and circuit-breaker covering all edge cases | P1 | M | Rate limiter, retry |

### Test Coverage

The retry and circuit breaker tests cover all 8 edge cases listed above, plus integration tests that verify the full flow against a mock HTTP server.

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-4 Retry & Circuit Breaker |