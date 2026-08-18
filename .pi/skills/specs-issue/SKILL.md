---
name: specs-issue
description: >
  Drive Path 3 of the specs workflow: turn change log entries into GitHub issues. Reads the changes
  written by specs-changes under docs/changes/<version>/<type>/<change-name>/README.md, analyzes and
  organizes them into epics, issues, and sub-issues, creates the GitHub issues with the correct
  issue types / projects / statuses, and updates the change docs with the issue IDs or URLs for
  traceability. Use after specs-changes has written the change log, or when the user wants change log
  entries filed on the board.
allowed-tools: Bash(read, grep, find, ls, mkdir, sed, gh)
---

# specs-issue

> Path 3 of the specs workflow: change log → epics/issues/sub-issues → GitHub issues. The change log
> from specs-changes is the input; the issue tree on the project board is the output.

## The 3-Path Workflow

```
Path 1: idea/case study → spec updates        (specs-proposal)
Path 2: spec diff → change log                (specs-changes)
Path 3: change log → epics/issues/sub-issues  (this skill)
```

## Prerequisites

- `gh` authenticated with `project` + `read:org` scopes: `gh auth status` / `gh auth refresh -s project,read:org`
- The target repo is a GitHub repo under the workspace org (e.g. `flintwillow-software/opennavi`)
- Follow the global `github-project-manager` skill conventions: discover the project board and its
  Status/Issue-Type fields first; org-level issue types must exist (Epic, Task, Feature) before creating typed issues

## Steps

### 1. Read the change log

- Read the change entries under `docs/changes/<version>/<type>/<change-name>/README.md` that are not
  yet tracked (no issue ID stamped).
- For each entry, extract: Summary, Changes (issue-granularity bullets with sub-bullets), Spec Refs,
  and the Notes (grouping intent written by specs-changes).

### 2. Analyze and organize into epics, issues, and sub-issues

- **Epic** — one per change folder (the coherent spec change). Title from the change name.
- **Issue** — one per **Changes** bullet in the entry. Title from the bullet.
- **Sub-issue** — one per indented sub-bullet (parallelizable parts).
- Use the Notes grouping intent when the change log is ambiguous.
- Record the mapping in the change doc before creating anything:

```markdown
## Issue Mapping

- Epic: <title>
  - Issue: <title> → spec: S130000
    - Sub-issue: <title>
```

### 3. Discover the project board

Per `github-project-manager` conventions:

```bash
gh project list --owner flintwillow-software --format json | jq -r '.[] | "\(.number) | \(.title)"'
gh project field-list <project-number> --owner flintwillow-software --format json | jq '
  .[] | select(.name == "Status") | {id: .id, options: [.single_select_options[] | {name, id}]}'
```

### 4. Create the Epic

One Epic per change folder, typed per org issue types:

```bash
gh issue create --repo flintwillow-software/<repo> \
  --title "<type>: <change name>" \
  --type Epic \
  --body "$(cat <<'EOF'
## Goal
<from the change entry Summary>

## Scope
<the Changes list>

Spec refs: <spec codes>
Change: docs/changes/<version>/<type>/<change-name>/
EOF
)"
```

### 5. Create Issue and Sub-issue tasks

For each mapped issue, create a Task/Feature-type issue with its sub-issues, linking sub-issues to
the issue and the issue to the epic (GraphQL sub-issues per `github-project-manager`).

### 6. Add to the project board (Ready)

```bash
gh project item-add <project-number> --owner flintwillow-software \
  --url "https://github.com/flintwillow-software/<repo>/issues/<num>"
gh project item-edit <project-number> --owner flintwillow-software ... # Status → Ready
```

### 7. Stamp the change docs

Update each change entry's `README.md` with the issue IDs/URLs so the system knows it's been created:

```markdown
## Tracked

- Epic: <url> — #<num>
  - Issue: <title> — #<num>
    - Sub-issue: <title> — #<num>
```

The change doc now carries the traceability chain: spec code → change folder → issues → version.

## Verification

- [ ] One Epic + N Issue (+ sub-issue) tasks created, typed (Epic/Task/Feature), titles per conventions
- [ ] Sub-issue links verified (subIssues.totalCount on the issue, issues on the epic)
- [ ] All issues on the project board in Ready status
- [ ] Change docs stamped with issue IDs/URLs
- [ ] No `fleet` terminology (the workspace uses `team`)

## Related

- `specs-changes` — previous step: writes the change log this skill reads
- `specs-proposal` — Path 1 (origin of the spec update)
- `github-issue-tracker` — execute the filed issues (global skill)
- `github-project-manager` — project board administration (global skill)