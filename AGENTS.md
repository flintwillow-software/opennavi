# AGENTS.md

## Project Overview

This is a **repo scaffolding template** from the Engineering Workspace. New repositories should be created by copying this template and customizing it for the target project.

## Template Structure

```
repo-template/
├── README.md                           ← Project overview (uses <placeholder> syntax)
├── CONTRIBUTING.md                     ← Contribution guidelines
├── CHANGELOG.md                        ← Keep a Changelog format
├── .github/
│   └── PULL_REQUEST_TEMPLATE.md       ← PR template with type dropdown
└── AGENTS.md                           ← You are here
```

### README.md

Uses `<placeholder>` syntax for project-specific values:
- `<Project Name>` — title
- `<repo>` — repository name
- `flintwillow-software/<repo>` — GitHub path
- `<One-line description>` — project summary

### CONTRIBUTING.md

Covers the basics: fork/clone workflow, conventional commits, PR requirements, and code standards.

### CHANGELOG.md

Follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format with semantic versioning.

### PULL_REQUEST_TEMPLATE.md

Includes a type dropdown (bug, feature, refactor, etc.), related issue link, testing checklist, and screenshots section.

## Documentation Priority

**Always look in `docs/` first for conventions, standards, and how-to guides.** This is the single source of truth for how the workspace operates.

## Commit Conventions

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new feature
fix: resolve bug #123
docs: update README
chore: update dependencies
refactor: restructure module
test: add unit tests
```

## Build & Development Tasks — use `just`

**Always use the `justfile` for building, testing, and running the CLI.** Don't hand-roll `go build` / `go test` invocations — the recipes match the CI gates and keep artifacts out of the source tree.

```bash
just build      # compile the CLI → bin/navi
just ci         # build + vet + fmt-check + test-race (all CI merge gates)
just test       # unit tests
just test-race  # race detector (CI-only gate locally; see justfile)
just vet        # go vet
just fmt        # gofmt in place (fmt-check is the CI gate)
```

Run the CLI against the local runtime state directory:

```bash
just daemon     # start daemon with -db data/navi.db
just health     # health probe against data/navi.db
just enqueue    # enqueue a thought against data/navi.db
just clean      # remove bin/ and data/
```

Conventions agents must respect:

- **`bin/` and `data/` are the only artifact/runtime directories.** Never write binaries, databases, logs, or scratch files into the source tree — bare gitignore patterns at the repo root previously caused tracked files under `cmd/navi/` to be silently ignored (NAVI-54). Gitignore entries are anchored to these directories only.
- **Pass explicit `-db` paths** (`data/navi.db`) when invoking `./bin/navi` directly; the compiled-in default is `./navi.db`, which would drop a database in the repo root.
- Run `just ci` before pushing — `-race` cannot run on the local Pi, so CI is the only place it executes; don't treat a local green `just test` as the full gate.

## AI Agent Guidelines

- Always check `docs/` before answering workflow or convention questions
- For workspace-wide conventions, check the parent `engineering-workspace/docs/`
- When scaffolding a new repo, copy this template and replace `<placeholder>` values
- For GitHub issues, project boards, labels, and milestones, use the **github-project-manager** skill
