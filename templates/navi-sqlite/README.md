# navi-sqlite

Starter template for a navi whose **state lives in one SQLite file**.

This is a real out-of-repo-style navi module — it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`, declaring a datastore candidate, its tools, model, and portal in
Go. There is no YAML definition file and no separate build step: `go build`
produces the whole binary.

## Layout

```
navi-sqlite/
├── cmd/navi-sqlite/
│   └── main.go      # the navi definition and entry point
├── go.mod           # nested module, pinned to the published SDK
└── README.md
```

## What the navi declares

- **Identity** — name, version, personality.
- **Datastore** — the `sqlite` backend (`sqlite.CandidateAt(dbPath)`), marked
  default for this binary. That single candidate backs every aggregate:
  thoughts and their transcripts, stateful tool state, and memory. The file
  is `var/navi-sqlite.db` unless `NAVI_DB` or a `datastore.params.path`
  runtime-config parameter says otherwise.
- **Tools** — the builtins (`file_read`, `shell_exec`) bound to the working
  directory, plus the stateful todo tool registered against the bound
  datastore's tool-state view (`n.Datastores.Handle().Stateful()`), so todos
  persist in the same database as everything else.
- **Model** — one Ollama provider marked default (ADR 0009).
- **Portal** — the TUI.

The backend is configured for the runtime's single ordered writer: WAL so a
flush never blocks a read, a busy timeout so another process sharing the file
(the `navi enqueue` mailbox, the cron connector) waits rather than fails, and
transcript bodies encrypted when `NAVI_SESSION_KEY` is set.

## Build & run

```bash
go build -o navi-sqlite ./cmd/navi-sqlite
./navi-sqlite
```

Runtime settings (provider endpoints, credential references, a different
`datastore.backend`) come from the config file named by `NAVI_CONFIG`, if set —
never from the compiled definition.
