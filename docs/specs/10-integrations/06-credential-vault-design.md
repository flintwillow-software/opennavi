# Credential/Secret Vault — Design Reference

> Design details for the credential resolution system, extracted from the original gap analysis. Implements a resolution chain with local file encryption, environment variables, and HashiCorp Vault.

## Resolution Chain

Credentials are resolved at runtime from a prioritized chain:

```go
func resolveCredential(name string) (string, error) {
    // 1. Try environment variable first (highest priority, good for CI)
    if val := os.Getenv(name); val != "" {
        return val, nil
    }

    // 2. Try HashiCorp Vault (if configured)
    if vaultAddr := os.Getenv("VAULT_ADDR"); vaultAddr != "" {
        client, err := api.NewClient(&api.Config{
            Address: vaultAddr,
        })
        if err == nil {
            secret, err := client.Logical().Read("secret/data/" + name)
            if err == nil && secret != nil {
                if data, ok := secret.Data["data"].(map[string]interface{}); ok {
                    if val, ok := data[name]; ok {
                        return fmt.Sprint(val), nil
                    }
                }
            }
        }
    }

    // 3. Try local encrypted file (~/.config/navi/vault/...)
    path := filepath.Join(configDir, "vault", name+".enc")
    if data, err := os.ReadFile(path); err == nil {
        decrypted, err := decrypt(data, masterKey)
        if err == nil {
            return string(decrypted), nil
        }
    }

    return "", fmt.Errorf("credential %s not found in any source", name)
}
```

## Local File Encryption

- Each credential stored as a separate encrypted file (`~/.config/navi/vault/CRED_NAME.enc`)
- AES-256-GCM encryption with a master key from environment variable
- Master key via `NAVI_VAULT_KEY` env var or system keychain
- CLI: `opennavi vault set`, `opennavi vault get`, `opennavi vault list`

## HashiCorp Vault Integration

- Support KV v2 secrets engine
- Auth methods: token, AppRole, Kubernetes
- Auto-refresh for dynamic secrets (database credentials, AWS IAM)
- Configured via standard Vault environment variables

## Future Additions (not yet spec'd)

- macOS Keychain integration
- systemd credential support
- 1Password CLI integration
- AWS Secrets Manager / GCP Secret Manager

## Related

- [Credential Resolver Spec](../10-integrations/04-credential-resolver.md) — the formal spec this design detail feeds into