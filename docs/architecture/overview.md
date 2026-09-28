---
status: draft
created: '2026-09-24'
updated: '2026-09-28'
tags:
  - architecture
  - overview
---

# Architecture Overview

> What OpenNAVI is, how a navi is put together, and where the boundaries are.
> Design decisions are in [`docs/adr/`](../adr/); the rewrite that reached this
> state is recorded in [rewrite-plan.md](./rewrite-plan.md) and in Huly.

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

n.Skills.Add(skill.Skill{Name: "incident_response", Content: incidentResponse})
builtins, _ := builtin.Tools(workDir)
for _, t := range builtins {
    n.Tools.Add(t)
}
n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
n.Connectors.Add(cron.New(cron.Config{Schedules: schedules}))
n.Portals.Add(tui.NewPortal())
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

Public component packages and implementations are split so third-party components can be
written in their own modules without pulling in the runtime:

```
tool/                 public  Tool, Def, Handler, Schema, Access, provenance
tool/registry         public  tool registry: metadata, active set, dispatch
tool/{builtin,http,note,thoughts,time,todo}  public  the shipped tool catalog
thought/              public  Thought, thought-system handle, events, the queue
connector/            public  Connector interface + Deps (thought/model handles)
connector/cron        public  cron connector (schedules as a thought factory)
portal/               public  Portal interface + Deps, bounded observation interface
portal/gateway        public  HTTP portal gateway (observation + control framing)
portal/tui            public  terminal portal
model/                public  Model + provider descriptor
skill/                public  Skill value (embedded content)
store/                public  Store interface + implementations
navi/                 public  composition root; imports the above
internal/             private runtime: agent loop, providers, bridge, daemon, config, ops
cmd/navi/             reference navi (was the only runnable binary)
templates/            starter navi modules (guardrail)
testdata/             synthetic out-of-repo modules (CI fixtures)
```

**Dependency rule:** packages below `navi` never import `navi`. Connectors and
portals receive handles through their `Deps` interface. This keeps the graph
acyclic and components independently testable.

## Boundaries

| Boundary | Rule |
|---|---|
| Build-time vs runtime | Capability (tools, connectors, models) is compiled in; credentials, endpoints, and schedules are runtime config that may only narrow behavior |
| Compiled policy vs runtime policy | Runtime config may only tighten the compiled permission ceiling |
| Component vs implementation | Component packages are public and dependency-light; implementations are swappable |
| Repo vs tracker | Durable design lives in `docs/`; work state lives in Huly |
| Core vs extension | Core ships in the SDK; extensions are ordinary Go modules or (later) signed runtime plugins |

## Current state

The rewrite is complete (M0–M7). `navi.New` assembles typed collections,
validates and freezes them, and runs a real agent runner (`navi.AgentRunner`)
that dispatches through `tool/registry` and `internal/bridge` with a
mode-selected tool surface (ADR 0007). The catalog is ported onto `tool.Def`
collections, `model_list` exposes the declared provider set (ADR 0009), cron
ships as the `connector/cron` connector, and portals observe queue and
running-agent events through the bounded `portal.Observation` interface
(NAVI-352). Durable thoughts persist their mode.

`cmd/navi` is the reference navi: `run` assembles through `navi.New` with
durable stores, the catalog, configured providers (fallback chain and
compaction included), cron, and portals; `daemon` mode serves the HTTP portal
gateway so remote front-ends attach and control across processes. `navi-code`
and `navi-daemon` are real navi modules on the same surface, the old `sdk/`
authoring stub is retired, and the out-of-repo extension path is guarded by CI
(`testdata/external-navi`, `testdata/external-component`). The old `internal/daemon`
run path is dead code, tracked for removal (NAVI-354); transcript replay for
portals is tracked as NAVI-353.

## Related

- [runtime.md](./runtime.md) — navi object, thought system, providers, lifecycle
- [security.md](./security.md) — permissions, omission, secrets, trust
- [extension-model.md](./extension-model.md) — out-of-repo components
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones
- [`docs/adr/`](../adr/) — decision records