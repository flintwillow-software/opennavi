---
status: draft
created: '2026-09-24'
updated: '2026-09-30'
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
                    │          datastore (async)                │
                    └─────────────────────────────────────────────┘
```

- **Portals** are human-facing observation and control surfaces (CLI, TUI,
  WebUI). They list, schedule, and steer thoughts.
- **Connectors** are **bidirectional service integrations** (gateway webhooks,
  cron schedules, Discord/Slack/MQTT/email adapters, GitHub). Inbound they turn
  external stimuli into thoughts and manage their lifecycles; outbound they
  supply the tools that act on that service. See ADR 0014.
- **Tools** are agent-callable actions the navi invokes, including egress tools
  that send messages back out through a connector (`discord_send`).
- **Providers** are LLM backends selected per thought.
- **Datastores** are persistence backends (`fs` markdown, `sqlite`, …). RAM is
  the source of truth; the backend is eventual and off the read path (ADR 0011).
  Portals read history through the thought system, never through the datastore.

## The navi object

A navi is one composition root with typed collections:

```go
n := navi.New("oncall-bot",
    navi.WithVersion("1.0.0"),
    navi.WithPersonality(onCallPersona),
)

// One datastore backs thought state, transcripts, tool state, and memory.
// Candidates are declared here; runtime config selects the active one — `fs`
// (markdown + frontmatter, below a per-navi root) is the compiled default,
// `inmem` the non-durable reference.
n.Datastores.Add(fs.CandidateAt("~/.opennavi/oncall-bot"))
ds := n.Datastores.Handle()

n.Skills.Add(skill.Skill{Name: "incident_response", Content: incidentResponse})
builtins, _ := builtin.Tools(workDir)
for _, t := range builtins {
    n.Tools.Add(t)
}
n.Tools.Add(todo.Tools(naviID, ds.Stateful()))
n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
n.Connectors.Add(cron.New(cron.Config{Schedules: schedules}, cron.WithDatastore(ds)))
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
datastore/            public  datastore contract + data types (metadata/detail aggregates)
datastore/conformance public  the shared backend conformance suite an implementation runs
datastore/fs          public  markdown + frontmatter backend
datastore/sqlite      public  SQLite backend (thoughts, transcripts, stateful state)
navi/                 public  composition root; imports the above
internal/             private runtime: agent loop, providers, bridge, config, ops
cmd/navi/             reference navi (was the only runnable binary)
templates/            starter navi modules (guardrail)
internal/testdata/    synthetic out-of-repo modules (CI fixtures)
```

**Dependency rule:** packages below `navi` never import `navi`. Connectors and
portals receive handles through their `Deps` interface. This keeps the graph
acyclic and components independently testable. The seven component packages
(`tool`, `thought`, `connector`, `portal`, `model`, `skill`, `datastore`) are additionally
**stdlib-only** — their import closure reaches no `internal/*` package and no
tool-system implementation package — so importing one never drags the runtime
in. Two CI guards pin this: `external-consumer` asserts contract-purity of the
external component's whole closure (NAVI-355), and *public packages import no
runtime internals* asserts the rule across every public package (NAVI-356).
`datastore` is itself a public package (data types, their interfaces, in-memory
helpers; the durable implementations are `datastore/fs` and `datastore/sqlite`),
so `tool/{note,todo,registry} → datastore` is public→public. The contract also
ships the **shared backend conformance suite** (`datastore/conformance`): one
suite every backend runs, importable by an implementation in its own module —
`internal/testdata/external-backend` is the fixture that proves it, and the CI
job checks that fixture's boundary exactly as a third-party backend author's.

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
durable stores, the catalog, configured providers (with the fallback chain),
cron, and portals; `daemon` mode serves the HTTP portal
gateway so remote front-ends attach and control across processes. `navi-code`
and `navi-daemon` are real navi modules on the same surface, the old `sdk/`
authoring stub is retired, and the out-of-repo extension path is guarded by CI
(`internal/testdata/external-navi`, `internal/testdata/external-component`). The old `internal/daemon`
run path and its `internal/portal` compatibility shim are now retired (NAVI-354); cron tick state
persists and missed ticks coalesce across restarts via `connector/cron`'s
`WithStateStore` seam (NAVI-358); the thought-queue metrics collector is parked
pending wiring onto the reference navi (NAVI-357); portal state access —
thought history, transcripts, and the eventual unified navi stream — is
specified by [ADR 0011](../adr/0011-persistence-is-a-datastore-component.md) and
[ADR 0012](../adr/0012-portals-attach-to-one-navi-stream.md) and tracked as
NAVI-360 (backend stores + integration surface) and NAVI-361 (TUI portal +
navi exposure).

## Related

- [runtime.md](./runtime.md) — navi object, thought system, providers, lifecycle
- [security.md](./security.md) — permissions, omission, secrets, trust
- [extension-model.md](./extension-model.md) — out-of-repo components
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones
- [`docs/adr/`](../adr/) — decision records (0011 persistence, 0012 portal stream, 0013 context assembly, 0014 connectors)