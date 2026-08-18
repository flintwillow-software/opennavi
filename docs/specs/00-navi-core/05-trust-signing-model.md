---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000500, 000501, 000502, 000503, 000504, 000505, 000506, 000507]
---

# Trust & Signing Model

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Overview

The trust model for navis is **signature-based verification before execution**. Users verify the navi binary's signature matches the developer's key before running it. Extensions are separate binaries with their own signatures, verified before loading at runtime.

> **See also:** [P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md) for the P2P key trust model that extends the binary-level trust to message-level trust. While this spec covers verifying the navi binary itself, the P2P spec covers verifying individual messages between navis using SSH keypairs — the same keypair can also sign git commits, creating a unified trust chain from binary → message → code.

This creates a transparent trust chain:
```
Developer signs binary → User verifies signature → User runs binary
                                           ↕ (extensions)
                                    Extension author signs → User verifies → Loaded in sandbox
```

No Node.js, no npm, no unsigned JS packages. Just signed compiled binaries with verifiable provenance.

### Requirement: Signature-Based Verification
The system SHALL require users to verify a navi binary's signature matches the developer's key before execution.

#### Scenario: Verifying a navi binary
- **GIVEN** a user has a navi binary and the developer's public key
- **WHEN** the user runs the verification tool
- **THEN** the system SHALL confirm the signature matches the developer's key
- **AND** the user SHALL be able to run the binary only after successful verification

### Requirement: Extension Signature Verification
The system SHALL verify extensions as separate binaries with their own signatures, verified before loading at runtime.

#### Scenario: Loading an extension
- **GIVEN** a navi discovers an extension binary
- **WHEN** the navi attempts to load the extension
- **THEN** the system SHALL verify the extension's signature
- **AND** the system SHALL only load the extension if the signature is valid

---

## 01 Code Signing by Platform

Each operating system has its own code signing mechanism. The opennavi builder supports all three:

### macOS: `codesign`

macOS uses the built-in `codesign` tool for binary signatures. Developers sign with their Apple Developer ID or a self-signed certificate.

**Signing:**
```bash
# Sign with developer identity (prompted by system)
codesign --sign "Developer Identity" oncall-bot

# Sign with specific certificate
codesign --sign "My Team: dev@example.com" oncall-bot

# Verify signature
codesign --verify oncall-bot
```

**User verification:**
```bash
# Check signature before running
codesign --verify --verbose oncall-bot

# Run after verification
./oncall-bot
```

**Notes:**
- macOS may prompt for permission when first running an unsigned binary (Gatekeeper)
- Signed binaries bypass Gatekeeper prompts
- Self-signed certs work but show "unidentified developer" warning unless notarized by Apple

### Linux: GPG Signatures

Linux doesn't have a built-in code signing mechanism for binaries. The recommended approach is **GPG detached signatures** with a public key registry.

**Signing:**
```bash
# Generate GPG key (if you don't have one)
gpg --full-generate-key

# Detach-sign the binary
gpg --detach-sign --armor oncall-bot
# Produces: oncall-bot.asc (ASCII-armored signature file)

# Verify signature
gpg --verify oncall-bot.asc oncall-bot
```

**User verification:**
```bash
# Import developer's public key (from keyserver or direct transfer)
gpg --keyserver keys.openpgp.org --recv-keys ABCDEF1234567890
# or: gpg --import developer-public-key.asc

# Verify signature before running
gpg --verify oncall-bot.asc oncall-bot && ./oncall-bot
```

**Notes:**
- Users must manually import and trust the developer's public key
- Keyserver distribution is optional but recommended for discoverability
- No built-in OS-level enforcement — verification is user responsibility

### Windows: signtool / Authenticode

Windows uses Microsoft's Authenticode signing via `signtool`. Developers need a code-signing certificate from a trusted CA (e.g., DigiCert, Sectigo).

**Signing:**
```bash
# Sign with certificate (prompted for password)
signtool sign /f mycert.pfx /p password /t http://timestamp.digicert.com oncall-bot.exe

# Verify signature
signtool verify /pa oncall-bot.exe
```

**User verification:**
```cmd
:: Check signature before running (Windows prompts if unsigned)
signtool verify /pa oncall-bot.exe

:: Run after verification
oncall-bot.exe
```

**Notes:**
- Windows SmartScreen may flag unsigned executables as "unknown publisher"
- Signed binaries get "verified publisher" status, reducing user warnings
- Self-signed certs work but show security prompts

### Requirement: macOS Code Signing
The system SHALL support signing navi binaries on macOS using `codesign` with Apple Developer IDs or self-signed certificates.

#### Scenario: Signing on macOS
- **GIVEN** a developer has a macOS system with a code signing identity
- **WHEN** the developer runs `codesign --sign "Developer Identity" oncall-bot`
- **THEN** the binary SHALL be signed with the developer identity
- **AND** users SHALL verify the signature with `codesign --verify oncall-bot`

### Requirement: Linux GPG Signing
The system SHALL support signing navi binaries on Linux using GPG detached signatures.

#### Scenario: Signing on Linux
- **GIVEN** a developer has a GPG key
- **WHEN** the developer runs `gpg --detach-sign --armor oncall-bot`
- **THEN** the system SHALL produce an ASCII-armored detached signature file `oncall-bot.asc`
- **AND** users SHALL verify the signature with `gpg --verify oncall-bot.asc oncall-bot`

### Requirement: Windows Authenticode Signing
The system SHALL support signing navi binaries on Windows using `signtool` with Authenticode certificates.

#### Scenario: Signing on Windows
- **GIVEN** a developer has a code-signing certificate from a trusted CA
- **WHEN** the developer runs `signtool sign /f mycert.pfx oncall-bot.exe`
- **THEN** the binary SHALL be signed with the Authenticode certificate
- **AND** users SHALL verify the signature with `signtool verify /pa oncall-bot.exe`

---

## 02 Verification Flow

The verification flow is the same across platforms, just with different tools:

```
1. Developer compiles navi binary
         ↓
2. Developer signs binary (platform-specific tool)
         ↓
3. Developer distributes binary + signature file
         ↓
4. User downloads binary + signature
         ↓
5. User verifies signature matches developer's key
         ↓
6. If verified → user runs binary
   If not verified → user aborts, contacts developer
```

### Example: End-to-End Flow (Linux)

**Developer side:**
```bash
# Build the navi
opennavi-build -output ./dist/oncall-bot ./oncall-navi/

# Sign with GPG
gpg --detach-sign --armor ./dist/oncall-bot
mv oncall-bot.asc dist/

# Distribute (upload to website, release on GitHub, etc.)
```

**End user side:**
```bash
# Download binary and signature
curl -O https://example.com/oncall-bot
curl -O https://example.com/oncall-bot.asc

# Import developer's public key (one-time setup)
gpg --keyserver keys.openpgp.org --recv-keys ABCDEF1234567890

# Verify signature
gpg --verify oncall-bot.asc oncall-bot && echo "Signature valid" || echo "SIGNATURE INVALID - DO NOT RUN"

# Run after verification
chmod +x oncall-bot && ./oncall-bot
```

### Requirement: Cross-Platform Verification Flow
The system SHALL support the same verification flow across all platforms: developer compiles, signs, distributes, user downloads, verifies, and runs.

#### Scenario: End-to-end verification on Linux
- **GIVEN** a developer has built and GPG-signed a navi binary
- **WHEN** a user downloads the binary and signature file
- **THEN** the user SHALL import the developer's public key and verify the signature
- **AND** the user SHALL run the binary only if the signature is valid

### Requirement: Verification Outcome
The system SHALL provide a clear pass/fail outcome for signature verification, with instructions to abort and contact the developer on failure.

#### Scenario: Signature verification fails
- **GIVEN** a user runs verification on a tampered binary
- **WHEN** the signature does not match
- **THEN** the system SHALL report "SIGNATURE INVALID - DO NOT RUN"
- **AND** the user SHALL be instructed to contact the developer

---

## 03 Extension Trust Model

Extensions (standalone plugins) have their own trust model, separate from the navi binary:

### Extension Signing

Extension authors sign their standplugin binaries using the same platform-specific tools. The navi developer specifies which extension signers are allowed in the navi definition:

```go
extensions: ExtensionConfig{
    AllowStandalonePlugins: true,
    
    // Extensions must be signed by one of these keys
    AllowedSigners: []string{
        "github.com/oncall-bot/monitoring-ext",  // GitHub repo (for GPG)
        "ABCDEF1234567890",                      // GPG key ID
        "CN=OnCall Bot Team, O=Example Inc",     // Certificate subject
    },
}
```

### Extension Verification at Runtime

When the navi loads an extension:

```go
// In opennavi SDK (compiled into binary)
func loadExtension(path string) error {
    sigPath := path + ".asc"  // or .sig for Windows
    
    // Verify signature matches allowed signers
    signer, err := verifySignature(sigPath, path, allowedSigners)
    if err != nil {
        return fmt.Errorf("extension %s: signature verification failed: %w", path, err)
    }
    
    // Check if signer is in allowed list
    if !isAllowedSigner(signer, allowedSigners) {
        return fmt.Errorf("extension %s: signer not in allowed list", path)
    }
    
    // Load extension (sandboxed process)
    return loadSandboxed(path)
}
```

**Key points:**
- Extensions are verified **before loading**, not just before running
- If signature is invalid or signer not in allowlist → extension rejected, navi continues without it
- Extensions run in sandboxed processes — can't access main binary's memory space

### Requirement: Extension Signer Allowlist
The system SHALL allow the navi developer to specify which extension signers are allowed, and SHALL reject extensions not signed by an allowed signer.

#### Scenario: Loading an allowed extension
- **GIVEN** a navi definition specifies an allowed signers list
- **WHEN** the navi discovers an extension signed by an allowed signer
- **THEN** the system SHALL verify the signature and load the extension

#### Scenario: Loading a disallowed extension
- **GIVEN** a navi definition specifies an allowed signers list
- **WHEN** the navi discovers an extension signed by a signer not in the list
- **THEN** the system SHALL reject the extension
- **AND** the navi SHALL continue without the extension

### Requirement: Pre-Loading Extension Verification
The system SHALL verify extension signatures before loading, not before running, and reject extensions with invalid signatures or disallowed signers.

#### Scenario: Extension with invalid signature
- **GIVEN** an extension binary has an invalid signature
- **WHEN** the navi attempts to load the extension
- **THEN** the system SHALL reject the extension with an error
- **AND** the navi SHALL continue without the extension

---

## 04 Signature Storage and Management

### Developer Keys

Developers manage their signing keys separately from the opennavi SDK:

**macOS:** Use Keychain Access or `security` CLI to manage certificates  
**Linux:** Use GPG keyring (`~/.gnupg/`)  
**Windows:** Use Certificate Manager or `certlm.msc`

The opennavi builder doesn't manage keys — it just invokes the platform's signing tools with the developer's existing credentials.

### Public Key Distribution

Developers distribute their public keys through:
- **Keyserver** (Linux GPG): `gpg --keyserver keys.openpgp.org --send-keys YOUR_KEY_ID`  
- **Website**: Host public key on project website for download  
- **README/RELEASES**: Include public key fingerprint in documentation  

Users import these keys manually (one-time setup) before verifying binaries.

### Requirement: Out-of-Band Key Management
The system SHALL require developers to manage their signing keys outside the opennavi SDK, using platform-specific tools such as Keychain Access, GPG keyring, or Certificate Manager.

#### Scenario: Managing signing keys
- **GIVEN** a developer needs to sign a navi binary
- **WHEN** the developer sets up signing
- **THEN** the developer SHALL manage signing keys using platform-specific tools
- **AND** the opennavi builder SHALL invoke the platform's signing tools with existing credentials

### Requirement: Public Key Distribution
The system SHALL support developers distributing public keys through keyservers, websites, and project documentation.

#### Scenario: Distributing a public key
- **GIVEN** a developer has a signing key
- **WHEN** the developer publishes the public key
- **THEN** the developer SHALL be able to distribute it via a keyserver, project website, or README
- **AND** users SHALL import the public key manually before verifying binaries

---

## 05 Security Considerations

### What the Trust Model Protects Against

| Threat | Protected? | How |
|--------|-----------|-----|
| Tampered binary (downloaded file modified) | ✅ | Signature verification catches any change |
| Unsigned JS/Python code execution | ✅ | No runtime loading of unsigned scripts |
| Dependency confusion attacks | ✅ | All dependencies compiled in, no runtime downloads |
| Supply chain attacks on extensions | ✅ | Extension signatures verified before loading |

### What the Trust Model Does NOT Protect Against

| Threat | Protected? | Notes |
|--------|-----------|-------|
| Malicious developer (signed their own key) | ❌ | User must trust the developer's identity |
| Stolen signing key | ❌ | Same as any compromised key — revoke and re-sign |
| Zero-day vulnerabilities in compiled code | ❌ | Signature doesn't prevent bugs, only tampering |

### Best Practices for Developers

1. **Use a dedicated signing key** — don't use your personal GPG cert or Apple Developer ID for navis
2. **Publish public keys publicly** — on project website, README, keyserver
3. **Sign every release** — never distribute unsigned binaries in production
4. **Document verification steps** — tell users how to verify before running

### Best Practices for End Users

1. **Verify signatures before running** — don't skip this step
2. **Import public keys from trusted sources** — project website, not random GitHub repos
3. **Report invalid signatures immediately** — contact the developer, don't run the binary
4. **Keep signing keys secure** — if compromised, revoke and re-sign

### Requirement: Tamper Detection
The system SHALL detect any modification to the navi binary after it was signed, through signature verification.

#### Scenario: Verifying a tampered binary
- **GIVEN** a navi binary has been tampered with after signing
- **WHEN** the user runs signature verification
- **THEN** the system SHALL report that the signature is invalid
- **AND** the user SHALL be warned not to run the binary

### Requirement: No Unsigned Code in Main Process
The system SHALL prevent unsigned code from executing in the main process, protecting against dependency confusion and supply chain attacks.

#### Scenario: Attempting to load unsigned code
- **GIVEN** a navi is running
- **WHEN** an attempt is made to load unsigned code into the main process
- **THEN** the system SHALL block the execution
- **AND** the system SHALL log the blocked attempt

---

## 06 Builder Integration

The opennavi builder supports signing via command-line flags:

```bash
# Sign with default identity (platform-specific)
opennavi-build --sign ./oncall-navi/

# Sign with specific identity/key
opennavi-build --sign "My Developer ID" ./oncall-navi/  # macOS
opennavi-build --sign ABCDEF1234567890 ./oncall-navi/    # Linux (GPG key ID)
opennavi-build --sign mycert.pfx ./oncall-navi.exe       # Windows

# Sign and verify in one step
opennavi-build --sign --verify ./oncall-navi/
```

When `--sign` is passed, the builder:
1. Runs the platform-specific signing tool after compilation
2. Outputs the signature file alongside the binary
3. Reports any signing errors (e.g., missing key, permission denied)

### Requirement: Builder Signing Integration
The system SHALL integrate signing into the build process via `--sign`, running the platform-specific signing tool after compilation.

#### Scenario: Signing during build
- **GIVEN** a developer runs `opennavi-build --sign ./oncall-navi/`
- **WHEN** the builder completes compilation
- **THEN** the builder SHALL run the platform-specific signing tool
- **AND** the builder SHALL output the signature file alongside the binary
- **AND** the builder SHALL report any signing errors

### Requirement: Builder Verify Flag
The system SHALL support a `--verify` flag that runs verification checks after compilation.

#### Scenario: Verifying during build
- **GIVEN** a developer runs `opennavi-build --sign --verify ./oncall-navi/`
- **WHEN** the builder completes signing
- **THEN** the builder SHALL run verification checks on the signed binary

---

## 07 Next Steps

- **[01-navi-definition.md](./01-navi-definition.md)** — How extensions fit into the navi definition
- **[02-builder-compiler.md](./02-builder-compiler.md)** — How signing integrates with the build process
- **[05-extension-contract.md](./05-extension-contract.md)** — Extension protocol and runtime loading


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000500 | 0.1.0 | Migrated from docs/spec/ to spec-tree |