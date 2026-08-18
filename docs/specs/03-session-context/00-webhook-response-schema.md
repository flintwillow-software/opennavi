---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - session-context
priority: medium
spec-refs: [030000, 030001, 030002, 030003, 030004, 030005, 030006, 030007, 030008]
---

# Plan: Webhook Response Schema

**Scope:** Define formal response schemas for OpenNavi's HTTP webhook endpoints — covering sync, async, and fire-and-forget patterns. Based on patterns from CI/CD systems (GitHub Actions, Jenkins), monitoring tools (PagerDuty, Grafana), and standard REST practices.

---

## 00 Overview

OpenNavi webhooks accept HTTP POST requests from external systems (CI/CD, monitoring, IoT). The gateway processes these events through the agent loop and delivers responses via configured platforms (Telegram, Discord, Slack, etc.). This doc defines:
- Response schemas for each webhook pattern (sync, async, fire-and-forget)
- Error response format
- Content negotiation support

### Requirement: Webhook Requests Accepted via HTTP POST

The system SHALL accept inbound webhook events as HTTP POST requests from external systems, process them through the agent loop, and deliver responses through configured delivery platforms.

#### Scenario: Successful webhook ingestion

- GIVEN an external system sends an HTTP POST request to the gateway webhook endpoint with a valid payload
- WHEN the gateway receives the request
- THEN the gateway SHALL parse the payload, create a session, and route the event through the agent loop
- AND the system SHALL deliver the agent's response to the configured platform

### Requirement: Webhook Endpoint Requires Authentication

The system SHALL authenticate every webhook request using an HMAC-SHA256 signature carried in the `X-OpenNavi-Secret` header before processing.

#### Scenario: Unauthenticated request rejected

- GIVEN an external system sends an HTTP POST to the webhook endpoint
- WHEN the request lacks a valid `X-OpenNavi-Secret` header or the HMAC signature does not match
- THEN the gateway SHALL return a `401 Unauthorized` response with error code `unauthorized`
- AND the system SHALL NOT process the request

### Requirement: Response Schemas Defined per Pattern

The system SHALL use a distinct HTTP status code and response body schema for each webhook pattern: synchronous (200), asynchronous (202), and fire-and-forget (204).

#### Scenario: Sync pattern returns 200

- GIVEN a webhook is configured for synchronous processing
- WHEN the agent completes processing within the configured timeout
- THEN the gateway SHALL return HTTP `200 OK` with a JSON body containing `status: "ok"`, the agent response, and delivery metadata

#### Scenario: Async pattern returns 202

- GIVEN a webhook is configured for asynchronous processing
- WHEN the gateway accepts the webhook
- THEN the gateway SHALL return HTTP `202 Accepted` with a JSON body containing `status: "accepted"`, a `webhook_id`, and `estimated_delivery_platforms`

#### Scenario: Fire-and-forget returns 204

- GIVEN a webhook is configured as fire-and-forget
- WHEN the gateway acknowledges receipt
- THEN the gateway SHALL return HTTP `204 No Content` with an empty body

---

## 01 Webhook Patterns

### 2.1 Synchronous Pattern (`200 OK`)

**When used:** CI/CD status checks, quick triage requests where caller expects immediate response.

**Behavior:** Gateway waits for the agent to finish processing before returning a response. Timeout: configurable (default: 30 seconds).

```http
POST /gateway/webhook HTTP/1.1
Host: oncall-bot.example.com
X-OpenNavi-Secret: <hmac_signature>
Content-Type: application/json

{
    "user": "ci-system",
    "message": "Run health check on payment-api service",
    "platform": "github"
}
```

**Response (success):**
```http
HTTP/1.1 200 OK
Content-Type: application/json

{
    "status": "ok",
    "session_id": "abc-123-def-456",
    "response": {
        "text": "Health check results:\n- payment-api: UP (latency: 45ms)\n- database: UP (connections: 12/100)\n- cache: UP (hit rate: 98.2%)",
        "markdown": "**Health Check Results**\n\n| Service | Status | Latency |\n|---------|--------|---------|\n| payment-api | UP | 45ms |\n| database | UP | - |\n| cache | UP (hit rate: 98.2%) |",
        "tool_results": [
            {
                "tool_name": "query_grafana",
                "content": "...",
                "is_error": false
            }
        ]
    },
    "delivery": {
        "platform": "telegram",
        "channel_id": "-1001234567890",
        "message_id": 42
    },
    "processing_time_ms": 2340
}
```

**Response (timeout):**
```http
HTTP/1.1 408 Request Timeout
Content-Type: application/json

{
    "status": "timeout",
    "session_id": "abc-123-def-456",
    "message": "Agent processing exceeded timeout of 30s. Response will be delivered via configured platform.",
    "delivery_scheduled": true,
    "processing_time_ms": 30000
}
```

### 2.2 Asynchronous Pattern (`202 Accepted`)

**When used:** Long-running analysis, incident triage with multiple tool calls, any operation that may exceed the sync timeout.

**Behavior:** Gateway accepts the webhook immediately and processes it in the background. Response is delivered to configured platform(s) when complete.

```http
POST /gateway/webhook HTTP/1.1
Host: oncall-bot.example.com
X-OpenNavi-Secret: <hmac_signature>
Content-Type: application/json

{
    "user": "monitoring-system",
    "message": "Investigate error spike in payment-api service from the last 5 minutes",
    "platform": "grafana"
}
```

**Response (accepted):**
```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{
    "status": "accepted",
    "session_id": "abc-123-def-456",
    "message": "Webhook accepted. Processing in background.",
    "estimated_delivery_platforms": ["telegram", "discord"],
    "webhook_id": "wh_789xyz"
}
```

**Delivery (when complete):**
```json
// Delivered to Telegram as an edited message or new message:
{
    "status": "delivered",
    "session_id": "abc-123-def-456",
    "delivery_platform": "telegram",
    "message": {
        "text": "... full agent response text ...",
        "markdown": "... markdown version ..."
    },
    "webhook_id": "wh_789xyz"
}
```

### 2.3 Fire-and-Forget Pattern (`204 No Content`)

**When used:** Internal health pings, status updates where no response is needed.

**Behavior:** Gateway acknowledges receipt and processes in background. No delivery configured.

```http
POST /gateway/webhook HTTP/1.1
Host: oncall-bot.example.com
X-OpenNavi-Secret: <hmac_signature>
Content-Type: application/json

{
    "user": "health-checker",
    "message": "Service heartbeat from payment-api at 2026-08-02T10:30:00Z",
    "platform": "internal"
}
```

**Response:**
```http
HTTP/1.1 204 No Content

// Empty body, no Content-Type header needed
```

### Requirement: Synchronous Webhook Timeout Handling

The system SHALL enforce a configurable timeout (default 30 seconds) on synchronous webhook processing and return HTTP `408 Request Timeout` when the agent does not respond in time.

#### Scenario: Sync timeout triggers 408 response

- GIVEN an external system sends a synchronous webhook request
- WHEN the agent processing duration exceeds the configured timeout
- THEN the gateway SHALL return HTTP `408 Request Timeout`
- AND the response body SHALL include `status: "timeout"`, `session_id`, and `delivery_scheduled: true`

### Requirement: Async Webhook Delivery Notification

The system SHALL process asynchronous webhooks in the background and deliver the completed agent response to the configured platform(s) when processing finishes.

#### Scenario: Background processing completes

- GIVEN an external system sends an asynchronous webhook request
- WHEN the agent completes processing in the background
- THEN the system SHALL deliver the full response to each platform listed in `estimated_delivery_platforms`
- AND the delivery payload SHALL include `status: "delivered"`, `session_id`, and the agent response text

### Requirement: Fire-and-Forget No Delivery Guarantee

The system SHALL NOT return any response body or configure delivery for fire-and-forget webhooks; the gateway acknowledges receipt with `204 No Content` and processes the event silently.

#### Scenario: Fire-and-forget drops response

- GIVEN an external system sends a fire-and-forget webhook request
- WHEN the gateway accepts the request
- THEN the gateway SHALL return HTTP `204 No Content` with an empty body
- AND the system SHALL NOT deliver the agent response to any platform

---

## 02 Error Response Schema

### 3.1 Authentication Errors (`401 Unauthorized`)

```json
{
    "status": "error",
    "code": "unauthorized",
    "message": "Invalid webhook signature. Check X-OpenNavi-Secret header.",
    "details": {
        "expected_algorithm": "hmac-sha256",
        "provided_signature": "... (truncated for security)",
        "timestamp": "2026-08-02T10:30:00Z"
    }
}
```

### 3.2 Validation Errors (`400 Bad Request`)

```json
{
    "status": "error",
    "code": "validation_error",
    "message": "Invalid webhook payload format.",
    "details": {
        "errors": [
            {
                "field": "message",
                "reason": "required field missing"
            },
            {
                "field": "user",
                "reason": "must be a non-empty string"
            }
        ]
    }
}
```

### 3.3 Processing Errors (`500 Internal Server Error`)

```json
{
    "status": "error",
    "code": "processing_error",
    "message": "Agent processing failed.",
    "details": {
        "session_id": "abc-123-def-456",
        "error_type": "llm_provider_error",
        "error_message": "Anthropic API returned 429: rate limit exceeded",
        "retryable": true,
        "suggested_action": "Retry the webhook after waiting for rate limit to clear. Use async pattern (202) for non-critical events."
    }
}
```

### 3.4 Service Unavailable (`503 Service Unavailable`)

```json
{
    "status": "error",
    "code": "service_unavailable",
    "message": "Gateway is currently shutting down or overloaded.",
    "details": {
        "retry_after_seconds": 30,
        "active_sessions": 47,
        "max_concurrent_sessions": 50
    }
}
```

### Requirement: Standardized Error Response Format

The system SHALL return a standardized JSON error body for every error response, containing `status`, `code`, `message`, and `details` fields.

#### Scenario: Validation error returns structured body

- GIVEN an external system sends a webhook with an invalid payload
- WHEN the gateway rejects the request
- THEN the response SHALL include `status: "error"`, a machine-readable `code` (e.g. `validation_error`), a human-readable `message`, and a `details` object with field-level error descriptions

### Requirement: Retryable Error Flag

The system SHALL include a `retryable` boolean field in processing error responses to indicate whether the caller should retry the request.

#### Scenario: Retryable error guidance

- GIVEN a webhook processing fails due to a transient error (e.g. LLM provider rate limit)
- WHEN the gateway returns a `500 Internal Server Error` response
- THEN the response body SHALL include `retryable: true` in the `details` object
- AND the `details` SHALL include a `suggested_action` field with guidance for the caller

### Requirement: Service Unavailable with Retry-After

The system SHALL return HTTP `503 Service Unavailable` and include a `retry_after_seconds` field when the gateway is shutting down or overloaded.

#### Scenario: Overloaded gateway returns 503

- GIVEN the gateway is at or near capacity for concurrent sessions
- WHEN a new webhook request arrives
- THEN the gateway SHALL return HTTP `503 Service Unavailable`
- AND the response body SHALL include `retry_after_seconds` indicating when the caller should retry

---

## 03 Common Fields Across All Responses

| Field | Type | Description | Required |
|-------|------|-------------|----------|
| `status` | string | Overall status: `"ok"`, `"accepted"`, `"error"`, `"timeout"` | Yes |
| `session_id` | string | UUID of the session created/used for processing | Conditional (yes for ok/error, optional for accepted) |
| `message` | string | Human-readable message describing what happened | Yes |
| `timestamp` | string | ISO 8601 timestamp of response generation | Yes |

### Conditional Fields by Status

**When `status: "ok"`:**
- `response`: Agent's processed output (text, markdown, tool results)
- `delivery`: Where the response was delivered (platform, channel, message ID)
- `processing_time_ms`: Time taken to process in milliseconds

**When `status: "accepted"`:**
- `webhook_id`: Unique identifier for this webhook (for tracking/dedup)
- `estimated_delivery_platforms`: List of platforms where response will be delivered

**When `status: "error"`:**
- `code`: Machine-readable error code (`unauthorized`, `validation_error`, `processing_error`, etc.)
- `details`: Error-specific details (varies by error type)
- `retryable`: Boolean indicating if the caller should retry

### Requirement: Common Fields Present in All Responses

The system SHALL include the `status`, `session_id`, `message`, and `timestamp` fields in every webhook response body.

#### Scenario: All responses carry common fields

- GIVEN a webhook request is processed
- WHEN the gateway generates a response body
- THEN the response SHALL always contain a `status` string, a `session_id` UUID (conditional as per spec), a human-readable `message` string, and an ISO 8601 `timestamp`

### Requirement: Conditional Fields by Status Value

The system SHALL include status-specific conditional fields based on the value of `status`: `response` + `delivery` + `processing_time_ms` for `ok`, `webhook_id` + `estimated_delivery_platforms` for `accepted`, and `code` + `details` + `retryable` for `error`.

#### Scenario: OK response includes agent output

- GIVEN a synchronous webhook processing succeeds
- WHEN the response body has `status: "ok"`
- THEN the body SHALL include `response` (agent output with text, markdown, tool_results), `delivery` (platform, channel_id, message_id), and `processing_time_ms`

#### Scenario: Error response includes error details

- GIVEN a webhook processing fails
- WHEN the response body has `status: "error"`
- THEN the body SHALL include `code`, `details` (with error-specific fields), and `retryable`

---

## 04 Webhook ID and Deduplication

### 5.1 Webhook ID Format

```go
type WebhookID struct {
    Prefix   string    // "wh_" for webhooks, "cron_" for cron triggers
    Timestamp time.Time // When webhook was received
    Random   string    // 8-char random suffix for uniqueness
}

func generateWebhookID() WebhookID {
    return WebhookID{
        Prefix:    "wh_",
        Timestamp: time.Now(),
        Random:    generateRandomString(8),
    }
}

// Format: wh_20260802T103000Z_a1b2c3d4
func (w WebhookID) String() string {
    return fmt.Sprintf("%s%s_%s", w.Prefix, w.Timestamp.Format(time.RFC3339), w.Random)
}
```

### 5.2 Deduplication Strategy

Webhooks may be retried by external systems. To prevent duplicate processing:

```go
type WebhookDeduplicator struct {
    seen       map[string]time.Time // webhook_id → received_at
    maxAge     time.Duration        // How long to keep dedup entries (default: 24h)
    mu         sync.RWMutex
}

func (d *WebhookDeduplicator) IsDuplicate(webhookID string) bool {
    d.mu.RLock()
    _, exists := d.seen[webhookID]
    d.mu.RUnlock()
    
    if !exists {
        return false
    }
    
    // Check if still within dedup window
    receivedAt, _ := d.seen[webhookID]
    if time.Since(receivedAt) > d.maxAge {
        return false // Expired — allow processing again
    }
    
    return true
}

func (d *WebhookDeduplicator) MarkSeen(webhookID string) {
    d.mu.Lock()
    d.seen[webhookID] = time.Now()
    d.mu.Unlock()
    
    // Cleanup expired entries periodically
    go func() {
        time.Sleep(1 * time.Hour)
        d.cleanupExpired()
    }()
}

func (d *WebhookDeduplicator) cleanupExpired() {
    d.mu.Lock()
    defer d.mu.Unlock()
    
    now := time.Now()
    for id, receivedAt := range d.seen {
        if now.Sub(receivedAt) > d.maxAge {
            delete(d.seen, id)
        }
    }
}
```

### 5.3 Caller-Side Deduplication (Idempotency Keys)

Callers can include an `idempotency_key` header to ensure at-most-once delivery:

```http
POST /gateway/webhook HTTP/1.1
X-OpenNavi-Secret: <hmac_signature>
X-Idempotency-Key: unique-request-id-from-caller
Content-Type: application/json
```

If the same `X-Idempotency-Key` is sent twice within a short window, the gateway returns the cached response from the first processing instead of re-processing.

### Requirement: Webhook ID Generation

The system SHALL generate a unique webhook ID for every incoming webhook using the format `wh_<ISO8601_timestamp>_<8-char-random>`.

#### Scenario: Webhook ID is unique and parseable

- GIVEN the gateway receives a webhook request
- WHEN the gateway generates a webhook ID
- THEN the ID SHALL use the `wh_` prefix, include the ISO 8601 timestamp of reception, and append an 8-character random suffix
- AND the ID SHALL be unique across all webhooks processed by the instance

### Requirement: Server-Side Deduplication

The system SHALL detect and skip duplicate webhook requests by tracking seen webhook IDs with a configurable maximum age (default 24 hours).

#### Scenario: Duplicate webhook ID is not reprocessed

- GIVEN the gateway receives a webhook request with a webhook ID that was seen within the dedup window
- WHEN the gateway checks for duplicates
- THEN the gateway SHALL return the cached response from the original processing
- AND the system SHALL NOT re-process the webhook or create a new session

### Requirement: Idempotency Key Support

The system SHALL support an `X-Idempotency-Key` header that allows callers to retry webhook requests safely by returning the cached response from the first processing of that key.

#### Scenario: Idempotency key returns cached response

- GIVEN the gateway receives a webhook with an `X-Idempotency-Key` header
- WHEN the same `X-Idempotency-Key` is sent again within the dedup window
- THEN the gateway SHALL return the cached response from the first processing
- AND the system SHALL NOT process the webhook again

---

## 05 Content Negotiation

### 6.1 Supported Formats

| Accept Header | Format | When to Use |
|---------------|--------|-------------|
| `application/json` (default) | JSON with text + markdown fields | Most callers, programmatic consumption |
| `text/plain` | Plain text only (no markdown formatting) | Simple display systems, SMS gateways |
| `application/vnd.opennavi.markdown+json` | Markdown-formatted response in JSON | Systems that render markdown natively |

### 6.2 Content-Type Response Header

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-API-Version: v1
Sunset: Sat, 01 Jan 2027 00:00:00 GMT  // if deprecated
Deprecation: true                       // if deprecated

{...}
```

### 6.3 Format Selection Logic

```go
func selectResponseFormat(acceptHeader string) ResponseFormat {
    switch {
    case strings.Contains(acceptHeader, "application/vnd.opennavi.markdown+json"):
        return ResponseFormatMarkdown
    
    case strings.Contains(acceptHeader, "text/plain"):
        return ResponseFormatPlain
    
    default:
        return ResponseFormatJSON // Default to full JSON with text + markdown
    }
}

type ResponseFormat int
const (
    ResponseFormatJSON ResponseFormat = iota
    ResponseFormatPlain
    ResponseFormatMarkdown
)

// Format response according to selected format
func formatResponse(resp AgentResponse, format ResponseFormat) []byte {
    switch format {
    case ResponseFormatPlain:
        return []byte(resp.Text) // Plain text only
    
    case ResponseFormatMarkdown:
        data, _ := json.Marshal(map[string]any{
            "text":     resp.Markdown,
            "markdown": resp.Markdown,
        })
        return data
    
    default: // Full JSON with all fields
        data, _ := json.Marshal(resp)
        return data
    }
}
```

### Requirement: Content Negotiation via Accept Header

The system SHALL select the response format based on the HTTP `Accept` header, supporting `application/json` (default), `text/plain`, and `application/vnd.opennavi.markdown+json`.

#### Scenario: Accept header selects plain text

- GIVEN an external system sends a webhook with `Accept: text/plain`
- WHEN the gateway generates the response
- THEN the response body SHALL contain only plain text with no markdown formatting
- AND the `Content-Type` header SHALL match the selected format

#### Scenario: Default to JSON when no Accept header

- GIVEN an external system sends a webhook without an `Accept` header
- WHEN the gateway generates the response
- THEN the response body SHALL use the default JSON format with both `text` and `markdown` fields

### Requirement: API Version Header in Responses

The system SHALL include an `X-API-Version` header in every webhook response, and optionally include `Sunset` and `Deprecation` headers for deprecated API versions.

#### Scenario: API version advertised in response

- GIVEN a webhook request is processed successfully
- WHEN the gateway returns the response
- THEN the response SHALL include the `X-API-Version: v1` header
- AND if the version is deprecated, the response SHALL also include `Sunset` and `Deprecation` headers

---

## 06 HMAC Signature Verification

### 7.1 Signature Algorithm

```go
// Verify webhook signature using HMAC-SHA256
func verifyWebhookSignature(body []byte, secret string, providedSig string) bool {
    hmac := hmac.New(sha256.New, []byte(secret))
    hmac.Write(body)
    
    expectedMAC := hex.EncodeToString(hmac.Sum(nil))
    
    // Constant-time comparison to prevent timing attacks
    return subtle.ConstantTimeCompare([]byte(expectedMAC), []byte(providedSig)) == 1
}

// Generate signature for test/debugging
func generateWebhookSignature(body []byte, secret string) string {
    hmac := hmac.New(sha256.New, []byte(secret))
    hmac.Write(body)
    return hex.EncodeToString(hmac.Sum(nil))
}
```

### 7.2 Signature Header Format

```http
X-OpenNavi-Signature: sha256=<hex_encoded_hmac>
X-OpenNavi-Timestamp: 1722584400
X-OpenNavi-Nonce: abc123def456
```

- `X-OpenNavi-Signature`: HMAC-SHA256 of the request body using the configured secret
- `X-OpenNavi-Timestamp`: Unix timestamp when the signature was generated (for replay attack prevention)
- `X-OpenNavi-Nonce`: Random string to prevent replay attacks (must be unique per request)

### 7.3 Replay Attack Prevention

```go
func verifyTimestamp(timestampStr string, maxAge time.Duration) error {
    timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
    if err != nil {
        return fmt.Errorf("invalid timestamp format")
    }
    
    receivedAt := time.Unix(timestamp, 0)
    if time.Since(receivedAt) > maxAge {
        return fmt.Errorf("timestamp too old: request is %v older than allowed %v", 
            time.Since(receivedAt), maxAge)
    }
    
    return nil
}
```

### Requirement: HMAC-SHA256 Signature Verification

The system SHALL verify every webhook request by computing an HMAC-SHA256 signature of the request body using the configured secret and comparing it with the `X-OpenNavi-Signature` header using constant-time comparison.

#### Scenario: Valid signature allows processing

- GIVEN the gateway receives a webhook with a valid `X-OpenNavi-Signature` header
- WHEN the gateway verifies the HMAC-SHA256 signature
- THEN the system SHALL allow the request to proceed
- AND the comparison SHALL use constant-time operations to prevent timing attacks

#### Scenario: Invalid signature rejects request

- GIVEN the gateway receives a webhook with an invalid or missing `X-OpenNavi-Signature` header
- WHEN the gateway verifies the signature
- THEN the gateway SHALL return HTTP `401 Unauthorized`
- AND the system SHALL NOT process the request

### Requirement: Replay Attack Prevention

The system SHALL verify the `X-OpenNavi-Timestamp` header and reject requests whose timestamp exceeds a configurable maximum age (default 5 minutes) to prevent replay attacks.

#### Scenario: Stale timestamp rejected

- GIVEN the gateway receives a webhook with an `X-OpenNavi-Timestamp` header
- WHEN the timestamp is older than the configured maximum age
- THEN the gateway SHALL reject the request with a `401 Unauthorized` error
- AND the error details SHALL indicate the timestamp is too old

### Requirement: Nonce Uniqueness Enforcement

The system SHALL reject duplicate `X-OpenNavi-Nonce` values within the dedup window to prevent replay attacks.

#### Scenario: Duplicate nonce rejected

- GIVEN the gateway receives a webhook with an `X-OpenNavi-Nonce` header
- WHEN the same nonce was already seen within the dedup window
- THEN the gateway SHALL reject the request as a duplicate
- AND the system SHALL NOT process the request

---

## 07 Implementation Order

1. **Webhook receiver** — HTTP handler with HMAC signature verification and timestamp validation
2. **Response schemas** — Define JSON structures for ok/accepted/error/timeout responses
3. **Sync vs async routing** — Determine response pattern based on webhook config or caller preference
4. **Deduplication** — Webhook ID tracking + idempotency key support
5. **Content negotiation** — Accept header parsing and format selection
6. **Error handling** — Standardized error responses with retryable flags

---

## 08 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Three webhook patterns (sync/async/fire-and-forget)** — Mirrors CI/CD best practices from GitHub Actions and Jenkins | Callers choose based on whether they need immediate feedback or can wait for platform delivery |
| **Webhook ID format (`wh_` prefix + timestamp + random)** — Machine-parseable, unique, contains metadata | Enables easy filtering in logs and deduplication without storing extra state |
| **HMAC-SHA256 with constant-time comparison** — Industry standard for webhook auth | Prevents timing attacks while being simple to implement correctly |
| **Idempotency key support** — Allows callers to safely retry failed webhooks | Critical for production CI/CD systems where network failures are common |
| **Content negotiation via Accept header** — Standard HTTP mechanism, no custom headers needed | Callers can request plain text for SMS or markdown for rich display without changing their payload format |


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S030000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |