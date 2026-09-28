# OpenNAVI

> An SDK for creating **n**etwork **a**ssistants and **v**irtual **i**nterfaces — compiled AI agent binaries distributed as single executable files.

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Overview

OpenNAVI is a Go SDK for building **navis** — self-contained AI agent
binaries. A navi is defined in Go, compiled to a single executable, and runs
standalone: `go build` links exactly what the navi imports, so the binary's
module graph is the record of what it can do.

A navi is a **composition root**. You declare its capabilities in typed
collections and call `Run`:

```go
n := navi.New("oncall-bot",
    navi.WithVersion("1.0.0"),
    navi.WithPersonality("You are an always-on operator's assistant."),
)

n.Skills.Add(skill.Skill{
    Name: "triage", Description: "Triage an alert.",
    Content: "Orient, then act in small verifiable steps.",
})
n.Tools.Add(issueReadTool())                 // a tool.Tool descriptor + handler
n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
n.Connectors.Add(cron.New(cron.Config{...})) // inbound work as a thought factory
n.Portals.Add(tui.NewPortal())

return n.Run(ctx) // validate, freeze, assemble, serve
```

The SDK provides:

- **Composition root** (`navi/`) — typed collections for skills, tools,
  models, connectors, and portals, with eager validation and freeze-on-`Run`.
- **Component packages** (`tool`, `thought`, `connector`, `portal`, `model`,
  `skill`) — the dependency-light public surface components and navis are
  written against.
- **Thought-based runtime** — the navi's main loop is the thought system
  (`thought/`): a durable priority queue with scheduling, preemption,
  park-with-checkpoint, and crash recovery. Every door (CLI, connectors, tools)
  feeds the same queue, and each thought executes through a pluggable `Runner`.
- **Agent loop** (`internal/agent`) — the shipped `Runner`: the think → act →
  observation loop with streaming, tool execution, and context-window
  compaction, run once per thought. `navi.AgentRunner` bridges it to the
  thought system.
- **Provider layer** (`internal/llm`) — Ollama and OpenRouter with a fallback
  chain.
- **Session store** (`store/`, `store/sqlite/`) — conversations, thoughts, and
  memory in one SQLite database.
- **Tool system** (`tool/`) — the `Tool` type, the registry and bridge, and
  the shipped catalog: builtins (`file_read`, `shell_exec`) plus the todo,
  note, time, http, and thoughts tools.
- **Starter modules** (`templates/`) — `navi-code` (a CLI coding agent) and
  `navi-daemon` (an always-on daemon).

## Architecture

The architecture is documented in [`docs/architecture/`](./docs/architecture/):

| Document | Covers |
|----------|--------|
| [Overview](./docs/architecture/overview.md) | What a navi is, C4 context, package layout, boundaries |
| [Runtime](./docs/architecture/runtime.md) | The navi object, collections, thought system, modes, providers, lifecycle |
| [Memory](./docs/architecture/memory.md) | Short-term (always in context) vs long-term (searched) memory |
| [Security](./docs/architecture/security.md) | Permission ceiling, compile-time omission, secrets, trust |
| [Extension model](./docs/architecture/extension-model.md) | Out-of-repo tools/skills/models/connectors/portals and navis |
| [Rewrite plan](./docs/architecture/rewrite-plan.md) | Migration milestones (M0–M7) |

Design decisions are recorded as immutable records in [`docs/adr/`](./docs/adr/).
Work and planning are tracked in Huly; durable design lives in this repo.

## Repository Structure

```
opennavi/
├── cmd/navi/       # reference navi binary (run, enqueue, health)
├── navi/           # composition root
├── tool/ thought/ connector/ portal/ model/ skill/   # public component packages
├── tool/           # the tool system: Tool type, registry, catalog
├── store/          # public session store interface + SQLite implementation
├── internal/       # runtime: agent loop, providers, bridge, daemon, config
├── templates/      # starter navi modules (CI guardrail)
└── docs/
    ├── architecture/   # current architecture
    └── adr/            # decision records
```

## Development

```bash
just build   # compile the reference navi → var/bin/navi
just test    # unit tests
just ci      # build + vet + gofmt-check + race tests (same gates as CI)
```

## Contributing

Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

MIT — see [LICENSE](LICENSE).