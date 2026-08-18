---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000300, 000301, 000302, 000303, 000304, 000305, 000306, 000307, 000308, 000309, 000310, 000311]
---

# Builder / Compiler

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Overview

The opennavi builder takes a navi definition (written in Go OR YAML) and compiles it into a single executable binary. It leverages Go's existing toolchain for compilation, linking, and optimization — no custom build system needed. The only new piece is **interface selection**, which determines which delivery modes get compiled into the binary.

### Requirement: Single Binary Output
The system SHALL compile a navi definition, written in Go or YAML, into a single executable binary using the Go toolchain.

#### Scenario: Building a navi
- **GIVEN** a navi definition written in Go or YAML
- **WHEN** the developer runs the builder
- **THEN** the builder SHALL produce a single executable binary
- **AND** the builder SHALL use Go's existing toolchain for compilation, linking, and optimization

### Requirement: Interface Selection in Compilation
The system SHALL determine which delivery modes are compiled into the binary based on the navi definition's interface selection.

#### Scenario: Compiling with a subset of interfaces
- **GIVEN** a navi definition enables a subset of interfaces
- **WHEN** the builder compiles the binary
- **THEN** only the selected delivery modes SHALL be compiled in

---

## 01 The Build Process

```go
// Option 1: Go code definition
cmd/oncall-bot/main.go:
sdk.Build(navi{...})

// Option 2: YAML definition
cmd/oncall-bot/naviconfig.yaml:
name: oncall-bot
version: 1.0.0
personality: "You are OnCall Bot..."
tools: [...]
skills: [...]
interfaces: [...]
```

Both formats produce the same compiled binary. The builder detects which format is present and processes accordingly.

The builder wraps Go's standard build process:
```bash
# Build with either Go code or YAML definition
cd my-navi/
opennavi-build -output ./dist/oncall-bot .
```

```bash
# Build the navi
opennavi-build my-project-agent/

# Produces: opennavi-my-project-agent (or just my-project-agent)

# With flags
opennavi-build -output ./dist/oncall-bot ./oncall-navi/
opennavi-build -ldflags="-X main.version=1.0.0" ./my-navi/
```

### Requirement: Build Format Detection
The system SHALL detect whether the navi project uses a Go code definition or a YAML definition and process it accordingly.

#### Scenario: Building from Go code
- **GIVEN** a navi project with a Go code definition
- **WHEN** the developer runs `opennavi-build`
- **THEN** the builder SHALL detect the Go format and compile the binary

#### Scenario: Building from YAML
- **GIVEN** a navi project with a `naviconfig.yaml`
- **WHEN** the developer runs `opennavi-build`
- **THEN** the builder SHALL detect the YAML format and compile the binary

### Requirement: Equivalent Output from Both Formats
The system SHALL produce the same compiled binary from a Go code definition and an equivalent YAML definition.

#### Scenario: Comparing build outputs
- **GIVEN** two navi projects with equivalent definitions, one in Go code and one in YAML
- **WHEN** both are built
- **THEN** the resulting binaries SHALL be functionally equivalent

---

## 02 Interface Selection Optimization

The key differentiator from a standard Go build is **interface selection**. The builder examines the navi definition's `interfaces` field and only compiles in the selected delivery modes:

### What Gets Compiled In Based on Interfaces

| Interface | Components Compiled In | Binary Size Impact |
|-----------|----------------------|-------------------|
| **CLI** | TUI framework, terminal I/O, chat view components | Medium (TUI is ~2MB) |
| **WebUI** | HTTP server, WebSocket hub, vanilla JS frontend, static file embedding | Small (~500KB for embedded assets) |
| **Gateway** | HTTP server, TLS/mTLS, webhook parser, message router | Medium (~1.5MB) |
| **Cron** | Scheduler, timer management | Tiny (~50KB) |
| **Telegram** | Telegram bot adapter, API client, message formatting | Small (~300KB) |
| **Discord** | Discord bot adapter, gateway WS client | Medium (~800KB) |
| **Slack** | Slack bot adapter, webhook handlers | Small (~400KB) |

### Interface Dependency Graph

Some interfaces have dependencies:

```
Gateway interface
  └── requires TLS (for HTTPS endpoints)
       └── pulls in crypto/tls from stdlib (small)

WebUI interface  
  └── requires HTTP server (from stdlib — already included)
  └── includes embedded static assets (HTML/CSS/JS)

CLI interface
  └── requires TUI framework (~2MB, custom component system)
```

The builder resolves these dependencies automatically. If you enable `Gateway`, it pulls in the TLS handling code even if not explicitly selected.

### Requirement: Selective Interface Compilation
The system SHALL examine the navi definition's `interfaces` field and compile in only the selected delivery modes and their components.

#### Scenario: Compiling CLI-only
- **GIVEN** a navi definition enables only the CLI interface
- **WHEN** the builder compiles the binary
- **THEN** the TUI framework, terminal I/O, and chat view components SHALL be compiled in
- **AND** no other interface components SHALL be included

#### Scenario: Compiling Gateway-only
- **GIVEN** a navi definition enables only the Gateway interface
- **WHEN** the builder compiles the binary
- **THEN** the HTTP server, TLS handling, webhook parser, and message router SHALL be compiled in

### Requirement: Automatic Interface Dependencies
The system SHALL automatically resolve interface dependencies, pulling in required components such as TLS handling for Gateway even when not explicitly selected.

#### Scenario: Enabling Gateway with HTTPS
- **GIVEN** a navi definition enables Gateway with HTTPS
- **WHEN** the builder compiles the binary
- **THEN** the builder SHALL automatically include TLS/mTLS handling
- **AND** the builder SHALL include the HTTP server dependency for WebUI when WebUI is enabled

---

## 03 Build Configuration

### Build Flags

```bash
# Standard Go ldflags work as expected
opennavi-build -ldflags="-X main.version=1.0.0" ./my-navi/

# Cross-compilation (same as standard Go)
GOOS=linux GOARCH=amd64 opennavi-build -output ./dist/linux-amd64/oncall-bot ./oncall-navi/
GOOS=darwin GOARCH=arm64 opennavi-build -output ./dist/darwin-arm64/oncall-bot ./oncall-navi/
```

### Build Output

The builder produces:
1. **Binary** — the compiled navi executable
2. **Build metadata** (optional) — version, build time, commit hash embedded in binary
3. **Signature** (optional) — if `--sign` flag is passed, runs codesign on the output

```bash
# Standard build
opennavi-build ./oncall-navi/
# → produces: oncall-bot

# With signing (macOS)
opennavi-build --sign ./oncall-navi/
# → produces: oncall-bot (codesigned with default identity)

# Cross-compile for multiple platforms
opennavi-build -output ./dist/linux-amd64 ./oncall-navi/
opennavi-build -output ./dist/darwin-arm64 ./oncall-navi/
```

### Requirement: Go Build Flag Support
The system SHALL support standard Go ldflags and cross-compilation environment variables in the builder.

#### Scenario: Passing ldflags
- **GIVEN** a developer runs `opennavi-build -ldflags="-X main.version=1.0.0"`
- **WHEN** the builder compiles the binary
- **THEN** the ldflags SHALL be applied to the Go compilation

#### Scenario: Cross-compiling
- **GIVEN** a developer sets `GOOS=linux GOARCH=amd64`
- **WHEN** the builder compiles the binary
- **THEN** the builder SHALL produce a binary for the target platform
- **AND** the binary SHALL be written to the specified output directory

### Requirement: Build Output Artifacts
The system SHALL produce a compiled navi executable, optionally embedding build metadata and optionally signing the output when `--sign` is passed.

#### Scenario: Building with metadata and signing
- **GIVEN** a developer runs `opennavi-build --sign ./oncall-navi/`
- **WHEN** the builder compiles the binary
- **THEN** the builder SHALL embed build metadata such as version, build time, and commit hash
- **AND** the builder SHALL run the platform's code signing tool on the output

---

## 04 Tool Compilation

Tools defined in either Go code or YAML are compiled into the binary:
- **Go tools** (`internal/agent/tools.go`, `tools/monitoring/*.go`) — compiled directly as Go functions
- **YAML tool definitions** — processed by builder, handler references resolved to Go files, then compiled

The builder doesn't do anything special here — it's just standard Go compilation.

```go
// In my-navi/main.go (compiled into binary)
func githubIssuesHandler(args map[string]any) (string, error) {
    client := github.NewClient(os.Getenv("GITHUB_TOKEN"))
    issues, _ := client.Issues.List(...)
    return formatIssues(issues), nil
}

// This function is compiled into the binary at build time.
// No runtime loading, no external dependencies.
```

**Key point:** The entire tool implementation — including any third-party libraries it uses — is linked into the final binary. The binary is self-contained.

### Requirement: Tool Compilation from Go and YAML
The system SHALL compile tools defined in Go code or YAML into the binary, resolving YAML handler references to Go files.

#### Scenario: Compiling Go-defined tools
- **GIVEN** a navi project defines tools in Go source files
- **WHEN** the builder compiles the project
- **THEN** the tool functions SHALL be compiled directly into the binary

#### Scenario: Compiling YAML-defined tools
- **GIVEN** a navi project defines tools in YAML with handler file references
- **WHEN** the builder processes the definition
- **THEN** the builder SHALL resolve the handler references to Go files
- **AND** the builder SHALL compile them into the binary

### Requirement: Self-Contained Binary
The system SHALL link the entire tool implementation, including third-party libraries, into the final binary.

#### Scenario: Running a navi with third-party tool libraries
- **GIVEN** a tool implementation uses third-party libraries
- **WHEN** the builder compiles the binary
- **THEN** the libraries SHALL be linked into the final binary
- **AND** the binary SHALL operate without external dependencies at runtime

---

## 05 Skill Compilation

Skills are text/config that get baked into the system prompt at build time:

```go
// At build time, skills are processed and embedded
skills := []SkillDefinition{...}  // from navi definition
systemPrompt := constructSystemPrompt(skills)

// The resulting system prompt is compiled into the binary as a string constant
// or loaded at runtime from an embedded file (same effect)
```

The builder doesn't need to do anything special — it's just Go code that runs at build time and produces the final binary with the system prompt baked in.

### Requirement: Skill Baking into System Prompt
The system SHALL process skills at build time and bake the resulting system prompt into the binary.

#### Scenario: Compiling skills into the system prompt
- **GIVEN** a navi definition includes skill definitions
- **WHEN** the builder compiles the navi
- **THEN** the skills SHALL be processed into the system prompt at build time
- **AND** the system prompt SHALL be compiled into the binary

---

## 06 Extension Compilation

Extensions are **not compiled into the navi binary**. They're separate standplugin binaries that get loaded at runtime by the opennavi SDK (see `05-extension-contract.md`). The builder only validates extension references:

```go
// In navi definition — extensions NOT compiled in
extensions: ExtensionConfig{
    AllowStandalonePlugins: true,
    AllowedSigners: []string{"github.com/oncall-bot/monitoring-ext"},
}
```

The builder verifies that referenced standplugins exist and are signed by allowed signers. The actual standplugin binaries are distributed separately.

### Requirement: Extension Reference Validation
The system SHALL validate extension references at build time, verifying that referenced standplugins exist and are signed by allowed signers, without compiling the extensions into the navi binary.

#### Scenario: Validating extension references
- **GIVEN** a navi definition references standplugins with an allowed signers list
- **WHEN** the builder compiles the navi
- **THEN** the builder SHALL verify the referenced standplugins exist and are signed by the allowed signers
- **AND** the standplugin binaries SHALL NOT be compiled into the navi binary

---

## 07 Build Verification

After compilation, the builder can run verification checks:

```bash
opennavi-build --verify ./oncall-navi/
```

Verification includes:
1. **Binary size check** — warn if binary exceeds expected size (indicates unexpected dependencies)
2. **Interface consistency** — verify selected interfaces are compatible
3. **Tool reference validation** — ensure all referenced tools have implementations
4. **Extension signer verification** — confirm standplugin binaries match allowed signers

### Requirement: Build Verification Checks
The system SHALL support post-compilation verification via `--verify`, including binary size checks, interface consistency checks, tool reference validation, and extension signer verification.

#### Scenario: Verifying a build
- **GIVEN** a developer runs `opennavi-build --verify ./oncall-navi/`
- **WHEN** the builder completes compilation
- **THEN** the builder SHALL check the binary size against expected limits
- **AND** the builder SHALL verify interface compatibility, tool implementations, and extension signers
- **AND** the builder SHALL report any verification failures

---

## 08 Distribution Workflow

The typical distribution flow:

```bash
# 1. Developer writes navi and builds it
cd oncall-navi/
opennavi-build -output ./dist/oncall-bot .

# 2. Developer signs the binary (platform-specific)
codesign --sign "Developer Identity" dist/oncall-bot  # macOS
# or: gpg --detach-sign --armor dist/oncall-bot  # Linux
# or: signtool sign dist/oncall-bot.exe            # Windows

# 3. Distribute via any method
cp dist/oncall-bot /usr/local/bin/           # direct install
tar -czf oncall-bot-1.0.0.tar.gz dist/      # archive distribution
docker build --build-context . Dockerfile    # container packaging
```

End users verify the signature before running:

```bash
# macOS
codesign --verify oncall-bot

# Linux (if GPG-signed)
gpg --verify oncall-bot.asc oncall-bot

# Run after verification
./oncall-bot
```

### Requirement: Platform-Specific Signing
The system SHALL support signing the built binary with platform-specific tools: `codesign` on macOS, GPG detached signatures on Linux, and `signtool` on Windows.

#### Scenario: Signing on macOS
- **GIVEN** a built navi binary on macOS
- **WHEN** the developer runs `codesign --sign "Developer Identity" oncall-bot`
- **THEN** the binary SHALL be signed with the developer identity
- **AND** users SHALL be able to verify the signature with `codesign --verify`

#### Scenario: Signing on Linux
- **GIVEN** a built navi binary on Linux
- **WHEN** the developer runs `gpg --detach-sign --armor oncall-bot`
- **THEN** the system SHALL produce a detached signature file `oncall-bot.asc`
- **AND** users SHALL be able to verify the signature with `gpg --verify oncall-bot.asc oncall-bot`

### Requirement: Pre-Run Signature Verification
The system SHALL require end users to verify the binary's signature before running it.

#### Scenario: Verifying before running
- **GIVEN** a user has downloaded a navi binary and signature
- **WHEN** the user runs verification
- **THEN** the system SHALL confirm the signature is valid before the user runs the binary

---

## 09 SDK Integration

The builder is part of the opennavi SDK. Developers import it in their navi code:

```go
import "github.com/flintwillow-software/opennavi/sdk"

func main() {
    sdk.Build(navi{...})  // builds and exits
}
```

Or they can use the CLI tool directly for more control:

```bash
# Build with custom flags
opennavi-build -ldflags="-X main.commit=abc123" ./my-navi/

# Cross-compile
GOOS=linux GOARCH=amd64 opennavi-build -output ./dist/oncall-bot-linux-amd64 ./oncall-navi/
```

### Requirement: SDK Build Function
The system SHALL expose the builder through the opennavi SDK, invocable via `sdk.Build()` in the navi's Go code.

#### Scenario: Building via sdk.Build
- **GIVEN** a navi's entry point imports the opennavi SDK
- **WHEN** the developer runs the entry point program
- **THEN** the SDK SHALL build the navi and exit

### Requirement: CLI Build Tool
The system SHALL provide an `opennavi-build` CLI tool supporting custom flags and cross-compilation.

#### Scenario: Building with the CLI
- **GIVEN** a developer runs `opennavi-build -ldflags="-X main.commit=abc123" ./my-navi/`
- **WHEN** the CLI compiles the navi
- **THEN** the CLI SHALL apply the custom flags
- **AND** the CLI SHALL support cross-compilation via GOOS/GOARCH environment variables

---

## 10 What the Builder Does NOT Do

The builder is intentionally minimal:
- **No custom linker** — uses standard Go toolchain
- **No code obfuscation** — developers can obfuscate themselves if desired (go build -ldflags="-s -w")
- **No dependency scanning** — all dependencies are compiled in, no runtime downloads
- **No plugin compilation** — extensions are built separately by their own authors

The builder's job is: take the navi definition → compile it into a single binary. That's it. Everything else is standard Go tooling.

### Requirement: Standard Go Toolchain
The system SHALL use the standard Go toolchain for compilation, with no custom linker.

#### Scenario: Compiling a navi
- **GIVEN** a developer runs the builder
- **WHEN** the builder compiles the navi
- **THEN** the builder SHALL use the standard Go toolchain
- **AND** the builder SHALL NOT perform code obfuscation or dependency scanning

### Requirement: No Extension Compilation by Builder
The system SHALL NOT compile extensions as part of the navi build; extensions SHALL be built separately by their own authors.

#### Scenario: Building extensions separately
- **GIVEN** a navi definition references extensions
- **WHEN** the builder compiles the navi
- **THEN** the builder SHALL NOT compile the extension binaries
- **AND** the extensions SHALL be built and distributed separately by their authors

---

## 11 Next Steps

- **[01-navi-definition.md](./01-navi-definition.md)** — What the developer writes (the input to this builder)
- **[03-interface-set.md](./03-interface-set.md)** — Detailed interface configuration for each delivery mode
- **[04-trust-signing-model.md](./04-trust-signing-model.md)** — How to sign and verify the output binary


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000300 | 0.1.0 | Migrated from docs/spec/ to spec-tree |