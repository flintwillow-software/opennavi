---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000200, 000800]
tags:
  - adr
  - navi-core
  - sdk
---

# 0002 — The navi is a composition root with typed collections

## Context

Today "navi" names two disconnected things. `sdk.NaviDef` is a declarative
metadata struct that `sdk.Build` validates and then discards
(`ErrBuilderUnavailable`). The real, working navi is `cmd/navi` plus
`internal/daemon.Assemble`, a monolith that hardcodes its store, tools,
providers, thought queue, and connectors. The authoring surface and the runtime
do not communicate: the starter templates register tools into a local registry
that `sdk.Build` never sees, and the shipped binary ignores the definition
entirely. `sdk.Skill` is validated but never read anywhere in the codebase.

An SDK is only an SDK if its authoring path produces the thing that runs.

## Decision

The navi becomes a single, public composition root — the base object a navi
author constructs. It owns typed collections, each populated at construction
time (compile-in, Go-only):

```go
n := navi.New("oncall-bot",
    navi.WithVersion("1.0.0"),
    navi.WithPersonality(onCallPersona),
)

n.Skills.Add(skills.IncidentResponse)
n.Tools.Add(tools.TodoCreate(store))
n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4"))

discord := connector.Discord(connector.Config{Credential: "DISCORD_TOKEN"})
n.Connectors.Add(discord)
n.Tools.Add(discord.SendTool())

n.Portals.Add(portal.TUI())
n.Portals.Add(portal.WebSocket(":8090"))

if err := n.Run(ctx); err != nil {
    log.Fatal(err)
}
```

- The shape is the cobra model: construct, append, execute.
- `NaviDef` and `sdk.Build` merge into this object; `Build()`/`Run()` are the
  single entry point.
- Collections declare; `Run()` freezes them (snapshot), resolves ordering, and
  serves. Nothing mutates once serving begins.
- `Add` performs eager shape validation; `Run` performs cross-cutting
  validation (duplicate names, port conflicts, mode/tool consistency,
  permission ceiling).
- No `init()`-based registration. Explicit construction keeps the dependency
  graph visible and testable.

## Consequences

- A navi repo is a normal Go module with a short `main.go` — the same developer
  experience as writing a cobra CLI.
- `cmd/navi` stops being the only thing that runs; it becomes a reference navi.
- The templates guardrail (`S000805`) now guards a real authoring path.
- Wholesale breaking change to `sdk/`: pre-1.0, acceptable, but the templates,
  `tools/sdk-consumer`, and the CI guardrail must move in the same change.

## Alternatives considered

- **Keep `NaviDef` as declarative metadata and keep `Assemble` private.**
  Preserves two sources of truth for tools and leaves external authors unable
  to assemble a navi. Rejected.
- **Functional-options-only builder (`n = n.WithTool(...)`), no collections.**
  Workable but obscures the per-layer capability surface that makes a navi
  auditable at a glance. Rejected in favor of named collections.