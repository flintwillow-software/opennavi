---
status: draft
created: '2026-07-31'
updated: '2026-07-31'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000200, 000201, 000202, 000203, 000204, 000205, 000206]
---

# Navi Definition Format

**Status:** Draft  
**Last updated:** 2026-07-31

---

## 00 Overview

A navi is defined by seven components: **name**, **tools**, **skills**, **personality**, **interfaces**, **extensions**, and **permissions**. Developers write these in Go OR YAML using the opennavi SDK, then run the builder to produce a single compiled binary. The definition is what gets compiled into the binary — there's no runtime loading of tools or skills (except optional extensions).

### Requirement: Definition Components
The system SHALL define a navi by seven components: name, tools, skills, personality, interfaces, extensions, and permissions.

#### Scenario: Writing a complete navi definition
- **GIVEN** a developer writes a navi definition
- **WHEN** the definition includes all seven components
- **THEN** the system SHALL accept the definition
- **AND** the builder SHALL compile it into a single binary

### Requirement: Dual Format Support
The system SHALL support navi definitions written in either Go code or YAML, producing the same compiled binary from both.

#### Scenario: Building from Go and YAML definitions
- **GIVEN** a navi definition written in Go code
- **WHEN** the builder compiles it
- **THEN** the system SHALL produce a compiled binary equivalent to the YAML definition of the same navi

### Requirement: No Runtime Component Loading
The system SHALL compile tools and skills into the binary, with no runtime loading of tools or skills except optional extensions.

#### Scenario: Running a navi without component files
- **GIVEN** a navi binary has been built
- **WHEN** the binary is run on a machine without the source project
- **THEN** the navi SHALL operate with all its tools and skills available
- **AND** the navi SHALL NOT require loading tool or skill files at runtime

---

## 01 The Navi Definition

### Go Code Format

```go
package main

import (
    "github.com/flintwillow-software/opennavi/sdk"
)

func main() {
    sdk.Build(navi{
        name:        "oncall-bot",
        version:     "1.0.0",
        
        // Tools compiled into the binary
        tools: []ToolDefinition{...},
        
        // Skills baked into system prompt at build time (compiled-in)
        skills: []SkillDefinition{...},
        
        // Personality — how the navi communicates and thinks
        personality: "You are OnCall Bot...",
        
        // Interfaces — which delivery modes get compiled in
        interfaces: InterfaceSet{...},
        
        // Extensions — optional runtime plugins (not compiled in)
        extensions: ExtensionConfig{...},
        
        // IAM-style permissions — what the navi is allowed to do
        permissions: PermissionPolicy{...},
    })
}
```

### YAML Format

```yaml
# naviconfig.yaml
name: oncall-bot
version: 1.0.0
personality: |
  You are OnCall Bot. Communicate concisely and technically.

tools:
  - name: query_grafana
    description: Query Grafana for metrics
    handler: tools/monitoring/grafana.go

skills:
  - name: incident-response
    instructions_file: skills/incident-response.md
    keywords: [alert, incident]

interfaces:
  gateway:
    enabled: true
    port: 443
  cron:
    enabled: true

# Optional: thought queue config (only for daemon navis)
thought_queue:
  max_concurrent: 3
  priority_slots: 1
  slot_exhaustion: "queue"

extensions:
  allow_standalone_plugins: true

permissions:
  allow:
    - tool: query_grafana
      action: "*"
    - path: /src/**
      action: [read]
  deny:
    - tool: shell
    - path: ~/.ssh/*
      action: "*"
```

Both formats produce the same compiled binary. The builder detects which format is present and processes accordingly.

### Requirement: SDK Build Entry Point
The system SHALL compile a navi when the developer's entry point calls `sdk.Build()` with the navi definition.

#### Scenario: Invoking sdk.Build
- **GIVEN** a developer's `main()` function calls `sdk.Build(navi{...})`
- **WHEN** the resulting program is run
- **THEN** the system SHALL compile the navi definition into a binary
- **AND** the build process SHALL exit after producing the binary

---

## 02 Components

### 1. Name

Identifier for the navi. Used in:
- Binary filename (default: `<name>` or `opennavi-<name>`)
- System prompt identity ("I am {name}")
- Plugin/extension compatibility checks

```go
name: "oncall-bot",
// Produces binary: oncall-bot (or opennavi-oncall-bot)
```

### 2. Tools

Capabilities the navi can execute. Tool definitions include:
- **Name** — identifier used by the agent
- **Description** — what the tool does (shown to the LLM)
- **Parameters** — JSON Schema for arguments
- **Implementation** — compiled-in handler function or standplugin reference

```go
tools: []ToolDefinition{
    {
        Name:        "github_issues",
        Description: "List and manage GitHub issues",
        Parameters:  tool.JSONSchema(`{"type":"object","properties":{...}}`),
        Handler:     githubIssuesHandler,
    },
}

func githubIssuesHandler(args map[string]any) (string, error) {
    client := github.NewClient(os.Getenv("GITHUB_TOKEN"))
    issues, _ := client.Issues.List(...)
    return formatIssues(issues), nil
}
```

**Key point:** Tool implementations are **compiled into the navi binary**. They're not loaded from external scripts or packages. This is what makes the binary self-contained and verifiable.

### 3. Skills

Role-specific instructions that shape how the agent behaves. Two loading tiers:

| Tier | Location | Compiled In? | Verified? |
|------|----------|--------------|-----------|
| **Compiled-in** | `skills/` in project root | Yes (build time) | Yes (part of signed binary) |
| **Runtime-loaded** | `.config/navi/$NAVI_NAME/skills/` | No (runtime) | No (user-modifiable) |

See [02-agent-config/00-skills-system.md](../02-agent-config/00-skills-system.md) for detailed skill definition format and two-tier loading behavior.

### 4. Personality

How the navi communicates and thinks. This is text/config that gets baked into the binary as part of the system prompt.

```go
personality: `You are OnCall Bot, an on-call engineer assistant. You communicate 
concisely and technically. You prioritize accuracy over verbosity. When uncertain, 
you say so explicitly rather than guessing.`,
```

**Personality can also be loaded from a file:**

```go
personality: "file:./persona.md",  // loaded at build time, compiled into binary
```

Or as a directory (multiple personality files):

```go
personality: "dir:./persona/",  // all .md files in dir concatenated
```

### 5. Interfaces

Delivery modes that get **compiled into the binary**. Only selected interfaces are included — this controls binary size and attack surface.

See [03-interface-set.md](./03-interface-set.md) for detailed interface configuration options.

**Why compile interfaces in?**
- Smaller binary — only what's needed
- Fewer runtime dependencies
- Security — unselected interfaces can't be accidentally enabled
- Performance — no interface discovery overhead at startup

### 6. Extensions

Optional runtime plugins that augment functionality without rebuilding the binary. Extensions are **separate compiled binaries** with their own trust model (see [05-extension-contract.md](./05-extension-contract.md)).

```go
extensions: ExtensionConfig{
    AllowStandalonePlugins: true,  // enable standplugin loading
    
    // Optional: restrict which extensions can be loaded
    AllowedSigners: []string{
        "github.com/oncall-bot/monitoring-ext",
        "github.com/oncall-bot/alerting-ext",
    },
}
```

**Key point:** Extensions are **not compiled into the navi binary**. They're loaded at runtime as separate processes with signature verification. This allows end users to choose which extensions to install without rebuilding the navi.

### 7. IAM-Style Permissions

A policy document that defines what tools and operations the navi is allowed to perform. Enforced by the agent loop before any tool execution.

```go
permissions: PermissionPolicy{
    Allow: []PermissionRule{
        {Tool: "github_issues", Action: "*"},
        {Path: "/src/**", Actions: []string{"read"}},
        {Tool: "query_grafana", Action: "*"},
    },
    Deny: []PermissionRule{
        {Tool: "shell"},
        {Path: "~/.ssh/*", Actions: []string{"*"}},
    },
}
```

**How permissions work:**
1. Policy is compiled into the binary at build time (part of verifiable trust boundary)
2. At runtime, agent loop checks each tool call against policy before execution
3. If a tool call violates the policy, it's rejected with an error message
4. Users can layer on additional restrictions via `.config/navi/$NAVI_NAME/policy.yaml` at runtime

**Permission rule format:**
```yaml
# policy.yaml (runtime override or compiled-in)
allow:
  - tool: query_grafana          # Allow all actions on this tool
    action: "*"
  - path: /src/**                # Allow read access to source files
    action: [read]               # Or "write", "execute"
deny:
  - tool: shell                  # Deny all shell execution
  - path: ~/.ssh/*               # Deny access to SSH keys
    action: "*"

# Combined allow+deny (deny takes precedence)
allow:
  - tool: bash
    action: [read, execute]      # Allow read/execute on bash
deny:
  - tool: bash
    path: "*rm*"                 # But deny rm commands specifically
```

**Key point:** Permissions are part of the verifiable trust boundary. They're compiled into the binary alongside tools and skills, so end users can verify what a navi is allowed to do by checking its signature.

### Requirement: Name Component
The system SHALL use the navi name for the binary filename, system prompt identity, and plugin/extension compatibility checks.

#### Scenario: Naming a navi
- **GIVEN** a navi definition has `name: "oncall-bot"`
- **WHEN** the builder compiles the navi
- **THEN** the binary SHALL be named `oncall-bot` (or `opennavi-oncall-bot`)
- **AND** the name SHALL be used in the system prompt identity

### Requirement: Tool Compilation
The system SHALL compile tool implementations into the navi binary rather than loading them from external scripts or packages.

#### Scenario: Using a compiled tool
- **GIVEN** a navi definition includes a tool with a Go handler
- **WHEN** the builder compiles the navi
- **THEN** the tool implementation SHALL be linked into the binary
- **AND** the tool SHALL be callable by the agent at runtime without external files

### Requirement: Skill Two-Tier Loading
The system SHALL support two skill loading tiers: compiled-in skills from the project root `skills/` directory, and runtime-loaded skills from `.config/navi/$NAVI_NAME/skills/`.

#### Scenario: Loading a compiled-in skill
- **GIVEN** a skill exists in the project root `skills/` directory
- **WHEN** the builder compiles the navi
- **THEN** the skill SHALL be baked into the system prompt at build time
- **AND** the skill SHALL be verified as part of the signed binary

#### Scenario: Loading a runtime skill
- **GIVEN** a user adds a skill to `.config/navi/$NAVI_NAME/skills/`
- **WHEN** the navi starts
- **THEN** the skill SHALL be loaded at runtime
- **AND** the skill SHALL NOT be verified as part of the signed binary

### Requirement: Personality Compilation
The system SHALL bake the navi's personality into the binary as part of the system prompt at build time.

#### Scenario: Setting personality as a string
- **GIVEN** a developer sets `personality` as a string in the navi definition
- **WHEN** the builder compiles the navi
- **THEN** the personality text SHALL be compiled into the binary as part of the system prompt

#### Scenario: Loading personality from a file
- **GIVEN** a developer sets `personality: "file:./persona.md"`
- **WHEN** the builder compiles the navi
- **THEN** the personality SHALL be loaded from the file at build time and compiled into the binary

### Requirement: Interface Selection
The system SHALL compile only the interfaces selected in the navi definition into the binary.

#### Scenario: Selecting a subset of interfaces
- **GIVEN** a navi definition enables only Gateway and Cron interfaces
- **WHEN** the builder compiles the navi
- **THEN** only Gateway and Cron SHALL be included in the binary
- **AND** unselected interfaces SHALL NOT be present or accidentally enabled

### Requirement: Extension Declaration
The system SHALL support optional runtime extensions declared in the navi definition, with an allowed signers list restricting which extension signers may be loaded.

#### Scenario: Restricting extensions by signer
- **GIVEN** a navi definition sets `AllowedSigners` to specific extension authors
- **WHEN** the navi loads an extension at runtime
- **THEN** the navi SHALL only load extensions signed by the allowed signers
- **AND** the navi SHALL reject extensions signed by other signers

### Requirement: IAM-Style Permission Enforcement
The system SHALL enforce the IAM-style permission policy before any tool execution, rejecting tool calls that violate the policy.

#### Scenario: Allowing a permitted tool call
- **GIVEN** a navi policy allows `query_grafana` with action `*`
- **WHEN** the agent calls the `query_grafana` tool
- **THEN** the agent loop SHALL permit the tool execution

#### Scenario: Denying a restricted tool call
- **GIVEN** a navi policy denies the `shell` tool
- **WHEN** the agent attempts to call the `shell` tool
- **THEN** the agent loop SHALL reject the tool call with an error message

### Requirement: Permission Deny Precedence
The system SHALL apply deny rules with precedence over allow rules in the permission policy.

#### Scenario: Conflicting allow and deny rules
- **GIVEN** a policy allows `shell_exec` with read and execute actions
- **WHEN** the agent attempts an action also matched by a deny rule such as `*rm*`
- **THEN** the deny rule SHALL take precedence
- **AND** the tool call SHALL be rejected

### Requirement: User Permission Override
The system SHALL support additional permission restrictions layered via `.config/navi/$NAVI_NAME/policy.yaml` at runtime.

#### Scenario: Adding runtime restrictions
- **GIVEN** an end user adds restrictions to `.config/navi/oncall-bot/policy.yaml`
- **WHEN** the navi starts
- **THEN** the runtime policy SHALL layer on top of the compiled-in policy
- **AND** the agent loop SHALL enforce the combined restrictions

---

## 03 Example: On-Call Bot Navi

```go
package main

import (
    "github.com/flintwillow-software/opennavi/sdk"
)

func main() {
    sdk.Build(navi{
        name:        "oncall-bot",
        version:     "1.0.0",
        
        tools: []ToolDefinition{
            // GitHub adapter (compiled in)
            githubIssues,
            
            // Monitoring tools (standplugin — loaded at runtime)
            monitoringTools,  // references opennavi-monitoring standplugin
            
            // Webhook delivery (built into Gateway interface)
            webhookDelivery,
        },
        
        skills: []SkillDefinition{
            {
                Name: "incident-response",
                Instructions: `You respond to on-call alerts. When you receive 
an alert webhook, classify severity (P0-P3), query relevant metrics using 
monitoring tools, and provide initial triage advice. Escalate P0 incidents 
immediately via Telegram/Discord delivery.`,
                Keywords: []string{"alert", "incident", "on-call"},
            },
        },
        
        personality: `You are OnCall Bot. You communicate concisely and technically. 
Prioritize accuracy over verbosity. When uncertain about severity or impact, 
state your uncertainty explicitly rather than guessing.`,
        
        interfaces: InterfaceSet{
            Gateway: InterfaceConfig{Enabled: true, HTTPPort: 443},
            Cron:    InterfaceConfig{Enabled: true},
            Discord: InterfaceConfig{Enabled: true, Token: "${DISCORD_TOKEN}"},
        },
        
        extensions: ExtensionConfig{
            AllowStandalonePlugins: true,
            AllowedSigners: []string{"github.com/oncall-bot/monitoring-ext"},
        },
        
        permissions: PermissionPolicy{
            Allow: []PermissionRule{
                {Tool: "query_grafana", Action: "*"},
                {Path: "/src/**", Actions: []string{"read"}},
            },
            Deny: []PermissionRule{
                {Tool: "shell"},
                {Path: "~/.ssh/*", Actions: []string{"*"}},
            },
        },
    })
}
```

**What this produces:**
- Binary: `oncall-bot` (or `opennavi-oncall-bot`)
- Compiled in: GitHub tools, monitoring standplugin reference, cron scheduler, Discord adapter, permission policy
- Runtime loaded: monitoring standplugin (signed by oncall-bot team)
- Permissions enforced: can query Grafana and read source files, cannot execute shell or access SSH keys
- Signature verification required before running

### Requirement: Complete Navi Example
The system SHALL support a single navi definition combining compiled tools, runtime standplugin references, skills, personality, multiple interfaces, extensions, and permission policies.

#### Scenario: Building the on-call bot example
- **GIVEN** a developer defines the on-call bot navi with the complete component set
- **WHEN** the builder compiles it
- **THEN** the system SHALL produce a binary with compiled-in GitHub tools, cron scheduler, Discord adapter, and permission policy
- **AND** the monitoring standplugin SHALL be loaded at runtime, signed by the on-call bot team
- **AND** the permissions SHALL enforce Grafana queries and source reads while denying shell and SSH key access

---

## 04 Navi Definition vs. Runtime Configuration

The navi definition is **static** — it's what gets compiled into the binary at build time. Runtime configuration is separate and handled by the opennavi SDK:

| Aspect | Navi Definition | Runtime Config |
|--------|----------------|----------------|
| When set | At build time (compiled in) | At runtime (user config) |
| Where stored | In the binary itself | `.config/navi/$NAVI_NAME/` or `~/.opennavi/` |
| What it controls | Tools, skills, personality, interfaces, permissions | LLM provider keys, session settings, policy overrides |
| Who sets it | Navi developer | End user |
| Verified? | Yes (part of signed binary) | No (user-modifiable) |

**Example:** The navi developer defines which tools are available and what's allowed (compiled in). The end user configures their Anthropic API key at runtime via settings.

### Requirement: Static Definition vs. Runtime Config Separation
The system SHALL treat the navi definition as static, compiled into the binary at build time, and runtime configuration as user-modifiable and loaded at runtime.

#### Scenario: Separating developer and user concerns
- **GIVEN** a navi binary is built from a static definition
- **WHEN** an end user configures the navi at runtime
- **THEN** the user's runtime configuration SHALL NOT modify the compiled definition
- **AND** runtime configuration SHALL include items such as LLM provider keys, session settings, and policy overrides

---

## 05 Validation

The SDK validates navi definitions at build time:

1. **Name format** — must be lowercase alphanumeric with hyphens
2. **Tool uniqueness** — no duplicate tool names
3. **Interface compatibility** — some interfaces can't be enabled together (e.g., Gateway + WebUI both listen on HTTP, conflict on port)
4. **Extension signer verification** — if `AllowedSigners` is set, extension binaries must be signed by those keys
5. **Permission rule validation** — check for invalid tool names or path patterns in policy rules
6. **Personality format** — either string or file path

Invalid definitions produce build errors with clear messages. No runtime surprises.

### Requirement: Name Format Validation
The system SHALL validate that the navi name is lowercase alphanumeric with hyphens at build time.

#### Scenario: Providing an invalid name
- **GIVEN** a navi definition with an invalid name such as one containing uppercase characters or underscores
- **WHEN** the builder runs validation
- **THEN** the builder SHALL produce a clear build error
- **AND** the build SHALL fail before producing a binary

### Requirement: Tool Uniqueness Validation
The system SHALL reject navi definitions with duplicate tool names at build time.

#### Scenario: Providing duplicate tool names
- **GIVEN** a navi definition declares two tools with the same name
- **WHEN** the builder runs validation
- **THEN** the builder SHALL produce a clear build error

### Requirement: Interface Compatibility Validation
The system SHALL validate that enabled interfaces are compatible, rejecting conflicting combinations such as Gateway and WebUI both listening on HTTP.

#### Scenario: Enabling conflicting interfaces
- **GIVEN** a navi definition enables both Gateway and WebUI interfaces
- **WHEN** the builder runs validation
- **THEN** the builder SHALL detect the port conflict
- **AND** the builder SHALL produce a clear build error

### Requirement: Extension Signer Validation
The system SHALL verify at build time that extension binaries referenced by the navi are signed by keys in the `AllowedSigners` list.

#### Scenario: Referencing an unsigned extension
- **GIVEN** a navi definition has `AllowedSigners` set and references an extension not signed by those keys
- **WHEN** the builder runs validation
- **THEN** the builder SHALL reject the definition with an error

### Requirement: Clear Build Errors
The system SHALL produce build errors with clear messages for invalid definitions.

#### Scenario: Receiving a build error
- **GIVEN** a navi definition fails validation
- **WHEN** the builder reports the error
- **THEN** the error message SHALL clearly describe the validation failure
- **AND** the builder SHALL not produce a binary from the invalid definition

---

## 06 Next Steps

- **[01-navi-project-layout.md](./01-navi-project-layout.md)** — Go project conventions and directory structure
- **[02-builder-compiler.md](./02-builder-compiler.md)** — How the SDK takes this definition and compiles it into a binary
- **[03-interface-set.md](./03-interface-set.md)** — Detailed interface configuration for each delivery mode
- **[02-agent-config/00-skills-system.md](../02-agent-config/00-skills-system.md)** — Two-tier skill loading (compiled-in vs runtime)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000200 | 0.1.0 | Migrated from docs/spec/ to spec-tree |
| 2026-09-12 | S000200 | 0.2.1 | Renamed `bash` → `shell_exec` in deny-precedence scenario (namespaced tool names per S130400 §04) |