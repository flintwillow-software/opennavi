# OpenNavi Documentation

Design and architecture documentation for OpenNavi — an SDK for creating network assistants and virtual interfaces (NAVIS).

## Pi Cleanroom Specs

The following documents were written as cleanroom design specs for the **pi-mono** rewrite (Phase 1 core). They describe the coding agent from first principles without reference to the original TypeScript source:

| # | Document | Description |
|---|----------|-------------|
| 0 | [00-overview.md](./pi/00-overview.md) | **Master plan** — project scope, architecture, phases, dependencies, risks |
| 1 | [01-llm-provider-layer.md](./pi/01-llm-provider-layer.md) | LLM abstraction — core types, `Provider` interface, Anthropic/OpenRouter/LM Studio implementations, model registry |
| 2 | [02-agent-runtime.md](./pi/02-agent-runtime.md) | Agent runtime — state management, tool execution (parallel/sequential), event system, steering queues, agent loop |
| 3 | [03-tui-layer.md](./pi/03-tui-layer.md) | TUI framework — differential rendering, terminal interface, component system, overlays, built-in components |
| 4 | [04-coding-agent-cli.md](./pi/04-coding-agent-cli.md) | Coding agent CLI — built-in tools, CLI flags, interactive UI layout, slash commands, 4 modes (interactive, print, JSON, RPC) |
| 5 | [05-session-management.md](./pi/05-session-management.md) | Session persistence — JSONL format, branching, tree navigation, context compaction, HTML export |
| 6 | [06-configuration-settings.md](./pi/06-configuration-settings.md) | Configuration system — settings merge order, theme system, keybindings, custom models, hot-reload |
| 7 | [07-webui.md](./pi/07-webui.md) | Web UI — Go HTTP server, WebSocket hub, REST API, SSE streaming, vanilla JS frontend |

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

1. Start with **[pi/00-overview.md](./pi/00-overview.md)** for the big picture
2. Then follow the layer stack: **01 → 02 → 03** (LLM → Agent → TUI)
3. Read **04** to see how it all ties together in the CLI
4. Read **05** and **06** for persistence and configuration details
5. Read **07** for the web UI
6. Read the agent-role specs and multi-agent specs for autonomous architecture
7. Read **spec/README.md** for the full OpenNavi architecture specs

## Key Conventions

- **Config directory:** `~/.opennavi/` (not `~/.pi/`)
- **Project config:** `.opennavi/` (not `.pi/`)
- **Binary name:** `opennavi`
- **TUI:** Built from scratch (no framework), differential rendering
- **Providers (Phase 1):** Anthropic, OpenRouter, LM Studio
