---
status: draft
created: '2026-09-24'
updated: '2026-09-28'
tags:
  - architecture
  - extensions
  - sdk
---

# Extension Model

> How tools, skills, models, connectors, and portals are written and distributed
> from outside the core repository, and how navis themselves are authored
> outside it. Decisions: ADR
> [0004](../adr/0004-component-packages-public-implementations-private.md),
> [0005](../adr/0005-build-time-modules-are-the-extension-path.md).

## Two things extend the system

- **Out-of-repo components** — a tool/skill/model/connector/portal published as
  its own Go module and consumed by a navi.
- **Out-of-repo navis** — an application repo that imports the SDK and composes
  a navi. Nothing in the core repo is special.

Both are ordinary Go modules. The unit of distribution is the module; the unit
of versioning is semver on the component packages.

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

Out-of-repo modules import **component packages only**, never `navi`. This keeps
the graph acyclic and prevents an extension from dragging runtime internals into
consumers.

Component packages are **stdlib-only** and pull in no runtime internals:
importing one must not transitively reach an `internal/*` package or a
tool-system implementation package (`tool/registry`, the shipped catalog). The
`external-consumer` CI job asserts this as *contract-purity* over the component
module's whole dependency closure — not just its direct imports (NAVI-355) — and
a separate CI step asserts that no public package imports `internal/*`
(NAVI-356). A registration seam that needs the registry (the queue-handle
binding) therefore lives in `tool/registry`, not in the `thought` contract.

`datastore` is a **public package**: thought/transcript, stateful tool, and
memory data types, their interfaces, and in-memory helpers, with the durable
implementations in `datastore/fs` and `datastore/sqlite`,
`tool/{note,todo,registry}` importing it is therefore
public→public, not public→implementation — an intentional boundary, recorded
here (NAVI-356). The shipped catalog implementations live in the public
`tool/*` packages (including `tool/builtin`'s `file_read`/`shell_exec`); the
generic agent loop under `internal/agent` seeds no tools and receives them
through the registry and `internal/bridge`.

## Writing a component

A third-party tool is a normal module:

```
github.com/acme/navi-jira/
├── go.mod        // require github.com/flintwillow-software/opennavi vX.Y.Z
└── jira.go
```

```go
package jira

import (
    "context"
    "encoding/json"
    "time"

    "github.com/flintwillow-software/opennavi/tool"
)

func IssueRead(c Config) tool.Tool {
    schema, _ := tool.ParseSchema(json.RawMessage(`{...}`))
    return tool.Tool{
        Def: tool.Def{
            Name:          "jira_issue_read",
            Description:   "Read a Jira issue by key.",
            InputSchema:   schema,
            OutputFormat:  "text",
            Access:        tool.AccessReadOnly,
            Timeout:       10 * time.Second,
            Recovery:      "retry_once",
            Mode:          tool.ToolModeSequential,
            Version:       "1.2.0",
            PromptSnippet: "Read a Jira issue by key",
            SourceInfo: tool.SourceInfo{
                Origin: tool.OriginAuthor,
                Path:   "github.com/acme/navi-jira",
            },
        },
        Handler: func(ctx context.Context, args map[string]any) (string, error) { ... },
    }
}
```

| Layer | Packaging | Notes |
|---|---|---|
| Tool | `func Foo(cfg) tool.Tool` | Full metadata descriptor |
| Skill | `//go:embed *.md` + accessor | Content travels with the module, compiles in |
| Model | `func Foo(...) model.Provider` | Provider descriptor; the credential is a reference resolved at runtime |
| Connector | implements `connector.Connector` | Receives thought/model handles via `Deps`; exposes egress tools via `SendTools()` and turns declared inbound events into thoughts |
| Portal | implements `portal.Portal` | List/schedule/steer thoughts via `Deps` |

A connector's **outbound** half is egress tools: ordinary `tool.Tool` values
built with `connector.SendTool`, added explicitly by the navi author and never
auto-registered (ADR 0014, ADR 0020). The tool is bound at authoring time while
the service client arrives at `Start`, so the connector publishes it into a
`connector.Live[C]` handle and the gate is built into the handler:

```go
type Connector struct{ live *connector.Live[*client] }

func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
    if _, err := c.token(); err != nil { return err }  // fail closed (ADR 0008)
    c.live.Set(c.newClient())
    return nil
}
func (c *Connector) Stop() error { c.live.Clear(); return nil }

func (c *Connector) SendTools() ([]tool.Tool, error) {
    return []tool.Tool{connector.SendTool(def, c.live, c.reach)}, nil
}
```

A call before `Start` (or after `Stop`) returns `connector.ErrNotStarted` — a
clean error, never a nil dereference. `connector` references only the `tool`
package, so this stays contract-pure and an out-of-repo connector inherits no
implementation dependency.

A shared internal kit (`acme/navi-kit`) can expose common skills, tools,
connectors, and even a base navi constructor for a team, so many navis compose
on top.

## Writing a navi

```go
// github.com/acme/oncall-bot/main.go
//go:embed persona.md skills/*.md
var content embed.FS

func main() {
    persona, _ := fs.ReadFile(content, "persona.md")
    n := navi.New("oncall-bot",
        navi.WithVersion(version),
        navi.WithPersonality(string(persona)),
    )
    n.Skills.Add(skill.Skill{
        Name:    "triage",
        Content: mustRead(content, "skills/triage.md"),
    })
    n.Tools.Add(jira.IssueRead(jira.Config{Credential: "JIRA_TOKEN"}))
    n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
    n.Connectors.Add(cron.New(cron.Config{Schedules: []cron.Schedule{
        {Name: "health", Expr: "*/30 * * * *", Prompt: "Report runtime health."},
    }}))
    n.Portals.Add(tui.NewPortal())
    if err := n.Run(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

`go build` links exactly what is imported. The navi repo's `go.mod` is the
record of what the binary can do.

## Build-time vs runtime

| | Build-time module | Runtime plugin (deferred) |
|---|---|---|
| Typed | yes | via schema/JSON-RPC |
| Isolation | none | subprocess, sandboxed |
| Rebuild required | yes | no |
| Trust | module graph (artifact signing deferred) | explicit allow-signers |
| Registry origin | `author` / `sdk` / `builtin` | `extension` |

Build-time is the primary path (ADR 0005). Runtime plugins, if introduced,
register into the same tool registry at a distinct trust level.

## Versioning and guardrails

- Component packages (`tool`, `thought`, `connector`, `portal`, `model`,
  `skill`, `navi`) are the semver surface. Breaking them is a major bump.
- Navi repos and component repos pin versions in `go.mod`; that pin is the
  compatibility record.
- CI builds **synthetic out-of-repo modules** — an external navi and an external
  component module — against `main` and every release tag, in addition to the
  in-tree starter templates. `internal/testdata/external-navi` and
  `internal/testdata/external-component` are those modules; the `external-consumer` CI job
  builds them and asserts the component's import boundary — direct imports are
  contract-only and the full dependency closure is contract-pure.
- `SourceInfo` is populated from build info so provenance is auditable at
  runtime.

## Anti-patterns

- Extensions importing `opennavi/navi` (coupling, cycles).
- Public packages importing runtime internals (`opennavi/internal/*`).
- Component packages importing SQLite, HTTP clients, or provider SDKs.
- `init()`-based registration in component modules.
- Using Go's `plugin` package.

## Related

- [overview.md](./overview.md) — package layout
- [security.md](./security.md) — trust boundary
- [runtime.md](./runtime.md) — connector/portal interfaces
- [rewrite-plan.md](./rewrite-plan.md) — migration milestones