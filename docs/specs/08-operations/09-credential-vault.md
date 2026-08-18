---
status: draft
created: '2026-08-11'
updated: '2026-08-11'
tags:
  - spec
  - operations
  - secrets
  - vault
priority: high
spec-refs: [080900, 080901, 080902, 080903, 080904, 080905, 080906]
---

# Credential & Secret Vault

> **Status:** Draft · **Priority:** High  
> **Scope:** Secure storage and resolution of credentials (API keys, OAuth tokens, service credentials) across three backends: local encrypted files, environment variables, and HashiCorp Vault.

---

## 00 Overview

Navis need credentials to access external systems — APIs, databases, messaging platforms, cloud services. This spec defines how credentials are stored, resolved, and rotated.

**Resolution chain (first match wins):**

```
1. Environment variable        (highest priority, ideal for CI/CD)
2. HashiCorp Vault             (if VAULT_ADDR is configured)
3. Local encrypted file        (~/.config/navi/vault/CRED_NAME.enc)
   └─ master key from NAVI_VAULT_KEY env var or system keychain
```

```go
func resolveCredential(name string) (string, error) {
    // 1. Environment variable
    if val := os.Getenv(name); val != "" {
        return val, nil
    }

    // 2. HashiCorp Vault
    if addr := os.Getenv("VAULT_ADDR"); addr != "" {
        if val, err := vaultRead(name); err == nil {
            return val, nil
        }
    }

    // 3. Local encrypted file
    path := filepath.Join(configDir, "vault", name+".enc")
    if data, err := os.ReadFile(path); err == nil {
        decrypted, err := decryptAESGCM(data, masterKey())
        if err == nil {
            return string(decrypted), nil
        }
    }

    return "", fmt.Errorf("credential %s not found in any source", name)
}
```

### Requirement: Resolution Chain Priority

The system SHALL resolve credentials using a first-match-wins chain: environment variables, then HashiCorp Vault, then local encrypted files, with each source acting as a fallback for the previous.

#### Scenario: Environment variable found first
- **GIVEN** a credential name is requested
- **WHEN** an environment variable with that name exists
- **THEN** the system SHALL return the environment variable value without checking Vault or local files

#### Scenario: Full chain fallback
- **GIVEN** a credential name is requested
- **WHEN** neither an environment variable nor a Vault path exists for that name
- **THEN** the system SHALL attempt to decrypt the local encrypted file and return the value

### Requirement: Credential Resolution Function

The system SHALL provide a `resolveCredential(name)` function that checks each source in priority order and returns the first value found, or an error if none resolve.

#### Scenario: No credential found
- **GIVEN** a credential name is requested
- **WHEN** no environment variable, Vault path, or local encrypted file exists for that name
- **THEN** the system SHALL return an error indicating the credential was not found in any source

---

## 01 Local File Encryption

### Storage Layout

```
~/.config/navi/vault/           # Vault directory
├── STRIPE_API_KEY.enc          # One file per credential
├── SENDGRID_API_KEY.enc
└── DB_PASSWORD.enc
```

### Encryption

- **Algorithm:** AES-256-GCM (authenticated encryption)
- **Master key:** from `NAVI_VAULT_KEY` env var, or system keychain (macOS Keychain, libsecret)
- **File format:** base64-encoded nonce + ciphertext + auth tag

```
[base64(nonce || ciphertext || tag)]
```

### CLI Commands

```bash
opennavi vault set STRIPE_API_KEY sk_live_...   # Encrypt and store
opennavi vault get STRIPE_API_KEY               # Decrypt and print (stdout only)
opennavi vault list                              # List stored credential names
opennavi vault rm STRIPE_API_KEY                 # Delete a credential
opennavi vault rotate master-key                 # Rotate the master key (re-encrypt all)
```

### Key Rotation

`opennavi vault rotate master-key` re-encrypts all stored credentials with a new master key. Old key is retained for a grace period to decrypt anything written under the old key.

### Requirement: Encrypted File Layout

The system SHALL store each credential as a separate AES-256-GCM encrypted file under `~/.config/navi/vault/` with the `.enc` extension, one file per credential.

#### Scenario: Storing a new credential
- **GIVEN** a credential name and value
- **WHEN** the user runs `opennavi vault set <name> <value>`
- **THEN** the system SHALL encrypt the value and write it to `~/.config/navi/vault/<name>.enc`

### Requirement: AES-256-GCM Encrypted Storage

The system SHALL encrypt credentials at rest using AES-256-GCM authenticated encryption, with the master key sourced from the `NAVI_VAULT_KEY` environment variable or the system keychain.

#### Scenario: Master key from environment
- **GIVEN** the `NAVI_VAULT_KEY` environment variable is set
- **WHEN** the system encrypts or decrypts a credential
- **THEN** the system SHALL use the value of `NAVI_VAULT_KEY` as the master key

#### Scenario: Master key from system keychain
- **GIVEN** the `NAVI_VAULT_KEY` environment variable is not set
- **WHEN** the system encrypts or decrypts a credential
- **THEN** the system SHALL attempt to retrieve the master key from the system keychain (macOS Keychain or libsecret)

### Requirement: Vault CLI Commands

The system SHALL provide `opennavi vault set`, `opennavi vault get`, `opennavi vault list`, and `opennavi vault rm` commands for managing local encrypted credentials.

#### Scenario: List stored credentials
- **GIVEN** the vault directory contains encrypted credential files
- **WHEN** the user runs `opennavi vault list`
- **THEN** the system SHALL display the names of all stored credentials without decrypting them

### Requirement: Master Key Rotation

The system SHALL support rotating the master key by re-encrypting all stored credentials under the new key, retaining the old key for a grace period to decrypt credentials written under the old key.

#### Scenario: Rotate master key
- **GIVEN** the vault contains encrypted credentials
- **WHEN** the user runs `opennavi vault rotate master-key`
- **THEN** the system SHALL decrypt each credential with the old key and re-encrypt it with the new key

---

## 02 Environment Variables

The simplest method — credentials passed via environment variables:

```bash
STRIPE_API_KEY=sk_live_... SENDGRID_API_KEY=SG... ./my-navi
```

**Use cases:**
- CI/CD pipelines (GitHub Actions secrets)
- Docker/Kubernetes secrets
- systemd service environment directives
- Development (dotenv files)

**Priority:** Environment variables take precedence over Vault and local files. This makes CI/CD deployments deterministic.

### Requirement: Environment Variable Resolution

The system SHALL resolve credentials from environment variables, with environment variables taking precedence over Vault and local file backends.

#### Scenario: Precedence over other sources
- **GIVEN** a credential name exists both as an environment variable and in Vault or local files
- **WHEN** the system resolves the credential
- **THEN** the system SHALL return the environment variable value

### Requirement: CI/CD and Deployment Support

The system SHALL support environment variables as the credential source for CI/CD pipelines, Docker and Kubernetes secrets, systemd service environment directives, and development dotenv files.

#### Scenario: Docker secrets via environment
- **GIVEN** a Docker container is configured with credential environment variables
- **WHEN** the navi process starts inside the container
- **THEN** the system SHALL resolve credentials from the container environment without additional configuration

---

## 03 HashiCorp Vault

### Supported Features

- **KV v2 secrets engine** (most common)
- **Auth methods:** token, AppRole, Kubernetes, userpass
- **Dynamic secrets** (short-lived credentials for databases, cloud IAM, etc.)

### Configuration

Vault is configured via standard environment variables:

```bash
VAULT_ADDR=https://vault.internal:8200
VAULT_TOKEN=hvs.xxx          # or VAULT_ROLE_ID + VAULT_SECRET_ID (AppRole)
```

### API Integration

```go
import (
    vault "github.com/hashicorp/vault/api"
)

func vaultRead(name string) (string, error) {
    client, err := vault.NewClient(nil) // reads VAULT_ADDR/VAULT_TOKEN from env
    if err != nil {
        return "", err
    }

    // KV v2: secrets are at secret/data/<name>
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

### Dynamic Secrets

For short-lived credentials (database passwords, cloud credentials):

```
1. Navi requests credential from Vault AppRole
2. Vault issues a time-limited credential (e.g., 1h DB password)
3. Navi uses the credential for the session
4. Credential expires automatically — no rotation management needed
```

### Requirement: KV v2 Secrets Engine Integration

The system SHALL read credentials from HashiCorp Vault KV v2 secrets engine, using the standard `VAULT_ADDR` and `VAULT_TOKEN` environment variables for configuration.

#### Scenario: Read credential from Vault
- **GIVEN** `VAULT_ADDR` and `VAULT_TOKEN` are configured
- **WHEN** the system resolves a credential from Vault
- **THEN** the system SHALL read the secret from `secret/data/<name>` and return the credential value

### Requirement: Multi-Method Vault Authentication

The system SHALL support token, AppRole, Kubernetes, and userpass authentication methods for HashiCorp Vault, configured via standard Vault environment variables.

#### Scenario: AppRole authentication
- **GIVEN** `VAULT_ROLE_ID` and `VAULT_SECRET_ID` are set instead of `VAULT_TOKEN`
- **WHEN** the system connects to Vault
- **THEN** the system SHALL authenticate using the AppRole method

### Requirement: Dynamic Credential Support

The system SHALL support short-lived dynamic credentials from Vault for databases and cloud IAM, with credential expiration handled automatically by Vault.

#### Scenario: Short-lived database password
- **GIVEN** Vault is configured with a database secrets engine
- **WHEN** the navi requests a database credential
- **THEN** the system SHALL receive a time-limited credential that expires automatically without manual rotation

### Requirement: Vault API Error Handling

The system SHALL return an error when a Vault read fails due to connectivity, authentication, or missing secrets, allowing the resolution chain to fall through to the next source.

#### Scenario: Vault unreachable
- **GIVEN** `VAULT_ADDR` is configured but Vault is unreachable
- **WHEN** the system attempts to resolve a credential from Vault
- **THEN** the system SHALL return an error and fall through to the local encrypted file source

---

## 04 Usage in Integrations

The vault is the credential source for all [integration adapters](../10-integrations/README.md) (or planned integrations). Each integration declares which credentials it needs:

```yaml
# Integration config: stripe.yaml
integration:
  name: "stripe"
  provider: "stripe"
  credentials:
    - name: "STRIPE_API_KEY"
      source: "env | vault | file"   # resolution order override
      required: true
    - name: "STRIPE_WEBHOOK_SECRET"
      source: "env | vault | file"
      required: true
```

### Requirement: Integration Credential Declaration

The system SHALL allow each integration adapter to declare required credentials in its configuration, including the credential name, resolution order override, and whether the credential is required.

#### Scenario: Required credential missing
- **GIVEN** an integration adapter declares a credential as required
- **WHEN** the system fails to resolve that credential from any source
- **THEN** the system SHALL report an error and prevent the integration from starting

### Requirement: Per-Integration Resolution Order

The system SHALL support per-integration resolution order overrides, allowing an integration to specify `env`, `vault`, or `file` as its preferred credential source.

#### Scenario: Override to Vault-only
- **GIVEN** an integration config specifies `source: "vault"` for a credential
- **WHEN** the system resolves that credential
- **THEN** the system SHALL skip environment variables and local files and read only from Vault

---

## 05 Key Design Decisions

1. **Resolution chain** — env → vault → file. First match wins. Each source is a fallback for the previous.
2. **Local-first** — navis work offline with the encrypted file store; Vault is an optional upgrade.
3. **Never stored in plaintext** — credentials are AES-256-GCM encrypted at rest or in a secure external system.
4. **Master key separation** — the master key is never stored alongside the encrypted files.
5. **Simple CLI** — `opennavi vault set/get/list/rm/rotate` for everyday management.
6. **Compile-time integration** — vault resolution is compiled into the navi binary (like tools), not a runtime plugin.

## 06 Gaps / Future Work

- [ ] macOS Keychain / Windows Credential Manager integration
- [ ] systemd credential support
- [ ] 1Password CLI integration
- [ ] AWS Secrets Manager / GCP Secret Manager
- [ ] Credential expiration and auto-rotation for long-lived secrets
- [ ] Audit logging of credential access (who/what/when)

## Related

- [GAPS.md](../../gaps/GAPS.md) Gap 11 — credential vault gap analysis
- [Trust & Signing Model](../00-navi-core/05-trust-signing-model.md) — code signing trust boundary
- [Deployment & Operations](./05-deployment-and-operations.md) — how credentials are provided at deploy time

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S080900 | 0.1.0 | Migrated from docs/spec/ to spec-tree |