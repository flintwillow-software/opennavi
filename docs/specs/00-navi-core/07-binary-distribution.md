---
status: draft
created: '2026-08-15'
updated: '2026-08-15'
tags:
  - spec
  - navi-core
priority: high
spec-refs: [000700, 000701, 000702, 000703, 000704, 000705]
---

# Binary Distribution

> How compiled navi binaries are distributed to users — Homebrew tap, direct download with signature verification, and update delivery. A navi is a single signed executable; distribution is the delivery path for that executable.

---

## 00 Distribution Channels

Navi binaries are distributed as compiled executables via:
1. **Homebrew tap** — `brew install opennavi/navi/<name>` for macOS/Linux
2. **Direct download** — signed binary + signature file from a release page or CDN
3. **Private channel** — internal distribution (artifact server, S3) for proprietary navis

### Requirement: Distribution Channels

The system SHALL support distributing navi binaries via Homebrew tap, direct download with signature files, and private/internal channels.

#### Scenario: Homebrew installation

- **GIVEN** a signed navi binary published to a Homebrew tap
- **WHEN** a user runs `brew install opennavi/navi/<name>`
- **THEN** the tap formula SHALL fetch the binary and signature for the pinned version

#### Scenario: Direct download

- **GIVEN** a signed binary and signature file published to a release endpoint
- **WHEN** a user downloads both
- **THEN** the user SHALL be able to verify the signature against the author's public key

## 01 Signing & Verification

Distribution pairs each binary with a signature file ([S000500 Trust & Signing](../00-navi-core/05-trust-signing-model.md)). The user verifies before first run.

```
Developer: signs binary with private key
  → Releases binary + signature
User: downloads binary + signature
  → Verifies: openssl dgst -verify pubkey.pem -signature binary.sig binary
  → Runs: ./navi
```

### Requirement: Signed Release Artifacts

The system SHALL publish a signature file with every released binary, and the user SHALL verify the signature before executing.

#### Scenario: Signature verification

- **GIVEN** a downloaded binary and its signature file
- **WHEN** the user verifies with the author's public key
- **THEN** the verification SHALL succeed only if the binary is unmodified and authored by the key holder

## 02 Platform Builds

Distribution requires per-platform builds (macOS, Linux, Windows), each signed for its platform ([S000500](../00-navi-core/05-trust-signing-model.md) — code signing by platform).

### Requirement: Platform Builds

The system SHALL produce platform-specific signed builds (macOS, Linux, Windows) for distribution.

#### Scenario: Cross-platform release

- **GIVEN** a navi release
- **WHEN** the release is published
- **THEN** builds for each supported platform SHALL be produced and signed

## 03 Updates

Update delivery follows the same trust model — the user verifies each update's signature before execution.

### Requirement: Signed Updates

The system SHALL have the navi verify the signature of updates before applying them, using the same trust chain as the initial install.

#### Scenario: Applying an update

- **GIVEN** a navi checks for updates and finds a new version
- **WHEN** the update is downloaded
- **THEN** the navi SHALL verify the update's signature against the author's key before replacing the binary

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-15 | S000700 | 0.2.0 | Initial binary distribution spec |