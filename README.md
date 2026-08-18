# OpenNAVI

> An SDK for creating **n**etwork **a**ssistants and **v**irtual **i**nterfaces — compiled AI agent binaries distributed as single signed executable files.

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Overview

OpenNAVI is an SDK for building **navis** — self-contained AI agent binaries. Each navi is a single compiled executable with all dependencies built in, signed for verifiable provenance. The SDK provides:

- **Navi definition format** — declare your agent's tools, providers, identity, and trust config in `navi.yaml`
- **Builder/compiler** — produce a signed, platform-specific binary from a navi project
- **Agent runtime** — think → act → observation loop with session management and tool execution
- **Multi-agent coordination** — P2P chat, presence, identity, IAM, and team-of-teams orchestration
- **Messaging gateway** — async message delivery via webhooks, cron, email, SMS, MQTT
- **Tool catalog** — file ops, git, shell, search, test runner — compiled into every binary
- **Role system** — pre-built agent roles (PM, Support, Backend, Frontend, DevOps, SRE, QA, Reviewer) for autonomous team operation
- **LLM provider layer** — local (Ollama, LM Studio) and cloud (Anthropic, OpenAI, OpenRouter)

## Spec Tree

The full architecture is documented as an ordered specification tree:

| Component | Description |
|-----------|-------------|
| [00 — Navi Core](./docs/specs/00-navi-core/README.md) | Platform foundation: navi.yaml definition, builder, interfaces, trust, extensions |
| [01 — Agent Runtime](./docs/specs/01-agent-runtime/README.md) | Agent loop, thought queue, tool execution, error recovery |
| [02 — Agent Config](./docs/specs/02-agent-config/README.md) | Skills and persona system |
| [03 — Session Context](./docs/specs/03-session-context/README.md) | Session persistence, context window management, webhook schemas |
| [04 — Multi-Agent](./docs/specs/04-multi-agent/README.md) | P2P chat, presence, IAM, idle scheduling, team-of-teams |
| [05 — Messaging](./docs/specs/05-messaging/README.md) | Gateway, platform adapters, unified message protocol |
| [06 — Memory](./docs/specs/06-memory/README.md) | Four-tier memory architecture |
| [07 — LLM Provider](./docs/specs/07-llm-provider/README.md) | Provider abstraction, streaming, token counting |
| [08 — Operations](./docs/specs/08-operations/README.md) | Logging, metrics, health, retry, circuit breaker, API versioning |
| [10 — Integrations](./docs/specs/10-integrations/README.md) | External API adapter framework |
| [13 — Tool Catalog](./docs/specs/13-tool-catalog/README.md) | Built-in tool set: file, shell, git, search, code intel |
| [14 — Agent Roles](./docs/specs/14-agent-roles/README.md) | Role definitions for autonomous team agents |

See the [full spec index](./docs/specs/README.md) for conventions and the [change log](./docs/changes/README.md) for active and completed work.

## Repository Structure

```
opennavi/
├── cmd/            # CLI entry points (builder, agent binaries)
├── pkg/            # SDK packages (runtime, tools, roles, messaging, etc.)
├── docs/
│   ├── specs/      # Ordered specification tree
│   ├── changes/    # Versioned change proposals
│   ├── templates/  # Spec-tree and change skeletons
│   └── ...         # Additional documentation
├── .pi/skills/     # d3-* skills for the pi coding agent
└── scripts/        # Automation and CI scripts
```

## Development

```bash
# Build the SDK
go build ./...

# Run tests
go test ./...

# Run spec validation
./scripts/specs-validate.sh
```

## Contributing

Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

MIT