---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100000, 100001, 100002, 100003]
---

# 00 Adapter Interface

> Common IntegrationAdapter interface with Init, Health, and Close methods; layered composition of optional middleware; and compile-time integration into the navi binary.

## 00 Integration Adapter Interface

Every external integration in the OpenNavi system implements a common `IntegrationAdapter` interface. This interface provides lifecycle management (init, health check, close) and is the contract that all provider-specific adapters must satisfy.

### Requirement:

- The system SHALL define a common `IntegrationAdapter` interface with `Init`, `Health`, and `Close` methods (FR-1.1).
- The system SHALL define an `AdapterConfig` struct that holds all configuration for an integration adapter, including name, provider, endpoint, auth, rate limit, retry, credential, and optional webhook settings (FR-1.1).

### Scenario:

- GIVEN an adapter configuration with valid provider settings, WHEN `Init` is called with a context and config, THEN the adapter returns nil and is ready to use (TC-1).
- GIVEN an initialized adapter, WHEN `Health` is called, THEN the adapter returns nil if the service is reachable or an error describing the failure.
- GIVEN an initialized adapter, WHEN `Close` is called, THEN the adapter cleans up connections, flushes pending requests, and revokes tokens.

### User Stories

- **US-001**: As Ava (Finance Lead), I want my navi to reconcile transactions via Plaid, so that I can automate monthly bookkeeping. The adapter interface is the foundation for building the Plaid integration.
- **US-003**: As Carlos (IT Admin), I want my navi to query Splunk/Sentinel for security events, so that my team can respond to incidents. The adapter interface enables the Splunk adapter.
- **US-011**: As Finn (Home User), I want my navi to control lights and sensors via MQTT, so that my home automations work. The adapter interface supports non-HTTP integrations like MQTT.

### Design Notes

The `IntegrationAdapter` interface is the contract that all integrations implement:

```go
type IntegrationAdapter interface {
    Init(ctx context.Context, config AdapterConfig) error
    Name() string
    Health(ctx context.Context) error
    Close(ctx context.Context) error
}

type AdapterConfig struct {
    Name       string            `yaml:"name" json:"name"`
    Provider   string            `yaml:"provider" json:"provider"`
    Endpoint   string            `yaml:"endpoint" json:"endpoint"`
    Auth       AuthConfig        `yaml:"auth" json:"auth"`
    RateLimit  RateLimitConfig   `yaml:"rate_limit" json:"rate_limit"`
    Retry      RetryConfig       `yaml:"retry" json:"retry"`
    Credential CredentialConfig  `yaml:"credential" json:"credential"`
    Webhook    *WebhookConfig    `yaml:"webhook,omitempty" json:"webhook,omitempty"`
}
```

Each adapter is responsible for a single external service or API family. The `Name()` method returns the provider name (e.g., "stripe", "splunk", "quickbooks").

---

## 01 Layered Composition

Each integration adapter is composed from optional middleware layers that provide cross-cutting capabilities: authentication, rate limiting, retry with circuit breaker, credential resolution, and webhook handling.

### Requirement:

- The system SHALL compose each adapter from optional layers: auth, rate limiter, retry, credential resolver, and webhook listener (FR-1.2).

### Scenario:

- GIVEN an adapter with auth and rate limiter layers configured, WHEN the adapter sends a request, THEN the auth layer applies credentials first and the rate limiter enforces limits before the request is dispatched.
- GIVEN an adapter with retry and credential resolver layers configured, WHEN a credential is needed, THEN the credential resolver resolves it and the retry layer handles transient failures.

### Architecture Overview

```
┌─────────────────────────────────────────────────┐
│                Integration Adapter                │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Auth Layer                         │   │
│  │  OAuth 2.0 │ API Key │ Basic │ mTLS        │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Rate Limiter                       │   │
│  │  Per-endpoint │ Per-credential │ Global     │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Retry & Circuit Breaker            │   │
│  │  Exponential backoff │ Health check       │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Credential Resolver                │   │
│  │  Env var │ File │ Vault │ Encrypted store  │   │
│  └───────────────────────────────────────────┘   │
│                                                   │
│  ┌───────────────────────────────────────────┐   │
│  │         Webhook Listener (optional)        │   │
│  │  Per-service webhook to normalized event   │   │
│  └───────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

### Design Notes

The layered architecture allows each adapter to opt into the capabilities it needs. For example, a Stripe adapter uses auth (API key), rate limiting, retry, and credential resolver layers, but does not require a webhook listener. An MQTT adapter uses credential resolver and retry, but does not use HTTP auth or rate limiting.

---

## 02 Compile-Time Integration

Integration adapters are compiled into the navi binary at build time, not loaded as runtime plugins. This ensures deterministic behavior, avoids version conflicts, and simplifies deployment.

### Requirement:

- The system SHALL allow adapters to be compiled into the navi binary at build time (FR-1.3).

### Scenario:

- GIVEN a navi binary built with the Stripe integration, WHEN the navi starts, THEN the Stripe adapter is available without additional runtime loading.
- GIVEN a navi binary built without the MQTT integration, WHEN the navi starts, THEN the MQTT adapter is not present and cannot be configured.

### Design Notes

| Decision | Context | Chosen | Consequences |
|----------|---------|--------|--------------|
| Compile-time integration | Integration loading | Adapters compiled into the binary at build time | No runtime plugin loading, but must rebuild to add integrations |

---

## 03 Non-Functional Requirements

Cross-cutting performance, reliability, security, and observability requirements for the integrations framework.

### Performance

- Each integration adapter SHALL initialize in less than 500 ms (NFR-1).
- Rate limiter overhead SHALL be less than 1 ms per request (NFR-1).
- Credential resolution SHALL complete in less than 100 ms (cached after first resolution) (NFR-1).

### Reliability

- Circuit breaker SHALL trip after 5 consecutive failures (configurable) (NFR-2).
- Retry backoff SHALL not exceed 60 seconds between attempts (NFR-2).
- Credential resolution SHALL cache resolved values for the lifetime of the credential (re-read on expiry) (NFR-2).

### Security

- Credentials SHALL never be logged or exposed in error messages (NFR-3).
- OAuth tokens SHALL be stored encrypted at rest (NFR-3).
- Webhook signatures SHALL be verified before processing (NFR-3).

### Observability

- Each adapter SHALL expose Prometheus metrics (request count, latency, errors, rate limit hits) (NFR-4).
- Each adapter SHALL log structured events at info level (startup, shutdown, auth success/failure) (NFR-4).
- Circuit breaker state transitions SHALL be logged at warn level (NFR-4).

### Scenario:

- GIVEN an adapter with Prometheus metrics enabled, WHEN the adapter processes requests, THEN request count, latency, and error metrics are exposed.
- GIVEN an adapter starting up, WHEN the init sequence completes, THEN a structured log event is emitted at info level.

---

## Implementation Notes

### Phase 1: Core Interface + Credential Resolver + HTTP Adapter

**Goal:** Define the IntegrationAdapter interface, implement the credential resolver chain, and build a reusable HTTP adapter that wraps auth, rate limiting, and retry.

**Exit criteria:** A developer can define an integration adapter, configure credentials from env/file/Vault, and make authenticated HTTP requests with rate limiting and retry.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Define `IntegrationAdapter` interface + `AdapterConfig` struct | P1 | S | -- |
| Implement `HttpAdapter` -- generic HTTP transport with auth, rate limit, retry | P1 | L | Credential resolver |

### Phase 3: Individual Integrations

**Goal:** Implement concrete integrations per use case category.

**Exit criteria:** At least one integration per category is functional and tested.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| **Stripe** integration (payment, financial) | P1 | M | Phase 1 + 2 |
| **QuickBooks** integration (accounting) | P1 | M | Phase 1 + 2 |
| **Splunk** integration (SIEM) | P1 | L | Phase 1 + 2 |
| **Zendesk** integration (ticketing) | P1 | M | Phase 1 + 2 |
| **Jira** integration (ticketing) | P1 | M | Phase 1 + 2 |
| **Okta** integration (identity) | P1 | M | Phase 1 + 2 |
| **Jamf** integration (MDM) | P2 | M | Phase 1 |
| **ShipStation** integration (shipping) | P2 | M | Phase 1 |
| **ShotGrid** integration (creative) | P2 | M | Phase 1 + 2 |
| **Google Calendar** integration (calendar) | P2 | M | Phase 1 + 2 |
| **MQTT** integration (IoT) | P1 | L | Phase 1 |
| **Email (IMAP/SMTP)** integration (communication) | P1 | L | Phase 1 + 2 |
| **Twilio** integration (SMS, communication) | P1 | S | Phase 1 |

### Order of Operations

1. **Phase 1 first** -- The core interface, credential resolver, and HTTP adapter are prerequisites for everything else.
2. **Phase 2 second** -- OAuth and webhooks are needed by most integrations.
3. **Phase 3 in parallel** -- Individual integrations can be built concurrently once Phase 1 and 2 are complete.

### File Structure Proposal

```
internal/integrations/
├── adapter.go                        # IntegrationAdapter interface + AdapterConfig
├── adapter_test.go
├── auth/
│   ├── authenticator.go              # Authenticator interface
│   ├── apikey.go                     # APIKeyAuthenticator
│   ├── basic.go                      # BasicAuthenticator
│   ├── oauth2.go                     # OAuth2Authenticator
│   ├── mtls.go                       # mTLSAuthenticator
│   └── auth_test.go
├── ratelimit/
│   ├── ratelimit.go                  # RateLimiter interface + token bucket impl
│   └── ratelimit_test.go
├── retry/
│   ├── retry.go                      # RetryPolicy + CircuitBreaker
│   └── retry_test.go
├── credential/
│   ├── resolver.go                   # CredentialResolver chain
│   ├── env.go                        # EnvSource
│   ├── vault.go                      # VaultSource
│   ├── file.go                       # FileSource
│   └── resolver_test.go
├── webhook/
│   ├── event.go                      # WebhookEvent struct
│   ├── handler.go                    # WebhookHandler interface
│   ├── verifier.go                   # WebhookVerifier (HMAC, JWT)
│   └── webhook_test.go
├── http/
│   ├── adapter.go                    # HttpAdapter -- generic HTTP transport
│   └── adapter_test.go
├── stripe/                           # Provider-specific adapters
├── quickbooks/
├── splunk/
├── zendesk/
├── jira/
├── okta/
├── jamf/
├── shipstation/
├── shotgrid/
├── googlecalendar/
├── mqtt/
├── email/
└── twilio/
```

### Integration Points

| System / Module | Integration Type | Notes |
|-----------------|------------------|-------|
| Credential Vault | Cross-reference | Credential resolver sources use the vault implementation |
| Agent Runtime | Runtime | Integration adapters are initialized at navi startup and exposed as tools |
| Webhook system | Event | Webhook events flow into the navi's event system |
| Operations | Monitoring | Adapters expose Prometheus metrics consumed by operations |

### Definition of Done

- [ ] Code reviewed and merged
- [ ] Tests written and passing
- [ ] Acceptance criteria verified
- [ ] Integration tests pass against a mock server for the provider

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-1 IntegrationAdapter Interface |