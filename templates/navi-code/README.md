# navi-code

Starter template for a **CLI coding agent** — the pi/Claude-Code style
navi: an interactive terminal chat with tools for working in a
repository. Ships as the scaffolding base for `navi init`
(`--template code`, the default).

## Layout (S000100)

```
navi-code/
├── cmd/navi-code/main.go        # entry point: sdk.Build(...) + tool registration
├── skills/
│   └── coding-conventions.md    # compiled-in skill (system prompt)
├── .config/navi/navi-code/
│   └── config.example.yaml      # runtime config example (end-user settings)
├── go.mod                       # nested module, pinned to the published SDK
└── README.md
```

## What's in the definition

Everything in `cmd/navi-code/main.go` is the **static definition**
(Go-only, S000207): name, version, personality, one compiled-in skill,
the CLI interface, and the permission policy. One author tool
(`repo_stats`) is registered through the tool registry (S130400);
catalog builtins join the active set once the tool catalog ships
(NAVI-59).

Runtime settings — LLM provider endpoint, key paths — are **runtime
config** owned by the end user under `.config/navi/navi-code/`
(S000200 §04). See `config.example.yaml`.

## Build & run

```
go build ./cmd/navi-code
```

The template's `go.mod` pins a published SDK version
(`github.com/flintwillow-software/opennavi`). When developing against
a local checkout of the SDK, use a `go.work` file — never add a
`replace` directive to this `go.mod`:

```
go work init . ../..
# pin the workspace replace to the version required above:
# replace github.com/flintwillow-software/opennavi v0.1.0 => ../..
```
