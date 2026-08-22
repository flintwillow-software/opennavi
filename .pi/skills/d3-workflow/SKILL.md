---
name: d3-workflow
description: >
  Entrypoint for the Document-Driven Design (d3) workflow. Orchestrates the 4-step lifecycle: case
  study creation/updates → spec tree changes → change log generation → GitHub issue creation.
  Use this as the entrypoint for any d3 task; it routes to the appropriate d3-* skill based on
  the current state and user intent.
allowed-tools: Bash(read, grep, find, ls)
---

# d3-workflow — Document-Driven Design

> The d3 workflow manages the entire lifecycle from user ideas to tracked implementation work,
> with the specification tree as the source of truth. The name "d3" stands for **Document-Driven
> Design**: ideas become case studies, case studies drive spec trees, spec diffs drive change logs,
> and change logs drive tracked GitHub issues.

## The 4-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (d3-proposal)
Step 2: case study → spec tree updates (docs/specs/)           (d3-proposal)
Step 3: spec diff → change log (docs/changes/<version>/)       (d3-changes)
Step 4: change log → epics/issues/sub-issues in GitHub         (d3-issue)
```

### Step 1 — Case Study Creation/Updates

Take an idea, user chat, or design discussion and turn it into a structured case study under
`docs/case-study/<name>/` with the standard sub-files (CONTEXT.md, SOLUTION.md, RESULTS.md).

**When to run:** User has an idea, proposes a new project, or wants to document a design reference.

### Step 2 — Spec Tree Updates

Analyze the case study against existing spec trees, identify gaps, propose new/changed specs in
`spec-updates.md`, get human approval, then apply updates directly to `docs/specs/` in the
target repo(s).

**When to run:** After a case study exists and needs spec coverage, or when the user wants to
update specs for an existing case study.

### Step 3 — Change Log Generation

After specs have been updated (or a new spec tree has been created), diff the new spec state
against the old, and write structured change log entries under
`docs/changes/<version>/<type>/<change-name>/`. Each entry documents what code changes are
implied by the spec change.

**When to run:** After spec tree updates have been applied and the user wants to generate
implementation work items.

### Step 4 — Issue Creation

Read the change log entries, organize them into epics/issues/sub-issues, research implementation
steps, then **present the organized proposal for human review**. After approval, create the
GitHub issues with correct types, projects, parent/blockedby/depends-on relationships, and
priority based on need and blockers. Stamp issue IDs back into the change docs for traceability.

**When to run:** After change logs are written and the user wants to file implementation work.

## Routing

| User Intent | Skill |
|-------------|-------|
| Create/update a case study from an idea or chat | `d3-proposal` (Step 1) |
| Update spec trees from a case study | `d3-proposal` (Step 2) |
| Generate change log from spec diffs | `d3-changes` (Step 3) |
| Create GitHub issues from change logs | `d3-issue` (Step 4) |
| Regenerate spec tree index | `d3-index` |
| Render full spec tree for context | `d3-render` |
| Renumber/consolidate spec tree | `d3-renumber` |
| Show current spec tree + change log status | `d3-status` |

## Multi-Repo Workspace

For repos inside `repos/` or `infra/` (submodules), use `ws-d3` to resolve the target repo and
delegate to its shipped d3-* skills:

```
ws-d3 → resolve repo → cd $REPO → read .pi/skills/d3-*/SKILL.md → execute
```

For the workspace's own docs (the engineering-workspace itself), the d3-* skills in
`.pi/skills/` apply directly.

## Related

- `d3-proposal` — Steps 1 & 2: case studies → spec updates
- `d3-changes` — Step 3: spec diff → change log
- `d3-issue` — Step 4: change log → GitHub issues
- `ws-d3` — multi-repo routing for component repos
- `github-project-manager` — GitHub project board administration (global skill)
- `github-issue-tracker` — execute filed issues (global skill)
- `d3-index` — regenerate the spec tree index
- `d3-render` — holistic spec picture
- `d3-status` — spec tree and change log status