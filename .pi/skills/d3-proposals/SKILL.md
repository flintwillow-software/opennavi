---
name: d3-proposals
description: >
  Step 1 of the d3 workflow: take an idea or chat discussion and produce or update a case study.
  Clarifies scope, stakeholders, and constraints, then scaffolds or updates the case study under
  docs/case-study/<name>/ with the standard sub-files (CONTEXT.md, SOLUTION.md, RESULTS.md).
  Use when the user proposes a new idea, shares a chat or design discussion, or wants to update
  an existing case study.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv)
---

# d3-proposals

> Step 1 of the d3 workflow: idea/chat → case study. The case study captures what the user wants to
> build, why, and for whom — without touching any spec tree yet.

## The 5-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (this skill)
Step 2: case study → spec tree updates (docs/specs/)           (d3-specs)
Step 3: spec diff → change log (docs/changes/<version>/)       (d3-changes)
Step 4: change log → epics/issues/sub-issues in PM tool        (d3-issues)
```

Step 1 produces a structured case study. Step 2 (d3-specs) then analyzes it against spec trees.

## Steps

### 1. Understand the idea or chat

- If the user proposes a **new idea or shares a chat/discussion**: read it carefully.
  Clarify: What is being designed or built? Who are the users/stakeholders? What problem does it
  solve? What are the constraints? What would success look like?
- If the user wants to **update an existing case study**: read the current sub-files first,
  understand what changed or what needs to be added, and plan the update.

### 2. Scaffold or update the case study

For a **new case study**:
- Create `docs/case-study/<kebab-name>/`
- Write the standard sub-files using the case study conventions:

  | File | Purpose |
  |------|---------|
  | `README.md` | Entry point: summary, quick reference, ownership map, status history, related docs |
  | `CONTEXT.md` | Business problem, stakeholders, constraints, out of scope |
  | `SOLUTION.md` | Architecture, design decisions, data model, interfaces |
  | `RESULTS.md` | Outcomes, metrics, lessons learned (can be a placeholder — fill in after deployment) |

- Each file should have YAML frontmatter (`status`, `created`, `updated`, `tags`, `priority`).
- The README should include an ownership map relating code modules to case study files.

For an **update to an existing case study**:
- Edit the relevant sub-files.
- Update the `updated` date and Status History in the README.

### 3. Do NOT touch spec trees

- This skill is about **proposals only** — writing down what the user wants.
- Do NOT analyze specs, write spec-updates.md, or edit docs/specs/. That's d3-specs (Step 2).
- If the user asks to move forward with spec analysis, tell them the case study is ready and
  recommend running `d3-specs` next.

## What a Good Case Study Looks Like

- Clear problem statement and target users.
- Concrete enough that a gap analysis against spec trees can be done (Step 2).
- Ownership map: which code modules would the implementation touch?
- Related spec trees and case studies linked.

## Out of Scope

- Gap analysis against spec trees → `d3-specs`
- Editing `docs/specs/` directly → `d3-specs`
- Generating change logs → `d3-changes`
- Creating GitHub issues → `d3-issues`

## Related

- `d3-workflow` — entrypoint orchestrator (use this first to clarify intent)
- `d3-specs` — NEXT STEP: turn the case study into spec tree updates
- `d3-changes` — generate change logs from spec diffs
- `d3-issues` — file changes as GitHub issues
- `docs/case-study/README.md` — case study index and conventions