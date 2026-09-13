# navi-daemon

Starter template for a **daemon-style navi** — the OpenClaw/Hermes
pattern: an always-on background agent that consumes its thought
queue, wakes on cron schedules, and serves the HTTP gateway. Ships
alongside `navi-code` as a `navi init` choice
(`--template daemon`).

## Layout (S000100)

```
navi-daemon/
├── cmd/navi-daemon/main.go      # entry point: sdk.Build(...) + tool registration
├── .config/navi/navi-daemon/
│   └── config.example.yaml      # runtime config example (operator settings)
├── go.mod                       # nested module, pinned to the published SDK
└── README.md
```

## What's in the definition vs. runtime config

The Go definition in `cmd/navi-daemon/main.go` is the **static half**
(Go-only, S000207): name, version, personality, gateway + cron
interfaces, and the permission policy. One author tool (`queue_status`)
is registered through the tool registry (S130400); catalog builtins
join the active set once the tool catalog ships (NAVI-59).

The **deployment half** is runtime config owned by the operator under
`.config/navi/navi-daemon/` (S000200 §04): provider endpoints, cron
schedules, work-discovery sources. See `config.example.yaml`.

## Build & run

```
go build ./cmd/navi-daemon
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
