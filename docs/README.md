# OpenNavi Documentation

Design and architecture documentation for OpenNavi — an SDK for creating network assistants and virtual interfaces (NAVIS).

## Core Agent Specs

The following spec components define the core OpenNavi agent platform — the agent runtime, LLM provider abstraction, session/context management, config/persona, memory, and operations infrastructure that every navi depends on:

| Component | Description |
|-----------|-------------|
| [00-navi-core/](./specs/00-navi-core/README.md) | Platform foundation: vision, definition format, builder, interfaces, trust, extensions |
| [01-agent-runtime/](./specs/01-agent-runtime/README.md) | Core execution: agent loop, thought queue, tool execution, error recovery |
| [02-agent-config/](./specs/02-agent-config/README.md) | Skills and persona system (baked in at build time) |
| [03-session-context/](./specs/03-session-context/README.md) | Session persistence, context window management, webhook schemas |
| [06-memory/](./specs/06-memory/README.md) | Four-tier memory architecture with recall |
| [07-llm-provider/](./specs/07-llm-provider/README.md) | LLM abstraction with streaming, token counting, provider registry |
| [08-operations/](./specs/08-operations/README.md) | Cross-cutting concerns: logging, metrics, health, retry, circuit breaker, rate limiting, API versioning |
| [13-tool-catalog/](./specs/13-tool-catalog/README.md) | Built-in tool set: file ops, shell, git, search, test runner, code intel |

## Autonomous Agent Specs

The following documents describe the autonomous agent platform capabilities beyond the core coding agent, including the multi-agent team model for autonomous SDLC and operations:

| Document | Description |
|----------|-------------|
| [04-multi-agent/](./specs/04-multi-agent/README.md) | Council, sub-agents, P2P chat, presence, idle scheduling, team-of-teams |
| [05-messaging/](./specs/05-messaging/README.md) | Gateway, platform adapters, unified message protocol, hooks, cron |
| [14-agent-roles/](./specs/14-agent-roles/README.md) | Role-based agent definitions (PM, Support, Lead, Backend, Frontend, DevOps, SRE, QA, Reviewer) |
| [Case Study](../../docs/case-study/autonomous-saas-product-lifecycle/README.md) | Autonomous navi team managing SDLC and operations |

## Architecture Specs

The `specs/` directory contains the full OpenNavi architecture specifications, organized into numbered feature components:

| Group | Description |
|-------|-------------|
| [00-navi-core/](./specs/00-navi-core/README.md) | Platform foundation: vision, definition format, builder, interfaces, trust, extensions |
| [01-agent-runtime/](./specs/01-agent-runtime/README.md) | Core execution: agent loop, thought process, tool error recovery |
| [02-agent-config/](./specs/02-agent-config/README.md) | Skills and persona (baked in at build time) |
| [03-session-context/](./specs/03-session-context/README.md) | Session persistence, context window, webhook schemas |
| [04-multi-agent/](./specs/04-multi-agent/README.md) | Council, sub-agents, P2P chat, presence, idle scheduling |
| [05-messaging/](./specs/05-messaging/README.md) | Gateway, platform adapters, unified message protocol |
| [06-memory/](./specs/06-memory/README.md) | Four-tier memory architecture |
| [07-llm-provider/](./specs/07-llm-provider/README.md) | LLM abstraction, streaming, token counting |
| [08-operations/](./specs/08-operations/README.md) | Cross-cutting concerns, deployment, autonomous mode, API versioning |

## Case Study

| Document | Description |
|----------|-------------|
| [Autonomous SaaS Product Lifecycle](./case-study/autonomous-saas-product-lifecycle/README.md) | Autonomous navi team managing SDLC and operations |

## Reading Order

For a new contributor, read in this order to build up the mental model incrementally:

1. Start with **[00-navi-core/00-overview.md](./specs/00-navi-core/00-overview.md)** for the big picture
2. Then follow: **navi-core → agent-runtime → session-context** (definition → execution → persistence)
3. Read **llm-provider** and **tool-catalog** for the provider and tool layers
4. Read **memory** and **operations** for cross-cutting capabilities
5. Read **agent-roles** and **multi-agent** for autonomous team architecture
6. Read **spec/README.md** for the full spec index

## Key Conventions

- **Config directory:** `~/.opennavi/` (not `~/.pi/`)
- **Project config:** `.opennavi/` (not `.pi/`)
- **Binary name:** `opennavi`
- **TUI:** Built from scratch (no framework), differential rendering
- **Providers (Phase 1):** Anthropic, OpenRouter, LM Studio
