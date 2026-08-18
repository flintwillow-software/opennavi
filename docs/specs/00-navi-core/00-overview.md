---
status: draft
created: '2026-07-29'
updated: '2026-07-29'
tags:
  - spec
  - navi-core
priority: high
spec-refs: [000000, 000001, 000002, 000003, 000004, 000005, 000006, 000007, 000008]
---

# OpenNavi — Compiled Agent Platform

**Status:** Draft  
**Last updated:** 2026-07-29

---

## 00 Vision

OpenNavi is a platform for building **navis** — self-contained AI agent binaries that ship as single executable files. A navi is not an SDK you run, and it's not a collection of unsigned scripts loaded at runtime. It's a compiled program with verifiable provenance: you download it, verify its signature, and trust what it does because the binary itself is the trust boundary.

This flips the current agent distribution model on its head. Instead of shipping unsigned JavaScript packages that require Node.js runtimes and entire dependency trees, opennavi produces signed binaries that contain everything they need to operate — tools, skills, personality, delivery interfaces — all compiled and verified.

### Requirement: Compiled Agent Binary
The system SHALL produce a single executable binary for each navi that includes all tools, skills, personality, interfaces, and permissions.

#### Scenario: Building a navi produces a single binary
- **GIVEN** a developer has written a navi definition with tools, skills, personality, and interfaces
- **WHEN** the developer runs the opennavi builder
- **THEN** the builder SHALL produce a single executable binary containing all defined components

### Requirement: Verifiable Provenance
The system SHALL enable signature verification of the navi binary before execution, establishing the binary itself as the trust boundary.

#### Scenario: Verifying a navi binary before execution
- **GIVEN** a user has downloaded a navi binary
- **WHEN** the user verifies the binary's signature
- **THEN** the system SHALL confirm the signature matches the developer's key
- **AND** the user SHALL be able to trust the binary's contents without external dependencies

---

## 01 What is a Navi?

A navi is defined by seven components:

| Component | Description | Compiled Into Binary? |
|-----------|-------------|----------------------|
| **Name** | Identifier for the agent | Yes |
| **Tools** | Capabilities the agent can execute (file ops, API calls, shell commands) | Yes — implementations compiled in |
| **Skills** | Role-specific instructions that shape how the agent behaves | Yes — baked into system prompt at build time |
| **Personality** | How the navi communicates and thinks (tone, style, reasoning patterns) | Yes — part of navi definition |
| **Interfaces** | Delivery modes: CLI/TUI chat, web UI, webhook server, platform adapters (Telegram, Discord), cron scheduler | Yes — only selected interfaces are compiled in |
| **Permissions** | IAM-style policies defining what tools and paths the agent can access | Yes — part of the verifiable trust boundary |
| **Extensions** | Optional runtime plugins that augment functionality without rebuilding the binary | No — separate binaries loaded at runtime |
| **Thought Queue** | Optional priority queue for daemon navis (gateway/cron interfaces) | Config, compiled in |

A developer writes a navi definition, runs the opennavi builder, and gets back a single executable. That's it.

### Requirement: Navi Component Definition
The system SHALL define a navi by seven components: Name, Tools, Skills, Personality, Interfaces, Permissions, and Extensions.

#### Scenario: Defining a navi with all components
- **GIVEN** a developer is creating a navi definition
- **WHEN** the developer specifies all seven components
- **THEN** the system SHALL accept the definition and produce a valid navi binary

### Requirement: Component Compilation
The system SHALL compile Name, Tools, Skills, Personality, Interfaces, and Permissions into the navi binary at build time.

#### Scenario: Compiling components into the binary
- **GIVEN** a navi definition includes tools, skills, personality, interfaces, and permissions
- **WHEN** the builder compiles the navi
- **THEN** all those components SHALL be embedded in the resulting binary
- **AND** no runtime loading of those components SHALL occur

### Requirement: Extension Runtime Loading
The system SHALL load Extensions as separate binaries at runtime, not compiled into the navi binary.

#### Scenario: Loading an extension at runtime
- **GIVEN** a navi binary is running
- **WHEN** the system discovers an extension in the extension directory
- **THEN** the extension SHALL be loaded as a separate binary at runtime

---

## 02 The Trust Model

The trust chain is transparent:

```
Developer signs their navi binary
    ↓ (verified by end user before execution)
End user trusts the signature → runs the binary
    ↓ (extensions loaded in sandboxed processes)
Extensions are signed binaries with their own provenance
```

**What gets verified:**
- The navi binary's signature matches the developer's key
- Extensions have valid signatures from trusted sources
- No unsigned code runs in the main process

**What doesn't need verification:**
- The agent runtime itself — it's part of the compiled binary
- Tool implementations — they're compiled into the binary, not loaded at runtime
- Skills and personality — text/config baked in at build time

This is similar to how Terraform handles providers: one signed binary (terraform), with provider plugins that are also signed binaries. The trust chain is explicit and verifiable.

### Requirement: Binary Signature Verification
The system SHALL verify the navi binary's signature matches the developer's key before execution.

#### Scenario: Verifying a signed binary
- **GIVEN** a user has a navi binary and its signature file
- **WHEN** the user runs signature verification
- **THEN** the system SHALL confirm the binary's signature matches the developer's published key
- **AND** the system SHALL report whether the signature is valid or invalid

### Requirement: Extension Signature Verification
The system SHALL verify extensions have valid signatures from trusted sources before loading.

#### Scenario: Loading a signed extension
- **GIVEN** a navi discovers an extension binary
- **WHEN** the navi attempts to load the extension
- **THEN** the system SHALL verify the extension's signature against the allowed signers list
- **AND** the system SHALL reject the extension if the signature is invalid or the signer is not allowed

### Requirement: Unsigned Code Restriction
The system SHALL prevent unsigned code from executing in the main process.

#### Scenario: Attempting to load unsigned code
- **GIVEN** a navi is running
- **WHEN** an unsigned script or binary is loaded
- **THEN** the system SHALL reject the unsigned code
- **AND** the system SHALL log the rejection

---

## 03 Distribution

A navi is a single executable file. It can be distributed through any method that works for compiled binaries:

- **Direct download** — from the developer's website
- **Package managers** — Homebrew, Flatpak, AppImage (desktop), Docker (server)
- **Private registries** — internal binary repositories for enterprise teams
- **Email/IM** — literally attach the binary to a message (with verification)

No Node.js runtime. No `npm install`. No downloading arbitrary JS code into your process. Just one signed executable that does what it was compiled to do.

### Requirement: Single Binary Distribution
The system SHALL distribute navis as single executable files that require no runtime dependencies.

#### Scenario: Distributing a navi binary
- **GIVEN** a developer has built a navi binary
- **WHEN** the developer distributes the binary via any method
- **THEN** the binary SHALL require no runtime dependencies such as Node.js, npm, or SDK libraries
- **AND** the binary SHALL be executable immediately after download and verification

---

## 04 Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│              OpenNavi SDK (developer tooling)        │
│  Builder │ Compiler │ Interface selection            │
└────────────────────────┬────────────────────────────┘
                         │ compiles to
                         ▼
┌─────────────────────────────────────────────────────┐
│                    Navi Binary                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │  Tools   │  │ Skills   │  │ Interfaces       │  │
│  │ (compiled)│  │(baked in)│  │ (selected at     │  │
│  │          │  │          │  │  build time)      │  │
│  └──────────┘  └──────────┘  └──────────────────┘  │
│                                                      │
│  ┌─────────────────────────────────────────────┐    │
│  │          Agent Runtime (compiled in)         │    │
│  │  LLM Provider Layer → Session Management     │    │
│  │  ┌─────────────┐  ┌──────────────────────┐   │    │
│  │  │Thought Queue │  │ ChatSession          │   │    │
│  │  │(daemon navis)│  │(council, delegation)│   │    │
│  │  └─────────────┘  └──────────────────────┘   │    │
│  │  Agent Loop → Tool Execution                 │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
                         ↕ (optional, sandboxed)
┌─────────────────────────────────────────────────────┐
│                  Extensions                          │
│  Standalone binaries loaded at runtime              │
│  Signed by their authors                            │
│  Implement the Extension Contract                   │
└─────────────────────────────────────────────────────┘
```

### Two Execution Models

| Model | Where Used | Purpose |
|-------|-----------|---------|
| **Thought Queue** | Long-running (daemon) navis | Priority scheduling, crash recovery, per-thought session logs |
| **ChatSession** | Council mode, one-off delegation | Short-lived multi-agent coordination |

### Interfaces (Delivery Modes)

A navi can be compiled with any combination of interfaces. Only selected interfaces are included in the binary:

| Interface | Description | Use Case |
|-----------|-------------|----------|
| **CLI/TUI** | Interactive terminal chat with differential rendering | Developer tools, manual interaction |
| **Web UI** | HTTP server + WebSocket chat interface | Remote access, multi-user |
| **Gateway** | Webhook receiver + message routing | Event-driven automation |
| **Cron** | Scheduled task execution | Periodic monitoring, health checks |
| **Telegram** | Telegram bot adapter | Chat-based interaction |
| **Discord** | Discord bot adapter | Team collaboration |
| **Slack** | Slack bot adapter | Enterprise integration |

A single navi can have multiple interfaces. `oncall-bot` might ship with CLI (for manual triage) + Gateway + Cron (for automated monitoring). End users get one binary that does both.

### Requirement: Interface Selection
The system SHALL compile only selected interfaces into the navi binary based on the navi definition.

#### Scenario: Selecting interfaces at build time
- **GIVEN** a navi definition specifies a subset of interfaces
- **WHEN** the builder compiles the navi
- **THEN** only the specified interfaces SHALL be included in the binary
- **AND** unselected interfaces SHALL NOT be present in the binary

### Requirement: Agent Runtime
The system SHALL include an agent runtime in every navi binary that provides LLM provider integration, session management, and tool execution.

#### Scenario: Running the agent runtime
- **GIVEN** a navi binary is executed
- **WHEN** the navi starts
- **THEN** the agent runtime SHALL be available for LLM provider communication, session management, and tool execution

### Requirement: Two Execution Models
The system SHALL support both Thought Queue (for daemon navis) and ChatSession (for council mode) execution models.

#### Scenario: Using Thought Queue for daemon mode
- **GIVEN** a navi is configured as a daemon with Gateway or Cron interfaces
- **WHEN** the navi receives multiple incoming events
- **THEN** the Thought Queue SHALL prioritize and schedule the events

#### Scenario: Using ChatSession for council mode
- **GIVEN** a navi is operating in council mode
- **WHEN** multiple agents coordinate on a task
- **THEN** the ChatSession SHALL manage the short-lived multi-agent coordination

---

## 05 Relationship to pi-mono

OpenNavi is a Go rewrite of [badlogic/pi-mono](https://github.com/badlogic/pi-mono), but with a fundamentally different distribution model:

| Aspect | pi-mono | OpenNavi Navi |
|--------|---------|---------------|
| Distribution | npm package (unsigned JS) | Single compiled binary (signed) |
| Runtime dependency | Node.js + npm dependencies | None — everything compiled in |
| Trust boundary | Entire dependency tree | Signed binary + verified extensions |
| Delivery modes | TUI only (core), separate webui package | Any combination, compiled into one binary |
| Plugin model | Python/JS plugins loaded at runtime | Compiled tools, optional signed standplugin extensions |

The core architecture (LLM layer, agent loop, session management) is the same. The difference is how it's packaged and distributed: **compiled, verifiable, self-contained**.

### Requirement: Self-Contained Distribution
The system SHALL produce navis that require no runtime dependencies, unlike pi-mono's npm-based distribution.

#### Scenario: Comparing distribution models
- **GIVEN** a navi binary is built
- **WHEN** compared to pi-mono's npm package distribution
- **THEN** the navi SHALL not require any runtime dependency installation
- **AND** the navi SHALL be a single executable file

### Requirement: Trust Boundary
The system SHALL establish the signed binary as the trust boundary, unlike pi-mono's dependency-tree-based trust model.

#### Scenario: Comparing trust boundaries
- **GIVEN** a navi binary is signed
- **WHEN** compared to pi-mono's trust model
- **THEN** the trust boundary SHALL be the signed binary and verified extensions
- **AND** the system SHALL NOT require trust in a dependency tree

---

## 06 Reading Order

1. **[00-overview.md](./00-overview.md)** — This document: vision, trust model, architecture
2. **[01-navi-project-layout.md](./01-navi-project-layout.md)** — Go project conventions and directory structure
3. **[02-navi-definition.md](./02-navi-definition.md)** — What a developer writes to define a navi
4. **[03-builder-compiler.md](./03-builder-compiler.md)** — How the SDK compiles navis into binaries
5. **[04-interface-set.md](./04-interface-set.md)** — Delivery modes and interface selection
6. **[05-trust-signing-model.md](./05-trust-signing-model.md)** — Code signing, verification flow
7. **[06-extension-contract.md](./06-extension-contract.md)** — Standalone plugins for runtime augmentation

**Core execution model:**
8. **[Agent Runtime](../01-agent-runtime/03-agent-runtime.md)** — Agent loop, tool execution, events
9. **[Thought Queue](../01-agent-runtime/00-thought-queue.md)** — Thought queue (daemon navis) + workers & navi communication

**Core SDK specs (compiled into the navi binary):**
10. **[Webhook Response Schema](../03-session-context/00-webhook-response-schema.md)** — Webhook delivery formats and response patterns
11. **[Session Management](../03-session-context/01-session-management.md)** — Persistence, branching, compaction
12. **[Skills System](../02-agent-config/00-skills-system.md)** — Role-specific instructions (baked in at build)
13. **[Council Termination](../04-multi-agent/05-council-termination.md)** — Council mode termination conditions
14. **[Persona System](../02-agent-config/01-persona-system.md)** — Personality configuration
15. **[Multi-Agent System](../04-multi-agent/06-multi-agent-system.md)** — Sub-agents, council mode via ChatSession
16. **[Memory System](../06-memory/00-memory-system.md)** — Four-tier memory

**Inter-Agent Communication:**
17. **[P2P Chat & Presence](../04-multi-agent/00-p2p-chat-presence.md)** — P2P chat model, presence detection, public key trust, IAM chat permissions, idle scheduling, and unified identity (chat keys + git commit signing)

---

## 07 Use Cases

### 1. Project Management Agent (Hermes-style autonomous)
A navi that monitors GitHub issues, creates tasks via sub-agents, tracks status on project boards, and responds to webhooks from CI/CD systems. Runs as a daemon with Gateway + Cron interfaces. Can be interacted with manually via CLI/TUI for planning conversations.

### 2. On-Call Engineer
A navi that receives Grafana/BetterStack alerts via webhook, performs initial triage, queries metrics, and escalates to humans when needed. Runs in autonomous mode with Telegram/Discord delivery. Manual chat available for incident post-mortems.

### 3. Home Assistant / Digital Assistant
A navi that responds to Home Assistant events, controls IoT devices, and maintains personality/preferences across interactions. Runs as a daemon with webhook interface. Can be extended with optional standplugin tools for specific device types.

### Requirement: Use Case Support
The system SHALL support autonomous daemon navis, on-call engineer navis, and home assistant navis as valid use cases.

#### Scenario: Running a daemon navi
- **GIVEN** a developer has built a navi with Gateway and Cron interfaces
- **WHEN** the navi is deployed as a daemon
- **THEN** the navi SHALL receive webhooks and execute scheduled tasks autonomously

#### Scenario: Running an on-call engineer navi
- **GIVEN** a developer has built a navi with alert monitoring tools and Telegram/Discord delivery
- **WHEN** an alert is received
- **THEN** the navi SHALL perform initial triage and deliver responses to configured platforms

---

## 08 Key Design Decisions

1. **Compiled binaries, not runtime loading** — Tools and skills are compiled into the navi at build time. No JS/Python plugins loaded at runtime (except optional extensions).
2. **Interface selection at build time** — Only selected delivery modes get compiled in. Smaller binary, fewer attack surface.
3. **Extensions as signed standplugins** — Optional runtime augmentations are separate binaries with their own trust model. Sandboxed from the main process.
4. **Signature verification before execution** — Users verify the navi's signature before running. Extensions verified before loading.
5. **Single binary distribution** — No Node.js, no npm, no dependency trees. One executable that does everything it needs to do.


## Status History

| Date | S-Code | Version | Change |
|------|--------|---------|--------|
| 2026-08-13 | S000000 | 0.1.0 | Migrated from docs/spec/ to spec-tree |