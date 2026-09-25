---
status: draft
created: '2026-09-24'
updated: '2026-09-24'
tags:
  - architecture
  - overview
---

# Architecture Overview

> What OpenNAVI is, how a navi is put together, and where the boundaries are.
> This document describes the intended architecture. Design decisions are in
> [`docs/adr/`](../adr/); the rewrite that reaches this state is tracked in
> [rewrite-plan.md](./rewrite-plan.md) and in Huly.

## What it is

OpenNAVI is an **SDK for building navis** — self-contained AI agent binaries. A
navi author creates their own Go repository, imports the SDK, composes a navi
from tools, skills, models, connectors, and portals, and runs it. The result is
a single statically linked binary whose entire capability surface is visible in
its source (`main.go`) and dependency record (`go.mod`).

The thought system is the runtime's main loop and the thing that distinguishes a
navi from a generic agent harness. Every door — a chat connector, a cron
trigger, a gateway webhook, a human portal — feeds the same thought queue.

## C4 — Context

```
                    ┌─────────────────────────────────────────────┐
   operator ───────▶│                                             │
   (TUI / gateway)  │                  A NAVI BINARY              │
                    │                                             │
   chat platforms ─▶│  portals ─┐                                 │
   (Discord, etc.)  │           ├──▶ thought system ◀── connectors│◀─ webhooks,
                    │  human ───┘        │                (cron)  │   cron
                    │                   ▼                         │
   LLM providers ◀──┤              agent loop ──▶ tools           │
   (Ollama,         │                   │       (file, shell,     │
    OpenRouter)     │                   ▼        http, domain)   │
                    │              session store                  │
                    └─────────────────────────────────────────────┘
```

- **Portals** are human-facing observation and control surfaces (CLI, TUI,
  WebUI). They list, schedule, and steer thoughts.
- **Connectors** are transport integrations that bring work in (gateway
  webhooks, cron schedules, Discord/Slack/MQTT/email adapters). They manage
  thought lifecycles.
- **Tools** are agent-callable actions the navi invokes, including egress tools
  that send messages back out through a connector (`discord_send`).
- **Providers** are LLM backends selected per thought.
- **Store** persists sessions, thoughts, and memory.

## The navi object

A navi is one composition root with typed collections:

```go
n := navi.New("oncall-bot",
    navi.WithVersion("1.0.0"),
    navi.WithPersonality(onCallPersona),
)

n.Skills.Add(skills.IncidentResponse)
n.Tools.Add(tools.TodoRead(store))
n.Tools.Add(tools.TodoUpdate(store))
n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
n.Connectors.Add(connectors.Cron())
n.Portals.Add(portals.TUI())
n.Modes.Add(navi.PlanMode)

if err := n.Run(ctx); err != nil {
    log.Fatal(err)
}
```

- **Thoughts** is always present; it is core, not an `.Add()`.
- The other collections are populated at build time (compile-in, Go-only).
- `Run` freezes the collections, resolves ordering, assembles, and serves.

See [runtime.md](./runtime.md) for the full model.

## Package layout

Public contract and implementation are split so third-party components can be
written in their own modules without pulling in the runtime:

```
tool/          public  Tool, Def, Handler, Schema, Access, provenance
thought/       public  Thought, thought-system handle, events
connector/     public  Connector interface + Deps (thought/memory/model handles)
portal/        public  Portal interface + Deps
model/         public  Model + provider descriptor
skill/         public  Skill value (embedded content)
navi/          public  composition root; imports the above
agent/         public  agent loop, events, tool executor
llm/           public  Provider interface + adapters
store/         public  Store interface + implementations
internal/      private queue mechanics, compaction, metrics, CLI plumbing
cmd/navi/      reference navi (was the only runnable binary)
templates/     starter navi modules (guardrail)
```

**Dependency rule:** packages below `navi` never import `navi`. Connectors and
portals receive handles through their `Deps` contract. This keeps the graph
acyclic and components independently testable.

## Boundaries

| Boundary | Rule |
|---|---|
| Build-time vs runtime | Capability (tools, connectors, models) is compiled in; credentials, endpoints, and schedules are runtime config that may only narrow behavior |
| Compiled policy vs runtime policy | Runtime config may only tighten the compiled permission ceiling |
| Contract vs implementation | Contract packages are public and dependency-light; implementations are swappable |
| Repo vs tracker | Durable design lives in `docs/`; work state lives in Huly |
| Core vs extension | Core ships in the SDK; extensions are ordinary Go modules or (later) signed runtime plugins |

## Current state

This is a design in migration. Today the working runtime is the `cmd/navi`
monolith plus `internal/daemon.Assemble`; the navi object and the public
contract packages do not exist yet, and `sdk/` is a validation stub. The
milestones that close the gap are in [rewrite-plan.md](./rewrite-plan.md).

## Related

- [runtime.md](./runtime.md) — navi object, thought system, providers, lifecycle
- [security.md](./security.md) — permissions, omission, secrets, trust
- [extension-model.md](./extension-model.md) — out-of-repo components
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones
- [`docs/adr/`](../adr/) — decision records