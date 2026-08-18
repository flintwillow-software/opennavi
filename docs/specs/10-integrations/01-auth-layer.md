---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100100, 100101, 100102, 100103, 100104]
---

# 01 Auth Layer

> Multi-provider authentication layer supporting OAuth 2.0 (authorization code, client credentials, device code), API key, HTTP Basic, mutual TLS, and token refresh.

## 00 OAuth 2.0

The auth layer supports three OAuth 2.0 authorization flows: authorization code, client credentials, and device code. Each flow is configured via the `AuthConfig` struct with the appropriate `OAuth2Config`.

### Requirement:

- The system SHALL support OAuth 2.0 (authorization code, client credentials, device code flows) (FR-2.1).

### Scenario:

- GIVEN an OAuth2 config with authorization code flow, WHEN `Apply` is called on an outgoing request, THEN the Authorization header contains a Bearer token obtained via the authorization code flow (TC-2).
- GIVEN an OAuth2 config with client credentials flow, WHEN `Apply` is called, THEN the client authenticates with its client ID and secret and attaches the resulting Bearer token.
- GIVEN an OAuth2 config with device code flow, WHEN `Apply` is called, THEN the device code flow is used to obtain a token and the Bearer token is attached.

### User Stories

- **US-002**: As Ava (Finance Lead), I want my navi to create invoices in QuickBooks/Xero, so that billing is automated. QuickBooks and Xero both use OAuth 2.0 for API authentication.
- **US-009**: As Elena (Creative Director), I want my navi to track asset versions in ShotGrid/Frame.io, so that the pipeline is up to date. These services use OAuth 2.0.

### Design Notes

```go
type Authenticator interface {
    Apply(ctx context.Context, req *http.Request) error
    Refresh(ctx context.Context) error
    Type() string
}

type OAuth2Config struct {
    ClientID     string   `yaml:"client_id" json:"client_id"`
    ClientSecret string   `yaml:"client_secret" json:"client_secret"`
    TokenURL     string   `yaml:"token_url" json:"token_url"`
    Scopes       []string `yaml:"scopes" json:"scopes"`
    AuthFlow     string   `yaml:"auth_flow" json:"auth_flow"` // authorization_code | client_credentials | device_code
}
```

The `Authenticator` interface is the contract for all auth types. `Apply` adds auth headers/params to an outgoing request, `Refresh` renews the credential for token-based auth, and `Type` returns the auth type string.

---

## 01 API Key Authentication

API key authentication supports keys passed via HTTP headers or query parameters, with configurable header name and prefix.

### Requirement:

- The system SHALL support API key authentication (header or query param) (FR-2.2).

### Scenario:

- GIVEN an API key config with header "X-API-Key" and no prefix, WHEN `Apply` is called, THEN the request header contains "X-API-Key: <key_value>" (TC-3).
- GIVEN an API key config with header "Authorization" and prefix "Bearer", WHEN `Apply` is called, THEN the request header contains "Authorization: Bearer <key_value>".

### Design Notes

```go
type APIKeyConfig struct {
    Header string `yaml:"header" json:"header"` // default: "Authorization"
    Prefix string `yaml:"prefix" json:"prefix"` // e.g., "Bearer", "ApiKey"
}
```

The API key is resolved from the credential resolver chain at runtime, allowing it to be sourced from environment variables, Vault, or encrypted files.

---

## 02 HTTP Basic Authentication

HTTP Basic authentication supports configurable username and password field names, with credential values resolved from the credential resolver chain.

### Requirement:

- The system SHALL support HTTP Basic authentication (FR-2.3).

### Scenario:

- GIVEN a Basic auth config with username and password credentials, WHEN `Apply` is called, THEN the request contains a Basic Authorization header with the Base64-encoded credentials.

### Design Notes

```go
type BasicConfig struct {
    UsernameField string `yaml:"username_field" json:"username_field"` // credential key for username
    PasswordField string `yaml:"password_field" json:"password_field"` // credential key for password
}
```

The `UsernameField` and `PasswordField` specify which credential keys to resolve from the credential resolver chain. This allows the actual values to be stored securely rather than in plaintext configuration.

---

## 03 Mutual TLS (mTLS)

Mutual TLS authentication provides service-to-service auth where both the client and server present certificates. The client certificate, key, and optional CA certificate are resolved from the credential resolver chain.

### Requirement:

- The system SHALL support mutual TLS (mTLS) for service-to-service auth (FR-2.4, Should).

### Scenario:

- GIVEN an mTLS config with client cert and key credentials, WHEN the adapter makes a request, THEN the HTTP transport uses the client certificate for TLS handshake.

### Design Notes

```go
type MTLSConfig struct {
    CertField string `yaml:"cert_field" json:"cert_field"` // credential key for client cert
    KeyField  string `yaml:"key_field" json:"key_field"`   // credential key for client key
    CAField   string `yaml:"ca_field" json:"ca_field"`     // credential key for CA cert (optional)
}
```

mTLS is implemented at the HTTP transport layer rather than as a request-level header modification. The `HttpAdapter` constructs a custom `http.Transport` with the configured TLS certificates.

---

## 04 Token Refresh

OAuth 2.0 tokens are refreshed automatically when they expire or when a 401 response is received. The refresh mechanism uses the configured OAuth token URL and the stored refresh token (for authorization code flow) or re-authenticates (for client credentials flow).

### Requirement:

- The system SHALL support token refresh for OAuth 2.0 tokens (FR-2.5).

### Scenario:

- GIVEN an OAuth2 token that has expired, WHEN a request is made with a 401 response, THEN the system automatically refreshes the token and retries the request.
- GIVEN an OAuth2 token nearing expiry, WHEN `Refresh` is called, THEN a new token is obtained from the token URL and the stored token is updated.

### Design Notes

Token refresh for OAuth 2.0 follows this flow:
1. Request is made with the current Bearer token
2. If the response is 401 Unauthorized, the system checks if the token is expired
3. The system calls the token URL with the refresh token (authorization code flow) or client credentials
4. The new token is stored and the original request is retried with the new token
5. If refresh fails, the error is returned to the caller

---

## Implementation Notes

### Phase 2: OAuth Flow + Webhook Listener

**Goal:** Add OAuth 2.0 authentication support and the webhook listener pattern.

**Exit criteria:** Adapters can use OAuth 2.0 (authorization code, client credentials, device code) and receive/normalize webhooks.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Implement `OAuth2Authenticator` (authorization code flow) | P1 | L | HttpAdapter |
| Implement `OAuth2ClientCredentials` flow | P1 | M | OAuth2Authenticator |
| Implement `OAuth2DeviceCode` flow | P2 | S | OAuth2Authenticator |
| Implement token refresh with automatic retry on 401 | P1 | M | OAuth2Authenticator |
| Implement `mTLSAuthenticator` | P2 | S | HttpAdapter |

### Phase 1 Tasks (completed in parallel)

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Implement `APIKeyAuthenticator`, `BasicAuthenticator` | P1 | M | HttpAdapter |

### Integration Tests

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Write integration tests for OAuth token refresh | P1 | S | OAuth2Authenticator |

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-2 Auth Layer |