---
name: specs-proposal
description: >
  Drive Path 1 of the specs workflow: turn an idea or case study into approved spec updates. Takes a
  proposed idea or case study (new or updated), runs a gap analysis against the spec trees of the
  relevant repos, writes/updates the case study docs plus a spec-updates.md that documents the
  proposed new or changed specs, presents it for human review and approval, then applies the approved
  updates directly to docs/specs/ in the target repo(s). Use when the user proposes an idea or case
  study, wants to update an existing case study, or wants to take an idea into spec.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv)
---

# specs-proposal

> Path 1 of the specs workflow: idea → case study → approved spec updates. The case study is the
> vehicle; spec-updates.md is the approval artifact; the spec tree edit is the deliverable.

## The 3-Path Workflow

```
Path 1: idea/case study → spec updates        (this skill)
Path 2: spec diff → changes docs              (specs-changes)
Path 3: changes docs → GitHub issues          (specs-issue)
```

Path 1 ends with the spec tree updated directly. Path 2 then derives what code must change from the
spec diff, and Path 3 turns that into tracked GitHub issues. Run Path 1 alone when the user only wants
spec work; continue to Path 2/3 when the user wants the change implemented.

## Steps

### 1. Understand the idea or case study

- If the user proposes a **new idea**: clarify scope, stakeholders, constraints, and the problem.
- If the user proposes a **new case study**: scaffold `docs/case-study/<kebab-name>/` with the standard
  sub-files (entry point, CONTEXT.md, SOLUTION.md, RESULTS.md) from the case study conventions.
- If the user wants to **update an existing case study**: read the current sub-files first.

### 2. Gap analysis against current specs

Walk the spec trees of the relevant repos (`repos/*/docs/specs/README.md`) and map:

| Case study demand | Supporting spec | Gap |
|-------------------|-----------------|-----|
| (from the idea/case study) | existing component/feature that covers it | missing, partial, or planned |

- Use `specs-render` for the holistic spec picture of each repo.
- A demand is a gap when no spec covers it, the spec is `planned`, or the spec exists in a different
  repo than the one that should own it.
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
> is the source of truth; Path 2 derives the work from the diff.

## Out of Scope

- Generating changes docs from the spec diff → `specs-changes`
- Creating GitHub issues → `specs-issue`
- Executing issues (branch/PR/draft PR/pipeline) → `github-issue-tracker` (global skill)

## Related

- `specs-changes` — NEXT STEP: derive code-change docs from the spec diff
- `specs-issue` — file the changes as GitHub issues
- `specs-render` — holistic spec picture for gap analysis
- `github-issue-tracker` — execute the filed issues (global skill)
- `docs/specs/README.md` — spec code and numbering conventions