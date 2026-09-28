# navi-code

Starter template for a **CLI coding agent**: an interactive terminal navi with
tools for working in a repository.

This is a real out-of-repo-style navi module — it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`, declaring its skill, tools, model, and portal in Go. There is no
YAML definition file and no separate build step: `go build` produces the whole
binary.

## Layout

```
navi-code/
├── cmd/navi-code/
│   ├── main.go                  # the navi definition and entry point
│   └── coding-conventions.md    # compiled-in skill (go:embed)
├── go.mod                       # nested module, pinned to the published SDK
└── README.md
```

## What the navi declares

Everything in `cmd/navi-code/main.go` is the compiled-in definition:

- **Identity** — name, version, and personality (`navi.WithPersonality`).
- **Skill** — `coding_conventions`, embedded from `coding-conventions.md`.
- **Tools** — the builtins (`file_read`, `shell_exec`) bound to the working
  directory, plus one author tool (`repo_stats`). `shell_exec` is compiled in
  but denied by the permission ceiling (`navi.WithPermissions`), so it is never
  bound into a thought's surface.
- **Model** — one Ollama provider marked default (ADR 0009).
- **Portal** — the TUI.

Deployment-time settings (provider endpoints, credential references) come from
the runtime config file named by `NAVI_CONFIG`, if set — never from the
compiled definition.

## Build & run

```
go build ./cmd/navi-code
```

The template's `go.mod` requires the published module. When developing against
a local checkout, use a `go.work` file — never add a `replace` directive to
this `go.mod`:

```
go work init . ../..
# replace github.com/flintwillow-software/opennavi v0.1.0 => ../..
```