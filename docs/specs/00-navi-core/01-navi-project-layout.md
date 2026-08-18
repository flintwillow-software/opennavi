---
status: draft
created: '2026-07-31'
updated: '2026-07-31'
tags:
  - spec
  - navi-core
priority: medium
spec-refs: [000100, 000101, 000102, 000103, 000104, 000105, 000106]
---

# Navi Project Layout

**Status:** Draft  
**Last updated:** 2026-07-31

---

## 00 Overview

Each navi is its own Go project that follows standard Go conventions for directory structure. The builder processes the project and compiles everything into a single executable binary. This doc describes where different types of code and config live in a navi project.

### Requirement: Go Project Structure
The system SHALL require each navi to be its own Go project following standard Go directory conventions.

#### Scenario: Creating a navi project
- **GIVEN** a developer starts a new navi
- **WHEN** the developer initializes the project
- **THEN** the project SHALL follow standard Go project structure conventions
- **AND** the builder SHALL process the project into a single executable binary

---

## 01 Standard Directory Structure

```
my-navi/                          # Developer's Go project root
├── cmd/
│   └── oncall-bot/               # Entry point (required)
│       └── main.go               # Calls sdk.Build(navi{...})
│
├── internal/                     # Compiled-in code (trusted, part of binary)
│   ├── agent/                    # Agent runtime components
│   │   ├── tools.go              # Tool implementations
│   │   ├── skills.go             # Skill definitions  
│   │   ├── hooks.go              # Hook handlers
│   │   └── commands.go           # Slash command handlers
│   └── config/                   # Compiled-in configuration
│       └── policies.go           # Base permission policies
│
├── skills/                       # Skills loaded at build time (compiled in)
│   ├── incident-response.md      # Markdown skill files
│   ├── post-mortem.md
│   └── code-review.md
│
├── tools/                        # Additional tool source files
│   └── monitoring/
│       ├── grafana.go            # Grafana query tool
│       └── betterstack.go        # BetterStack log tool
│
├── naviconfig.yaml                 # Navi definition (alternative to Go code)
├── go.mod                        # Go module file
├── go.sum                        # Go checksums
└── README.md                     # Project documentation
```

### Requirement: Entry Point Convention
The system SHALL require each navi to have exactly one entry point at `cmd/<navi-name>/main.go`.

#### Scenario: Building a navi with an entry point
- **GIVEN** a navi project has its entry point at `cmd/oncall-bot/main.go`
- **WHEN** the builder processes the project
- **THEN** the builder SHALL use the entry point to compile the navi binary

### Requirement: Compiled-In Code Location
The system SHALL compile code in `internal/`, `skills/`, and `tools/` directories into the navi binary.

#### Scenario: Placing code in compiled directories
- **GIVEN** a developer places tool implementations in `internal/agent/tools.go` and skills in `skills/`
- **WHEN** the builder compiles the project
- **THEN** the code and skills SHALL be compiled into the binary
- **AND** they SHALL be part of the verifiable trust boundary

---

## 02 Directory Purposes

### `cmd/` — Entry Point

Each navi has exactly one entry point in `cmd/<navi-name>/main.go`. This is the only file that's strictly required to be in this location (Go convention). The entry point calls `sdk.Build()` with the navi definition.

```go
// cmd/oncall-bot/main.go
package main

import (
    "github.com/flintwillow-software/opennavi/sdk"
)

func main() {
    sdk.Build(navi{
        name:        "oncall-bot",
        version:     "1.0.0",
        tools:       []ToolDefinition{...},
        skills:      []SkillDefinition{...},
        personality: "...",
        interfaces:  InterfaceSet{...},
    })
}
```

### `internal/` — Compiled-In Code (Trusted)

Code in `internal/` is compiled directly into the binary. This includes tool implementations, hook handlers, and command handlers. The trust boundary extends here — end users verify this code by checking the signature.

**Structure follows Go conventions:**
- `internal/agent/tools.go` — Tool implementation functions
- `internal/agent/hooks.go` — Hook handler functions  
- `internal/agent/commands.go` — Slash command handler functions
- `internal/config/policies.go` — Base permission policies

### `skills/` — Build-Time Skills (Compiled In)

Markdown skill files in the project root are loaded at build time and compiled into the binary. These are part of what the developer ships with the navi.

```markdown
<!-- skills/incident-response.md -->
# Incident Response

You respond to on-call alerts. When you receive an alert webhook, classify severity (P0-P3), query relevant metrics using monitoring tools, and provide initial triage advice. Escalate P0 incidents immediately via Telegram/Discord delivery.
```

### `tools/` — Additional Tool Sources

Optional directory for tool implementation source files. These are Go files that implement tool logic and get compiled into the binary alongside `internal/`.

```go
// tools/monitoring/grafana.go
package monitoring

import "fmt"

func queryGrafana(args map[string]any) (string, error) {
    client := grafana.NewClient(...)
    result, _ := client.Query(args["query"].(string))
    return fmt.Sprintf("Query results: %v", result), nil
}
```

### `naviconfig.yaml` — Navi Definition (Alternative to Go Code)

Instead of writing the navi definition in Go, developers can use a YAML file. The builder processes this into the compiled binary just like Go code. This is useful for simpler navis or when you want configuration separate from logic.

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

extensions:
  allow_standalone_plugins: true
```

**Both Go code and YAML are supported.** Developers can use either or both — the builder processes them together into a single binary.

### Requirement: Entry Point Requirement
The system SHALL require each navi to have exactly one entry point in `cmd/<navi-name>/main.go` that calls `sdk.Build()` with the navi definition.

#### Scenario: Building with a valid entry point
- **GIVEN** a navi project with a single entry point at `cmd/oncall-bot/main.go` calling `sdk.Build()`
- **WHEN** the builder processes the project
- **THEN** the builder SHALL accept the project and produce a binary

#### Scenario: Building with a missing entry point
- **GIVEN** a navi project without an entry point at `cmd/<navi-name>/main.go`
- **WHEN** the builder processes the project
- **THEN** the builder SHALL reject the build with an error

### Requirement: YAML Definition Support
The system SHALL support navi definitions written in YAML via `naviconfig.yaml`, processed by the builder identically to Go code definitions.

#### Scenario: Building with a YAML definition
- **GIVEN** a navi project contains a `naviconfig.yaml` file
- **WHEN** the builder processes the project
- **THEN** the builder SHALL compile the YAML definition into the binary
- **AND** the resulting binary SHALL be equivalent to a Go code definition build

---

## 03 Runtime Config (User-Loaded)

End users customize navis via config files in `.config/navi/$NAVI_NAME/`. These are loaded at runtime, NOT compiled into the binary:

```
.config/navi/oncall-bot/                    # Per-navi user config
├── skills/                                 # User-added skills (runtime loaded)
│   ├── custom-alert-classifier.md          # User's own skill for specific alerts
│   └── internal-tool-usage.md              # How to use their private APIs
├── policy.yaml                             # Runtime permission overrides  
├── persona.md                              # Optional personality override
└── settings.yaml                           # Navi-specific settings
```

**Key distinction:** `skills/` at project root = compiled in (verifiable), `.config/navi/$NAVI_NAME/skills/` = runtime loaded (user-customized, not verified).

### Requirement: User Runtime Configuration
The system SHALL load user configuration from `.config/navi/$NAVI_NAME/` at runtime, separate from the compiled binary.

#### Scenario: Configuring a navi at runtime
- **GIVEN** an end user places configuration files in `.config/navi/oncall-bot/`
- **WHEN** the navi starts
- **THEN** the navi SHALL load the user configuration at runtime
- **AND** the configuration SHALL NOT be part of the compiled binary

### Requirement: Compiled vs. Runtime Skill Distinction
The system SHALL treat `skills/` at the project root as compiled-in and verifiable, and `.config/navi/$NAVI_NAME/skills/` as runtime-loaded and user-modifiable.

#### Scenario: Loading a runtime skill
- **GIVEN** a user adds a skill file to `.config/navi/oncall-bot/skills/`
- **WHEN** the navi starts
- **THEN** the skill SHALL be loaded at runtime without a rebuild
- **AND** the skill SHALL be treated as unverified user content

---

## 04 Go Module Requirements

Every navi project must be a valid Go module:

```bash
# Initialize the module (if starting fresh)
cd my-navi/
go mod init github.com/example/my-navi

# Add opennavi SDK dependency
go get github.com/flintwillow-software/opennavi/sdk
```

The `go.mod` file is required for the builder to resolve dependencies and compile correctly.

### Requirement: Go Module Requirement
The system SHALL require every navi project to be a valid Go module with a `go.mod` file.

#### Scenario: Building a navi without a go.mod
- **GIVEN** a navi project lacks a `go.mod` file
- **WHEN** the builder attempts to compile the project
- **THEN** the builder SHALL fail with an error indicating the missing module

#### Scenario: Building a navi with a go.mod
- **GIVEN** a navi project has a valid `go.mod` with the opennavi SDK dependency
- **WHEN** the builder attempts to compile the project
- **THEN** the builder SHALL resolve dependencies and compile the binary successfully

---

## 05 Building a Navi

```bash
# Build with Go code definition
cd my-navi/
opennavi-build -output ./dist/oncall-bot .

# Build with YAML definition (if naviconfig.yaml exists)
opennavi-build -output ./dist/oncall-bot .

# Both work — builder detects format automatically
```

The builder processes `cmd/<navi-name>/main.go` for Go definitions, or `naviconfig.yaml` for YAML definitions. Either produces the same compiled binary output.

### Requirement: Format Auto-Detection
The system SHALL automatically detect whether a navi project uses a Go code definition or a YAML definition.

#### Scenario: Building a Go-defined navi
- **GIVEN** a navi project with `cmd/<navi-name>/main.go`
- **WHEN** the developer runs `opennavi-build`
- **THEN** the builder SHALL process the Go definition and produce a binary

#### Scenario: Building a YAML-defined navi
- **GIVEN** a navi project with `naviconfig.yaml`
- **WHEN** the developer runs `opennavi-build`
- **THEN** the builder SHALL process the YAML definition and produce a binary

---

## 06 Next Steps

- **[02-navi-definition.md](./02-navi-definition.md)** — Detailed definition format (Go structs and YAML schema)
- **[03-interface-set.md](./03-interface-set.md)** — Delivery modes and interface selection
- **[02-agent-config/00-skills-system.md](../02-agent-config/00-skills-system.md)** — Two-tier skill loading (compiled-in vs runtime)


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000100 | 0.1.0 | Migrated from docs/spec/ to spec-tree |