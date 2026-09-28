# navi-daemon

Starter template for a **daemon-style navi**: an always-on background agent
that consumes its thought queue, wakes on cron schedules, and serves the HTTP
portal gateway.

This is a real out-of-repo-style navi module — it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`, declaring its tools, model, connector, and portal in Go. There is
no YAML definition file and no separate build step: `go build` produces the
whole binary.

## Layout

```
navi-daemon/
├── cmd/navi-daemon/main.go      # the navi definition and entry point
├── go.mod                       # nested module, pinned to the published SDK
└── README.md
```

## What the navi declares

Everything in `cmd/navi-daemon/main.go` is the compiled-in definition:

- **Identity** — name, version, and personality (`navi.WithPersonality`).
- **Tool** — one author tool (`queue_status`) reporting uptime, memory, and
  goroutine count.
- **Model** — one Ollama provider marked default (ADR 0009).
- **Connector** — `connector/cron`, turning schedule definitions into thoughts
  (a thought factory).
- **Portal** — `portal/gateway`, the HTTP observation surface. Control is
  authorizer-gated and fails closed, so the starter binds an observation-only
  gateway.

Deployment-time settings (provider endpoints, credential references, schedule
overrides) come from the runtime config file named by `NAVI_CONFIG`, if set.

## Build & run

```
go build ./cmd/navi-daemon
```

The template's `go.mod` requires the published module. When developing against
a local checkout, use a `go.work` file — never add a `replace` directive to
this `go.mod`:

```
go work init . ../..
# replace github.com/flintwillow-software/opennavi v0.1.0 => ../..
```