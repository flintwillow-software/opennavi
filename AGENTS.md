# AGENTS.md — opennavi

Repo-level guidance for agents working in this repository. Workspace-wide
conventions live in the engineering-workspace `AGENTS.md`.

## What this is

OpenNAVI is the Go SDK for building **navis** — thought-based agent binaries.
A navi is a composition root (`navi/`) over typed collections, and components
are written against the public component packages. Durable design lives in
`docs/architecture/` and `docs/adr/`; work state lives in Huly (project
**NAVI**). GitHub issues are public intake only.

## Layout

- `navi/` — composition root; `tool/` `thought/` `connector/` `portal/`
  `model/` `skill/` — public component packages
- `tool/registry`, `tool/bridge`, `tool/{builtin,http,note,thoughts,time,todo}`
  — the tool system: registry, executor bridge, and shipped catalog
- `agent/` `llm/` `store/` — public implementations
- `internal/` — runtime wiring (config, cron, context, ops)
- `cmd/navi/` — the reference navi; `templates/` — starter navi modules
- `testdata/external-*` — synthetic out-of-repo modules for the CI guardrail
- `docs/architecture/`, `docs/adr/` — durable design; `CHANGELOG.md` — release history

## Conventions

- Conventional commits; branch as `feature/<name>` / `fix/<name>` /
  `chore/<name>`; never commit to `main`.
- ADRs are immutable — supersede, don't edit.
- Huly is the source of truth for issues; do not edit GitHub issue state.
- Documentation moves with code: a PR that changes architecture updates
  `docs/architecture/` or adds an ADR in the same review.
- Durable design goes in `docs/`, never in Huly; work state goes in Huly,
  never in repo docs.

## Build & test — use `just`

The `justfile` recipes match the CI gates. Don't hand-roll `go build` /
`go test` invocations.

```bash
just build      # compile the reference navi → var/bin/navi
just ci         # build + vet + fmt-check + test-race (all CI merge gates)
just test       # unit tests
just vet        # go vet
just fmt        # gofmt in place (fmt-check is the CI gate)
```

Run the CLI against the local runtime state directory:

```bash
just daemon     # start the reference navi with -db var/data/navi.db
just health     # health probe against var/data/navi.db
just enqueue    # enqueue a thought against var/data/navi.db
just clean      # remove var/
```

Conventions agents must respect:

- **`var/` is the only artifact/runtime directory** (`var/bin` for binaries,
  `var/data` for state). Never write binaries, databases, logs, or scratch
  files into the source tree — bare gitignore patterns at the repo root
  previously caused tracked files under `cmd/navi/` to be silently ignored
  (NAVI-54). The gitignore entry is anchored (`/var/`).
- **Pass explicit `-db` paths** (`var/data/navi.db`) when invoking
  `var/bin/navi` directly; the compiled-in default is `./navi.db`, which would
  drop a database in the repo root.
- Run `just ci` before pushing — `-race` cannot run on the local Pi, so CI is
  the only place it executes; a local green `just test` is not the full gate.