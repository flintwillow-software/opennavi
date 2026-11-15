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
- `tool/registry`, `tool/{builtin,http,note,thoughts,time,todo}`
  — the tool system: registry and shipped catalog
- `datastore/` — public persistence contract (views, candidates, handle);
  `datastore/inmem` is the reference backend, `datastore/sqlite` the durable one
- `internal/` — runtime wiring (agent loop, providers, bridge, config, cron,
  context, ops)
- `cmd/navi/` — the reference navi; `templates/` — starter navi modules
- `internal/testdata/external-*` — synthetic out-of-repo modules for the CI guardrail
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

## Releasing

Cutting a release is a **procedure, not a merge hook** — every merge to `main`
does not publish one. It is two commands, because the changelog cut goes
through a pull request and the tag must name a commit that is really on `main`:

```bash
just release              # cuts [Unreleased] on release/vX.Y.Z and pushes it,
                          # then prints the gh pr create command
# open the PR, let the four checks pass, merge it
just release-tag v0.6.0   # tags the merged main
```

Both prompt for the tag message in `$EDITOR`: the summary is a **human**
artifact (that is what comparable projects converge on), while the version and
the changelog cut are mechanical. The version comes from the `Semver:` hints in
`[Unreleased]` — highest wins — so **every PR must add an entry there**; the
`Verify changelog entry` check enforces it. A release cut is exempt, because it
moves those entries into a versioned section.

> **Never tag before the merge.** An earlier version of this tooling committed
the cut to `main` and tagged first: branch protection rejected the push but the
tag had already gone through, leaving a published tag pointing at a commit that
was not on `main`. `just release-tag` refuses unless `HEAD == origin/main`.

Shipping the release to a running navi is the rest of the train — see the
`release-navi` skill in the engineering workspace.

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