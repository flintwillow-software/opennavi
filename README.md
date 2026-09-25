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

## Architecture

The architecture is documented in [`docs/architecture/`](./docs/architecture/):

| Document | Covers |
|----------|--------|
| [Overview](./docs/architecture/overview.md) | What a navi is, C4 context, package layout, boundaries |
| [Runtime](./docs/architecture/runtime.md) | The navi object, collections, thought system, modes, providers, lifecycle |
| [Security](./docs/architecture/security.md) | Permission ceiling, compile-time omission, secrets, trust |
| [Extension model](./docs/architecture/extension-model.md) | Out-of-repo tools/skills/models/connectors/portals and navis |
| [Rewrite plan](./docs/architecture/rewrite-plan.md) | Migration milestones (M0–M7) |

Design decisions are recorded as immutable records in [`docs/adr/`](./docs/adr/).
Work and planning are tracked in Huly; durable design lives in this repo.

## Repository Structure

```
opennavi/
├── cmd/navi/       # reference navi binary (run, enqueue, health)
├── agent/          # public agent loop and tool executor
├── llm/            # public provider layer (Ollama, OpenRouter, fallback)
├── store/          # public session store interface + SQLite implementation
├── tools/          # public tool registry and catalog
├── sdk/            # authoring surface (being replaced by the navi object)
├── internal/       # runtime implementation (daemon, thought queue, interfaces)
├── templates/      # starter navi modules (CI guardrail)
└── docs/
    ├── architecture/   # current architecture
    └── adr/            # decision records
```

## Development

```bash
just build   # compile the reference navi → bin/navi
just test    # unit tests
just ci      # build + vet + gofmt-check + race tests (same gates as CI)
```

## Contributing

Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

MIT