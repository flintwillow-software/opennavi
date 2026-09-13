---
status: draft
created: '2026-09-08'
updated: '2026-09-13'
tags:
  - spec
  - navi-core
  - sdk
priority: high
spec-refs: [000800, 000801, 000802, 000803, 000804]
---

# SDK Surface

> The opennavi module IS the SDK. This defines the public boundary — what a navi author imports and depends on — versus the internal implementation, and the consistency contract that makes every navi behave alike.

---

## 00 Overview

opennavi is distributed as a Go module: `github.com/flintwillow-software/opennavi`. A navi author creates their own Go repo, imports this module, and calls `sdk.Build(naviDef{...})` to produce a working agent binary. The SDK owns the agent loop, session/state management, store, provider adaptation, tool execution, and interface (CLI/TUI/Gateway) wiring. The author owns the navi definition, custom tools, skills, personality, and which interfaces compile in.

The dividing line between SDK and author is the **public package boundary** of the module. Everything under `internal/` is closed implementation that can change without notice; every public package is a promise with semver back-compat.

### Requirement: SDK as Importable Module

The system SHALL distribute opennavi as a Go module importable by independent navi projects, exposing the builder and core runtime through public packages.

#### Scenario: Author pulls in the SDK

- **GIVEN** a navi author creating a new Go repo
- **WHEN** the author adds `github.com/flintwillow-software/opennavi` as a dependency and imports its public packages
- **THEN** the author SHALL be able to assemble a working agent with a consistent runtime, store, provider, and tool system via `sdk.Build`

#### Scenario: Internal code is not importable

- **GIVEN** the module contains implementation under `internal/`
- **WHEN** an external navi project attempts to import an `internal/` package
- **THEN** the Go compiler SHALL reject the import as inaccessible (Go `internal/` visibility rule)

## 01 Module Layout & Public Boundary

The module splits into public surface and private implementation:

```
github.com/flintwillow-software/opennavi/
├── sdk/                    # PUBLIC — sdk.Build(naviDef), definition types
├── agent/                  # PUBLIC — Agent public API (Prompt/Continue/Abort/
│                           #           Subscribe/State), AgentTool, ToolExecutor
├── tools/                  # PUBLIC — tool registry + catalog (see S130400)
│   ├── registry/           #   ToolDefinition, registry, discovery, active set
│   ├── file/               #   file_read, file_write, file_edit, file_find, file_grep
│   ├── shell/              #   shell_exec (bash variants later)
│   ├── git/                #   git_diff, git_log, git_status, git_commit
│   ├── search/             #   code_ast_parse, code_symbol_search
│   └── test/               #   test_run
├── store/                  # PUBLIC — Store interface + SQLite implementation
├── llm/                    # PUBLIC — Provider interface + adapters (Ollama, OpenAI)
└── internal/               # PRIVATE — daemon, session internals, compaction,
                            #           executor concurrency, cmd/navi CLI, config
```

Public packages are the SDK contract. Their exported API follows Go semver stability: breaking changes require a major version bump. `internal/` has no such promise and is subject to change at any time.

### Requirement: Public/Internal Split

The system SHALL keep all implementation that is not part of the authoring contract under `internal/`, and expose only the agreed public surface as importable packages.

#### Scenario: Public surface is stable

- **GIVEN** a navi built on a released version of the SDK
- **WHEN** a newer SDK patch or minor version is released
- **THEN** the navi SHALL continue to compile against the public packages
- **AND** internal implementation changes SHALL NOT break the build

#### Scenario: Author uses public runtime API

- **GIVEN** a navi embedding the agent runtime programmatically (not just via `sdk.Build`)
- **WHEN** the author imports `agent` and calls `Prompt`/`Subscribe`/`State`
- **THEN** the runtime SHALL behave identically to the binary produced by `sdk.Build`

## 02 Consistency Contract

The SDK guarantees a consistent agent by owning the semantics that must not vary between navis:

| Concern | Owner | Why it's SDK-owned |
|---------|-------|--------------------|
| Agent loop (think→act→observe) | SDK | identical behavior across navis |
| Session/state (copy-on-write) | SDK | branching, steering, follows-up work the same |
| Store (session persistence) | SDK | one store contract, swappable impl |
| Provider adaptation | SDK | provider-agnostic message/tool types |
| Tool execution (parallel/sequential, bounded) | SDK | concurrency semantics are uniform |
| Tool registry & catalog | SDK | standard tool set + metadata contract |
| Error recovery / truncation | SDK | S010400 strategies applied uniformly |
| Interfaces (CLI/TUI/Gateway) | SDK | delivery modes behave consistently |

The author owns: navi definition, personality/system prompt, skills (compiled-in), custom/application tools, interface selection, runtime config defaults.

### Requirement: SDK-Owned Consistency

The system SHALL ensure that agent loop, session, store, provider, tool execution, and interface semantics are provided by the SDK so that all navis built on it exhibit consistent agent behavior.

#### Scenario: Two navis, same core

- **GIVEN** two independent navis built on the SDK with different personalities and tools
- **WHEN** both run the same prompt against the same provider
- **THEN** both SHALL exhibit the same agent-loop, session, and error-recovery semantics
- **AND** differences SHALL be limited to definition-owned concerns (tools, prompt, skills)

### Requirement: Authoring Contract

The system SHALL require navi authors to express personality, skills, custom tools, and interface selection through the definition, delegating runtime semantics to the SDK.

#### Scenario: Author extends without forking

- **GIVEN** a navi author needs a navi-specific tool
- **WHEN** the author registers it through the public tool registry alongside catalog tools
- **THEN** the tool SHALL run within the SDK's execution semantics (parallel bounds, timeout, recovery)
- **AND** the author SHALL NOT need to fork or modify SDK internals

## 03 Tool Surface

The tool system is the first full SDK surface the catalog implements (epic #199). The public contract (S130400) covers:

- **Self-describing `ToolDefinition`** — registration declares name, description, parameters schema, output format, access, timeout, recovery, mode, namespace, tags, provenance.
- **Registered vs active** — the registry holds all tool metadata; only the active subset is offered to the LLM, and the active set is adjustable.
- **Provenance** — every tool records its origin (`builtin`, `sdk`, `extension`, `author`) so the catalog is auditable and filterable.
- **Two tool sources** — compile-time catalog tools (in-binary, static, CGO-free) and runtime standplugin extensions (JSON-RPC, signed, sandboxed per S000600). Both register through the same registry.

### Requirement: Agent Tools vs Application Tools

The system SHALL distinguish SDK-owned agent tools (the catalog: `file_*`, `shell_exec`, `git_*`, `test_run`, `code_*`) from author/application tools by package ownership, tool namespace, and provenance — while allowing either to call the other.

#### Scenario: Author tool coexists with catalog

- **GIVEN** a navi with catalog tools and an author-registered `github_issue_read` tool
- **WHEN** the author tool is registered with namespace `github` and provenance `author`
- **THEN** the tool SHALL be discoverable and callable alongside catalog tools
- **AND** catalog and author tools SHALL be distinguishable by namespace and provenance

#### Scenario: Tool calls another tool

- **GIVEN** an author tool (`github_issue_read`) that internally reads a cached file
- **WHEN** the author tool invokes the catalog `file_read` implementation
- **THEN** the call SHALL resolve through the registry by name
- **AND** the agent SHALL see only the outermost tool call

## 04 Navi Author Workflow

```
1. go mod init github.com/<author>/<navi>
2. go get github.com/flintwillow-software/opennavi
3. cmd/<navi>/main.go → sdk.Build(naviDef{...})
   - name, version, personality, skills
   - tools: catalog tools to enable + author-registered tools
   - interfaces: CLI / Gateway / WebUI selection
4. opennavi-build → single self-contained binary (CGO_ENABLED=0)
```

Definitions are Go-only: there is no YAML definition format (S000207).

### Requirement: Minimal Authoring Path

The system SHALL allow a navi to be built from a single `main.go` entry point, yielding a working binary with the default tool set and selected interfaces.

#### Scenario: Building the minimal navi

- **GIVEN** a repository with `sdk.Build(naviDef{...})` in `cmd/<navi>/main.go` and a `go.mod` depending on the SDK
- **WHEN** the builder processes the project
- **THEN** the system SHALL produce a single self-contained binary (S000300)
- **AND** the binary SHALL include the default catalog tools and the selected interfaces

## 05 SDK Surface Guardrail

The starter templates (`templates/navi-code/` and `templates/navi-daemon/`, S000300 §11) are the executable check on this spec: CI builds both against SDK main and every release tag, so a change that breaks the public authoring surface fails the template builds in the same change cycle.

### Requirement: Template Build Guardrail
The system SHALL build the starter template against the SDK in CI, on the default branch and on every release tag.

#### Scenario: Breaking the public authoring surface
- **GIVEN** a change to the SDK that breaks a template's compilation or registration flow
- **WHEN** CI runs on the pull request
- **THEN** the template build SHALL fail
- **AND** the change SHALL NOT merge until the template is updated

## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-09-08 | S000800 | 0.2.0 | Initial SDK surface spec |
| 2026-09-13 | S000805 | 0.3.0 | Go-only authoring (removed YAML equivalence); added template-build guardrail for the public surface (S000312) |
| 2026-09-13 | S000805 | 0.3.1 | Guardrail covers the two-template set (navi-code, navi-daemon) per S000313 |