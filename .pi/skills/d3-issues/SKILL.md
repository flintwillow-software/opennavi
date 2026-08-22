---
name: d3-issues
description: >
  Step 4 of the d3 workflow: turn change log entries into tracked work items in a project management
  tool. Reads the changes written by d3-changes under docs/changes/<version>/<type>/<change-name>/README.md,
  analyzes and organizes them into epics, issues, and sub-issues, researches implementation steps,
  presents the organized proposal for human review and approval, then creates the work items with the
  correct types / statuses / relationships (parent/blockedby/depends-on) and priority based on need
  and blockers. Updates the change docs with the issue IDs or URLs for traceability.
  Use after d3-changes has written the change log. Currently supports GitHub via gh CLI.
allowed-tools: Bash(read, grep, find, ls, mkdir, sed, gh)
---

# d3-issues

> Step 4 of the d3 workflow: change log → organize → human review → PM tool. The change log
> from d3-changes is the input; the organized issue proposal is presented for review; the issue tree
> on the project board is the final output. Currently supports GitHub (via gh CLI) as the PM tool;
> the skill is structured to accommodate other PM tools in the future.

## The 5-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (d3-proposals)
Step 2: case study → spec tree updates (docs/specs/)           (d3-specs)
Step 3: spec diff → change log (docs/changes/<version>/)       (d3-changes)
Step 4: change log → epics/issues/sub-issues in PM tool        (this skill)
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
  and the Notes (grouping intent written by d3-changes).

### 2. Research implementation steps

For each change entry, research what it would take to implement:

- What files/modules would need to be created or modified
- What dependencies exist between entries (what blocks what)
- What the rough effort/priority should be based on need and blocker relationships
- Any architectural considerations or design decisions needed

### 3. Analyze and organize into epics, issues, and sub-issues

- **Epic** — one per change folder (the coherent spec change). Title from the change name.
- **Issue** — one per **Changes** bullet in the entry. Title from the bullet.
- **Sub-issue** — one per indented sub-bullet (parallelizable parts).
- Use the Notes grouping intent when the change log is ambiguous.
- Determine priority: items that unblock others get higher priority; items with no dependents can be lower.
- Set relationships: note which issues block or depend on which.
- Record the mapping:

```markdown
## Issue Mapping

- Epic: <title> [priority: high]
  - Issue: <title> → spec: S130000 [priority: high, blocks: <other-issue>]
    - Sub-issue: <title> [priority: medium]
  - Issue: <title> → spec: S130100 [priority: medium, depends-on: <above-issue>]
```

### 4. Human review and approval ⚠️

**This step is mandatory.** Before creating any issues in GitHub, present the organized proposal to
the user:

```
## Proposed Issue Organization

### Epic 1: Add Tool Catalog (high priority)
- **Issue:** Define tool catalog overview (S130000) — high, blocks everything below
  - *Sub-issue:* Define tool schema
  - *Sub-issue:* Define tool access levels
- **Issue:** Implement file read/write tools (S130100) — medium, depends-on S130000
  ...

### Relationships
- S130000 blocks S130100, S130200
- S130100 and S130200 are independent

### Next
After approval, I'll create these in GitHub with the correct project, types, and relationships.
```

- Do **NOT** create any issues until the human approves the organization.
- Iterate on the proposal based on feedback.
- Approval can be per-epic or whole-file.

### 5. Discover the project board

After approval, per `github-project-manager` conventions:

```bash
gh project list --owner flintwillow-software --format json | jq -r '.[] | "\(.number) | \(.title)"'
gh project field-list <project-number> --owner flintwillow-software --format json | jq '
  .[] | select(.name == "Status") | {id: .id, options: [.single_select_options[] | {name, id}]}'
```

### 6. Create the Epic

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

### 7. Create Issues and Sub-issues

For each mapped issue, create a Task/Feature-type issue with its sub-issues, linking sub-issues to
the issue and the issue to the epic (GraphQL sub-issues per `github-project-manager`).

Set relationships:
- **Parent:** sub-issues link to their parent issue; issues link to their epic
- **Blocked by:** set `blockedBy` when an issue depends on another
- **Depends on:** set `dependsOn` when an issue must complete before another starts

Set priority based on the research from Step 2 — use the project board's priority field if available,
or embed in the issue body/labels.

### 8. Add to the project board (Ready)

```bash
gh project item-add <project-number> --owner flintwillow-software \
  --url "https://github.com/flintwillow-software/<repo>/issues/<num>"
gh project item-edit <project-number> --owner flintwillow-software ... # Status → Ready
```

### 9. Stamp the change docs

Update each change entry's `README.md` with the issue IDs/URLs so the system knows it's been created:

```markdown
## Tracked

- Epic: <url> — #<num>
  - Issue: <title> — #<num> [priority: high]
    - Sub-issue: <title> — #<num> [priority: medium]
    - Sub-issue: <title> — #<num> [priority: medium]
  - Issue: <title> — #<num> [priority: low, depends-on: #<above-num>]
```

The change doc now carries the full traceability chain:
`spec code → change folder → issues (with relationships and priority) → version`

## Verification

- [ ] One Epic + N Issue (+ sub-issue) tasks created, typed (Epic/Task/Feature), titles per conventions
- [ ] Sub-issue links verified (subIssues.totalCount on the issue, issues on the epic)
- [ ] Issue relationships set (parent/blockedby/depends-on) per the organized proposal
- [ ] Priority set per the research from Step 2
- [ ] All issues on the project board in Ready status
- [ ] Change docs stamped with issue IDs/URLs, priorities, and relationships
- [ ] Human reviewed and approved the organization before creation

## Related

- `d3-workflow` — entrypoint orchestrator
- `d3-changes` — previous step: writes the change log this skill reads
- `d3-specs` — Step 2 (origin of the spec update)
- `d3-proposals` — Step 1 (origin of the case study)
- `github-issue-tracker` — execute the filed issues (global skill)
- `github-project-manager` — project board administration (global skill)