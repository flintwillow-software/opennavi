---
name: d3-changes
description: >
  Drive Step 3 of the d3 workflow: derive the code-change docs from a spec update. Resolves the
  repo, reviews the spec in place, diffs new spec against old, and writes the changes as a
  semantic-versioned change log under docs/changes/, grouped by conventional-commit type with
  epic/issue/sub-issue mapping kept in mind. Use after d3-proposal has applied spec updates, or
  when the user wants to move from a spec change to the implementation work list.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv, git)
---

# d3-changes

> Step 3 of the d3 workflow: spec diff → change log. Writes what changed in the spec as a
> changelog-style record of what needs to change in code, grouped so the epic/issue/sub-issue
> mapping in Step 4 falls out naturally.

## The 4-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (d3-proposal)
Step 2: case study → spec tree updates (docs/specs/)           (d3-proposal)
Step 3: spec diff → change log (docs/changes/<version>/)       (this skill)
Step 4: change log → epics/issues/sub-issues in GitHub         (d3-issue)
```

## Steps

### 1. Resolve the repo

- The user names a repo, or the spec change already happened in a known repo (`repos/`, `infra/`).
- If the spec update was applied by `d3-proposal`, the repo is whatever Step 2 touched.

### 2. Review the spec in place

- Read the updated spec files in `docs/specs/` — the current state is the source of truth.
- Identify which components/features the update touched (from `spec-refs` + Status History).

### 3. Diff new spec against old

```bash
git diff HEAD~1 -- docs/specs/   # or compare against the pre-update commit
```

- Extract from the diff the behavioral deltas: new requirements, changed requirements, new subfeatures.
- Ignore housekeeping (Status History rows, link fixes) unless they imply behavior.
- For an initial spec tree (single commit), use the initial commit as the baseline and document
  the entire tree as the first change set.

### 4. Write the change log

Structure — semver version dir, then conventional-commit type, then change name, then README:

```
docs/changes/
├── README.md                          ← tracker + conventions (updated by this skill)
└── <version>/                         ← semantic version, e.g. 0.2.0
    ├── <type>/                        ← conventional-commit type: feat, fix, chore, refactor, docs, test, perf
    │   └── <change-name>/             ← kebab-case, e.g. tool-catalog
    │       └── README.md              ← the change log entry (this skill writes it)
    └── ...
```

Each change's `README.md` is a changelog entry:

```markdown
# <version> — <type>: <change name>

> Derived from the spec diff of <spec code(s)>. Groups the code changes implied by the spec update.

## Summary

<2-3 sentences: what the spec change requires in code>

## Changes

- **<component/area>** — <what needs to change in code>
- **<component/area>** — <what needs to change in code>

## Spec Refs

<the spec codes this change implements, e.g. S130000, S130100>

## Notes

- <grouping intent for Step 4: which entries naturally form one epic, which are independent issues>
```

Grouping rules — keep the epic/issue/sub-issue mapping in mind while writing:

- One change folder per coherent spec change; group entries that will land in the same epic under the
  same change folder.
- Each **Changes** bullet is written at issue granularity (one unit of code change, one future issue).
- If a bullet has sub-parts that could be parallelized, list them as indented sub-bullets (future
  sub-issues) — but don't label them epic/issue/sub-issue here; the Notes section captures the intent.

### Semver rules

- **Version selection:** bump per semantic versioning — `0.1.0` → `0.2.0` for new features/spec
  components, `0.1.1` for bug fixes, `1.0.0` for breaking changes. Read `docs/changes/README.md` for
  the current active version; if none, bump from the latest git tag.
- **Idempotency:** before creating a change folder, check whether a corresponding GitHub issue already
  exists for it (search the repo for the spec code / change name). If it does, mark the entry as
  already-tracked and skip — do not duplicate.

### 5. Update the tracker

- Update `docs/changes/README.md`: the Active Changes table (or version log) gains the new change
  entry; note the spec refs.

### 6. Hand off to Step 4

- Report the change folder path(s) and the grouping intent (Notes sections) for `d3-issue` to
  expand into epics/issues/sub-issues.

## Related

- `d3-workflow` — entrypoint orchestrator
- `d3-proposal` — Step 1 & 2 (produces the spec update this skill diffs)
- `d3-issue` — NEXT STEP: expand the change log into epics/issues/sub-issues and file GitHub issues
- `github-issue-tracker` — execute the filed issues (global skill)
- `docs/changes/README.md` — change log conventions