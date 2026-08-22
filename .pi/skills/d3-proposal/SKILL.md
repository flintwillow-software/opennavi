---
name: d3-proposal
description: >
  Drive Steps 1 and 2 of the d3 workflow: take an idea or chat discussion and produce or update a
  case study, then analyze case study demands against existing spec trees, write spec-updates.md
  with proposed new/changed specs, present for human review and approval, and apply approved updates
  directly to docs/specs/ in the target repo(s). Use when the user proposes an idea or case study,
  wants to update an existing case study, or wants to take an idea into spec.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv)
---

# d3-proposal

> Steps 1 & 2 of the d3 workflow: idea/chat → case study → approved spec updates. The case study is
> the vehicle; spec-updates.md is the approval artifact; the spec tree edit is the deliverable.

## The 4-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (this skill)
Step 2: case study → spec tree updates (docs/specs/)           (this skill)
Step 3: spec diff → change log (docs/changes/<version>/)       (d3-changes)
Step 4: change log → epics/issues/sub-issues in GitHub         (d3-issue)
```

Steps 1 and 2 are handled by this skill. Step 3/4 run separately after approval.

## Steps

### 1. Understand the idea or chat

- If the user proposes a **new idea or shares a chat/discussion**: clarify scope, stakeholders,
  constraints, and the problem. What product or system is being designed? What would success look like?
- If the user proposes a **new case study**: scaffold `docs/case-study/<kebab-name>/` with the
  standard sub-files (entry point, CONTEXT.md, SOLUTION.md, RESULTS.md) from the case study conventions.
- If the user wants to **update an existing case study**: read the current sub-files first,
  understand what changed, and plan the update.

### 2. Gap analysis against current specs

Walk the spec trees of the relevant repos (`repos/*/docs/specs/README.md` or
`infra/*/docs/specs/README.md`) and map:

| Case study demand | Supporting spec | Gap |
|-------------------|-----------------|-----|
| (from the idea/case study) | existing component/feature that covers it | missing, partial, or planned |

- Use `d3-render` for the holistic spec picture of each repo.
- A demand is a gap when no spec covers it, the spec is `planned`, or the spec exists in a
  different repo than the one that should own it.
- Note which repo each gap belongs to — the update lands there.

### 3. Write the case study + spec-updates.md

- Write the case study sub-files (entry point with ownership map + related specs, CONTEXT, SOLUTION,
  RESULTS).
- Create **`spec-updates.md`** in the case study folder — the proposal document:

```markdown
# Spec Updates

> Proposed new or changed specs required to execute this case study. Reviewed and approved by a human
> before any spec tree is edited.

| Spec Code | Repo | File | Type | Why |
|-----------|------|------|------|-----|
| S130000 | repos/opennavi | docs/specs/13-tool-catalog/00-tool-catalog-overview.md | added | standard tool set for the coding agent |

## Per-Update Details

### S130000 (added)
- **Repo/file:** repos/opennavi/docs/specs/13-tool-catalog/00-tool-catalog-overview.md
- **What:** component + 4 features covering the 13 standard tools
- **Why:** the case study's tool system has no spec
```

- Each row maps to a gap from step 2. Spec codes follow the target repo's numbering conventions.

### 4. Human review and approval

- Present the case study + `spec-updates.md` to the user for review.
- Do **NOT** edit any `docs/specs/` file until the human approves the updates.
- Iterate on the proposal based on feedback.
- Approval can be per-update or whole-file — apply only what's approved.

### 5. Apply the approved updates directly to the spec tree(s)

For each approved update, edit the target repo's `docs/specs/` **in place**:

- New component: create `NN-component/` with README + feature files, register in the tree root.
- New feature: create `NN-feature.md`, add to the component README table, update the tree root.
- Modified feature: edit the file — new subfeatures at the end (append), update `spec-refs`
  (append only), add a Status History row.
- Update the tree root `docs/specs/README.md` (component table, status, latest version).
- Register the change in `docs/changes/README.md` (Active Changes table).
- Commit per repo.

> No change dirs, no proposal/design/tasks scaffolding, no GitHub issues at this stage. The spec tree
> is the source of truth; d3-changes derives the work from the diff.

## Out of Scope

- Generating change docs from the spec diff → `d3-changes`
- Creating GitHub issues → `d3-issue`
- Executing issues (branch/PR/draft PR/pipeline) → `github-issue-tracker` (global skill)

## Related

- `d3-workflow` — entrypoint orchestrator
- `d3-changes` — NEXT STEP: derive code-change docs from the spec diff
- `d3-issue` — file the changes as GitHub issues
- `d3-render` — holistic spec picture for gap analysis
- `github-issue-tracker` — execute the filed issues (global skill)
- `docs/specs/README.md` — spec code and numbering conventions