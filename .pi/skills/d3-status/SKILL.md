---
name: d3-status
description: >
  Show the current state of the spec tree and change log: active changes, completed versions,
  and component health. Report step in the d3 workflow (proposal → changes → issue) — the
  spec-side status; issue-side status lives on the GitHub project board (see github-project-manager /
  github-issue-tracker, global skills). Use when the user wants to know what's in flight or what shipped.
allowed-tools: Bash(grep, find, ls, sort, sed)
---

# d3-status

> Report the current state of the spec tree and change log.

## The 4-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (d3-proposal)
Step 2: case study → spec tree updates (docs/specs/)           (d3-proposal)
Step 3: spec diff → change log (docs/changes/<version>/)       (d3-changes)
Step 4: change log → epics/issues/sub-issues in GitHub         (d3-issue)
```

Status observes the spec side of the pipeline (active changes, synced versions). For the execution side (issues on the board), use the GitHub project board via `github-project-manager` / `github-issue-tracker`.

## Steps

### 1. Read the change tracker

Read `docs/changes/README.md` — the Active Changes and Completed Versions tables.

### 2. Walk the change tree

For each `docs/changes/<version>/` directory:

- List active change folders with their S-codes, status, and spec-refs.
- `grep` the frontmatter of each `proposal.md` for status, s-code, spec-refs.

### 3. Walk the spec tree

For each component in `docs/specs/`, count features and their statuses.

### 4. Present the report

```text
## Spec Tree
00-core-component: 2 features (1 draft, 1 in-review)
01-secondary-component: 1 feature (draft)

## In-Flight Changes
0.1.0: 2 changes
  [00-add-foo-S000000] proposed → 000000, 000001
  [01-add-bar-S000100] in-progress → 000100

## Completed Versions
0.0.0: 2 changes (tagged v0.0.0)
```

## Related

- `d3-index` — generates the permanent spec tree index
- `d3-issue` — file change tasks as GitHub issues
- `github-project-manager` / `github-issue-tracker` — execution status on the project board
- `docs/changes/README.md` — the live change tracker