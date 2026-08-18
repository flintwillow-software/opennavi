---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100500, 100501, 100502]
---

# 05 Webhook Listener

> Receives webhooks from external services, normalizes per-service payloads into a common event format, and verifies webhook signatures (HMAC, JWT, or provider-specific).

## 00 Webhook Reception

The webhook listener exposes an HTTP endpoint that receives incoming webhooks from external services. The endpoint is configurable per adapter and supports multiple providers.

### Requirement:

- The system SHALL support receiving webhooks from external services (FR-6.1).

### Scenario:

- GIVEN an adapter with a webhook listener configured, WHEN an external service sends a webhook POST to the listener endpoint, THEN the webhook is accepted and queued for processing.
- GIVEN an adapter without a webhook listener configured, WHEN no webhook endpoint is registered, THEN the adapter does not accept webhook requests.

### Design Notes

The webhook listener receives HTTP requests at a configurable endpoint, verifies the signature, normalizes the payload into a `WebhookEvent`, and dispatches it to the navi's event system.

```go
type WebhookEvent struct {
    Provider  string            `json:"provider"`           // e.g., "stripe", "splunk", "zendesk"
    EventID   string            `json:"event_id"`           // provider's event ID (for dedup)
    Type      string            `json:"type"`               // normalized event type
    Raw       json.RawMessage   `json:"raw"`                // original provider payload
    Timestamp time.Time         `json:"timestamp"`          // event time (from provider)
    Metadata  map[string]string `json:"metadata,omitempty"` // extracted context
}

type WebhookHandler interface {
    HandleEvent(ctx context.Context, event WebhookEvent) error
}
```

---

## 01 Payload Normalization

Each external service has its own webhook payload format. The webhook listener normalizes these per-service payloads into a common `WebhookEvent` struct, extracting provider, event ID, type, raw payload, timestamp, and metadata.

### Requirement:

- The system SHALL normalize per-service webhook payloads into a common event format (FR-6.2).

### Scenario:

- GIVEN a webhook from Stripe (e.g., `charge.succeeded`), WHEN the handler normalizes the payload, THEN the `WebhookEvent` has provider="stripe", eventID="evt_123", type="charge.succeeded", and the raw payload preserved (TC-9).
- GIVEN a webhook from Zendesk (e.g., `ticket.created`), WHEN the handler normalizes the payload, THEN the `WebhookEvent` has provider="zendesk", type="ticket.created", and the appropriate metadata extracted.

### Design Notes

Each provider has a normalization function that maps its webhook payload to the `WebhookEvent` struct. The `Raw` field preserves the original provider payload for downstream consumers that need provider-specific details.

Normalization follows this pattern:
1. Identify the provider from the request path or header
2. Parse the provider-specific payload
3. Extract the event ID, type, timestamp, and metadata
4. Return a `WebhookEvent` with the normalized fields

### User Stories

- **US-007**: As Dana (Support Manager), I want my navi to process incoming emails and create tickets, so that customer support is automated. The webhook listener normalizes incoming email webhooks into events that trigger ticket creation.

---

## 02 Signature Verification

Webhook payloads are verified before processing to ensure they came from the claimed provider and have not been tampered with. The verifier supports HMAC, JWT, and provider-specific signature methods.

### Requirement:

- The system SHALL verify webhook signatures (HMAC, JWT, or provider-specific) (FR-6.3).

### Scenario:

- GIVEN a webhook with a valid HMAC signature, WHEN the verifier checks the signature, THEN verification passes and the event is processed (TC-10).
- GIVEN a webhook with an invalid signature, WHEN the verifier checks the signature, THEN verification fails and the event is rejected with an error (TC-11).

### Design Notes

```go
type WebhookVerifier interface {
    Verify(payload []byte, signature string) error
}
```

The `WebhookVerifier` interface supports multiple verification methods:
- **HMAC**: Verify the payload against a shared secret using HMAC-SHA256
- **JWT**: Verify the JWT signature in the webhook header
- **Provider-specific**: Custom verification for providers with unique signing methods (e.g., Stripe's signature scheme)

The verifier is configured per-provider. The signing secret is resolved from the credential resolver chain, keeping it secure and out of configuration files.

### Scenario:

- GIVEN a webhook with a missing signature header, WHEN the verifier checks, THEN verification fails with an error indicating no signature found.
- GIVEN a webhook with a malformed signature, WHEN the verifier checks, THEN verification fails with an error indicating invalid signature format.

---

## Implementation Notes

### Phase 2: OAuth Flow + Webhook Listener

**Goal:** Add OAuth 2.0 authentication support and the webhook listener pattern.

**Exit criteria:** Adapters can use OAuth 2.0 (authorization code, client credentials, device code) and receive/normalize webhooks.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Define `WebhookEvent` struct and `WebhookHandler` interface | P1 | S | -- |
| Implement `WebhookVerifier` (HMAC, JWT, raw signature) | P1 | M | WebhookEvent |
| Implement webhook HTTP endpoint + event normalization | P1 | L | WebhookVerifier |

### Integration Tests

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Write integration tests for webhook signature verification | P1 | S | WebhookVerifier |

### Test Coverage

The webhook tests cover:
- Stripe webhook normalization (charge.succeeded)
- Zendesk webhook normalization (ticket.created)
- Valid HMAC signature verification
- Invalid signature rejection
- Missing signature header handling
- Malformed signature handling

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-6 Webhook Listener |