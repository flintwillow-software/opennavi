---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - integrations
priority: high
spec-refs: [100400, 100401, 100402, 100403]
---

# 04 Credential Resolver

> Priority-ordered credential resolution chain: environment variables, local encrypted files, and HashiCorp Vault. Supports fallback, caching, and per-integration source overrides.

## 00 Environment Variable Resolution

The credential resolver reads credentials from environment variables. This is the highest-priority source and is ideal for CI/CD pipelines, Docker/Kubernetes secrets, and development environments.

### Requirement:

- The system SHALL resolve credentials from environment variables (FR-5.1).

### Scenario:

- GIVEN an environment variable STRIPE_API_KEY is set to "sk_test_xxx", WHEN the resolver resolves "STRIPE_API_KEY", THEN it returns the env value "sk_test_xxx" (TC-7).
- GIVEN an environment variable is set and the same credential exists in Vault and local files, WHEN the resolver resolves, THEN the env var value is returned (highest priority).

### Design Notes

Environment variables are the simplest and most portable credential source. They are resolved via `os.Getenv(name)`. Use cases include:
- CI/CD pipelines (GitHub Actions secrets)
- Docker/Kubernetes secrets
- systemd service environment directives
- Development (dotenv files)

---

## 01 Local Encrypted File Resolution

Credentials can be stored as AES-256-GCM encrypted files on disk, one per credential, under `~/.config/navi/vault/`. The master key is sourced from the `NAVI_VAULT_KEY` environment variable or the system keychain.

### Requirement:

- The system SHALL resolve credentials from local encrypted files (FR-5.2).

### Scenario:

- GIVEN a valid encrypted file at `~/.config/navi/vault/STRIPE_API_KEY.enc`, WHEN the resolver resolves "STRIPE_API_KEY", THEN it returns the decrypted value.
- GIVEN a corrupted encrypted file, WHEN the resolver attempts to decrypt it, THEN it returns an error "decryption failed" and falls through to the next source.

### Design Notes

```
Storage layout: ~/.config/navi/vault/<NAME>.enc
Encryption:     AES-256-GCM (authenticated encryption)
File format:    base64(nonce || ciphertext || auth tag)
Master key:     NAVI_VAULT_KEY env var or system keychain
```

The vault CLI provides management commands:
```bash
opennavi vault set STRIPE_API_KEY sk_live_...   # Encrypt and store
opennavi vault get STRIPE_API_KEY               # Decrypt and print (stdout only)
opennavi vault list                              # List stored credential names
opennavi vault rm STRIPE_API_KEY                 # Delete a credential
opennavi vault rotate master-key                 # Re-encrypt all with new master key
```

---

## 02 HashiCorp Vault Resolution

The resolver reads credentials from HashiCorp Vault KV v2 secrets engine, using standard Vault environment variables for configuration. Supports token, AppRole, Kubernetes, and userpass authentication methods.

### Requirement:

- The system SHALL resolve credentials from HashiCorp Vault (if configured) (FR-5.3, Should).

### Scenario:

- GIVEN VAULT_ADDR and VAULT_TOKEN are configured, WHEN the resolver resolves "STRIPE_API_KEY", THEN it reads the secret from `secret/data/STRIPE_API_KEY` and returns the value.
- GIVEN VAULT_ADDR is configured but Vault is unreachable, WHEN the resolver attempts to resolve from Vault, THEN it returns an error and falls through to the file source.

### Design Notes

```go
func vaultRead(name string) (string, error) {
    client, err := vault.NewClient(nil) // reads VAULT_ADDR/VAULT_TOKEN from env
    if err != nil {
        return "", err
    }
    secret, err := client.Logical().Read("secret/data/" + name)
    if err != nil || secret == nil {
        return "", fmt.Errorf("vault read failed")
    }
    data, ok := secret.Data["data"].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("unexpected vault response")
    }
    return fmt.Sprint(data[name]), nil
}
```

Vault configuration is via standard environment variables:
- `VAULT_ADDR` -- Vault server address
- `VAULT_TOKEN` -- Vault authentication token
- `VAULT_ROLE_ID` + `VAULT_SECRET_ID` -- AppRole authentication

Vault supports dynamic secrets for short-lived credentials (database passwords, cloud credentials) that expire automatically.

---

## 03 Priority-Ordered Resolution

The credential resolver checks sources in a configurable priority order. The default chain is: environment variables (highest priority), then HashiCorp Vault, then local encrypted files. The first source that returns a value wins.

### Requirement:

- The system SHALL resolve credentials in a configurable priority order (FR-5.4).

### Scenario:

- GIVEN env var and file both set for the same credential, WHEN the resolver resolves, THEN the env value is returned (highest priority) (TC-8).
- GIVEN env var unset, Vault configured with a value, and file exists with a different value, WHEN the resolver resolves, THEN the Vault value is returned (2nd priority).
- GIVEN no sources have the credential, WHEN the resolver resolves, THEN it returns an error "credential not found".

### Credential Resolution Test Matrix

| Test Case | Env Var | Vault | File | Expected Source | Expected Result |
|-----------|---------|-------|------|-----------------|-----------------|
| Env var only | set | not configured | not configured | EnvSource | Returns env value |
| Vault only | unset | configured, has value | not configured | VaultSource | Returns vault value |
| File only | unset | not configured | exists, valid | FileSource | Returns decrypted value |
| All sources, env wins | set | configured, has value | exists, valid | EnvSource | Returns env value (highest priority) |
| All sources, vault wins | unset | configured, has value | exists, valid | VaultSource | Returns vault value (2nd priority) |
| No sources | unset | unconfigured | not exists | -- | Returns error "credential not found" |
| File corrupted | unset | not configured | exists, corrupted | -- | Returns error "decryption failed" |
| Vault unavailable | unset | configured, unreachable | exists, valid | FileSource | Falls through to file |
| Credential cached | set | -- | -- | EnvSource | Second call returns cached value (no re-read) |

### Design Notes

```go
type CredentialResolver struct {
    Sources []CredentialSource
}

type CredentialSource interface {
    Name() string
    Resolve(ctx context.Context, name string) (string, error)
    Priority() int // lower number = higher priority
}
```

Built-in sources:
- **EnvSource**: `os.Getenv(name)` -- priority 100
- **VaultSource**: HashiCorp Vault (KV v2) -- priority 200
- **FileSource**: `~/.config/navi/vault/<name>.enc` (AES-256-GCM) -- priority 300

The default chain is `["env", "vault", "file"]`. Per-integration overrides allow specifying a different order, e.g., `["vault", "env"]` for integrations that require Vault-only resolution.

### User Stories

- **US-005**: As Carlos (IT Admin), I want my navi to manage users via AD/LDAP/Okta, so that I can automate user lifecycle. The credential resolver supports the secure credential storage needed for identity provider API keys.
- **US-008**: As Dana (Support Manager), I want my navi to send SMS alerts via Twilio, so that critical issues reach the right person. Twilio credentials are resolved securely from the chain.

---

## Implementation Notes

### Phase 1 Tasks

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Implement `CredentialResolver` with env, file, Vault sources | P1 | M | Adapter interface |

### Testing

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Write unit tests for credential resolver chain covering all 9 test matrix cases | P1 | S | Credential resolver |

### Integration Points

The credential resolver sources use the vault implementation from the Operations module. Per-integration resolution order overrides allow an integration to specify `env`, `vault`, or `file` as its preferred credential source.

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from product-spec FR-5 Credential Resolver |