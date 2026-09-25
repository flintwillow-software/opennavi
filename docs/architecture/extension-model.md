---
status: draft
created: '2026-09-24'
updated: '2026-09-24'
tags:
  - architecture
  - extensions
  - sdk
---

# Extension Model

> How tools, skills, models, connectors, and portals are written and distributed
> from outside the core repository, and how navis themselves are authored
> outside it. Decisions: ADR
> [0004](../adr/0004-contract-packages-public-implementations-private.md),
> [0005](../adr/0005-build-time-modules-are-the-extension-path.md).

## Two things extend the system

- **Out-of-repo components** — a tool/skill/model/connector/portal published as
  its own Go module and consumed by a navi.
- **Out-of-repo navis** — an application repo that imports the SDK and composes
  a navi. Nothing in the core repo is special.

Both are ordinary Go modules. The unit of distribution is the module; the unit
of versioning is semver on the contract packages.

## Dependency rule

```
opennavi/navi      ← composition root
   │ imports
   ▼
opennavi/tool  opennavi/thought  opennavi/connector  opennavi/portal  opennavi/model  opennavi/skill
   ▲ imported by
   │
acme/navi-jira, acme/navi-kit, acme/oncall-bot
```

Components import **contract packages only**, never `navi`. This keeps the graph
acyclic and prevents an extension from dragging runtime internals into
consumers.

## Writing a component

A third-party tool is a normal module:

```
github.com/acme/navi-jira/
├── go.mod        // require github.com/flintwillow-software/opennavi vX.Y.Z
└── jira.go
```

```go
package jira

import "github.com/flintwillow-software/opennavi/tool"

func IssueRead(c Config) tool.Tool {
    return tool.New(
        tool.Def{
            Name:        "jira_issue_read",
            Description: "Read a Jira issue by key.",
            InputSchema: tool.Schema(`{...}`),
            Access:      tool.ReadOnly,
            Timeout:     10 * time.Second,
            Recovery:    "retry_once",
            Mode:        tool.Sequential,
            Source:      tool.Source{Module: "github.com/acme/navi-jira", Version: "v1.2.0"},
        },
        func(ctx context.Context, args map[string]any) (string, error) { ... },
    )
}
```

| Layer | Packaging | Notes |
|---|---|---|
| Tool | `func Foo(cfg) tool.Tool` | Full metadata descriptor |
| Skill | `//go:embed *.md` + accessor | Content travels with the module, compiles in |
| Model | `func Foo(...) model.Model` | Provider descriptor; key resolved at runtime |
| Connector | implements `connector.Connector` | Receives thought/memory/model handles via `Deps` |
| Portal | implements `portal.Portal` | List/schedule/steer thoughts via `Deps` |

A shared internal kit (`acme/navi-kit`) can expose common skills, tools,
connectors, and even a base navi constructor for a team, so many navis compose
on top.

## Writing a navi

```go
// github.com/acme/oncall-bot/main.go
//go:embed persona.md skills/*.md
var content embed.FS

func main() {
    n := navi.New("oncall-bot",
        navi.WithVersion(version),
        navi.WithPersonalityFS(content, "persona.md"),
    )
    n.Skills.AddFS(content, "skills")
    n.Tools.Add(jira.IssueRead(jira.Config{Credential: "JIRA_TOKEN"}))
    n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
    n.Connectors.Add(connectors.Cron())
    n.Portals.Add(portals.TUI())
    if err := n.Run(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

`go build` links exactly what is imported. The navi repo's `go.mod` is the
record of what the binary can do.

## Build-time vs runtime

| | Build-time module | Runtime plugin (deferred, S000600) |
|---|---|---|
| Typed | yes | via schema/JSON-RPC |
| Isolation | none | subprocess, sandboxed |
| Rebuild required | yes | no |
| Trust | covered by the navi signature; module graph | explicit allow-signers |
| Registry origin | `author` / `sdk` / `builtin` | `extension` |

Build-time is the primary path (ADR 0005). Runtime plugins, if introduced,
register into the same tool registry at a distinct trust level.

## Versioning and guardrails

- Contract packages (`tool`, `thought`, `connector`, `portal`, `model`,
  `skill`, `navi`) are the semver surface. Breaking them is a major bump.
- Navi repos and component repos pin versions in `go.mod`; that pin is the
  compatibility record.
- CI builds **synthetic out-of-repo modules** — an external navi and an external
  component module — against `main` and every release tag, in addition to the
  in-tree starter templates. `tools/sdk-consumer` is the seed for this.
- `SourceInfo` is populated from build info so provenance is auditable at
  runtime.

## Anti-patterns

- Extensions importing `opennavi/navi` (coupling, cycles).
- Contract packages importing SQLite, HTTP clients, or provider SDKs.
- `init()`-based registration in component modules.
- Using Go's `plugin` package.

## Related

- [overview.md](./overview.md) — package layout
- [security.md](./security.md) — trust boundary
- [runtime.md](./runtime.md) — connector/portal contracts
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones