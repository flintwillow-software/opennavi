---
name: specs-changes
description: >
  Drive Path 2 of the specs workflow: derive the code-change docs from a spec update. Resolves the
  repo, reviews the spec in place, updates it with what was requested (if not already applied), diffs
  new spec against old, and writes the changes as a semantic-versioned change log under docs/changes/,
  grouped by conventional-commit type with epic/issue/sub-issue mapping kept in mind. Use after
  specs-proposal has applied spec updates, or when the user wants to move from a spec change to the
  implementation work list.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv, git)
---

# specs-changes

> Path 2 of the specs workflow: spec diff → change log. Writes what changed in the spec as a
> changelog-style record of what needs to change in code, grouped so the epic/issue/sub-issue mapping
> in Path 3 falls out naturally.

## The 3-Path Workflow

```
Path 1: idea/case study → spec updates        (specs-proposal)
Path 2: spec diff → change log                (this skill)
Path 3: change log → epics/issues/sub-issues  (specs-issue)
```

Path 2 writes the change log only — it does NOT write the epic/issue/sub-issue breakdown. That is
Path 3's job, reading this log. Path 2 groups entries so that breakdown is obvious.

## Steps

### 1. Resolve the repo

- The user names a repo, or the spec change already happened in a known repo (`repos/`, `infra/`).
- If the spec update was applied by `specs-proposal`, the repo is whatever Path 1 touched.

### 2. Review the spec in place

- Read the updated spec files in `docs/specs/` — the current state is the source of truth.
- Identify which components/features the update touched (from `spec-refs` + Status History).

### 3. Update the spec if not yet applied

- If the user is requesting a spec change directly (no Path 1), apply the change to `docs/specs/`
  in place first: new subfeatures appended, `spec-refs` appended, Status History updated, tree root
  updated. (Same direct-edit mechanics as specs-proposal step 5.)

### 4. Diff new spec against old

- `git diff` the spec files between the old state and the new state:

```bash
git diff HEAD~1 -- docs/specs/   # or compare against the pre-update commit
```

- Extract from the diff the behavioral deltas: new requirements, changed requirements, new subfeatures.
- Ignore housekeeping (Status History rows, link fixes) unless they imply behavior.

### 5. Write the change log

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

- <grouping intent for Path 3: which entries naturally form one epic, which are independent issues>
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

### 6. Update the tracker

- Update `docs/changes/README.md`: the Active Changes table (or version log) gains the new change
  entry; note the spec refs.

### 7. Hand off to Path 3

- Report the change folder path(s) and the grouping intent (Notes sections) for `specs-issue` to
  expand into epics/issues/sub-issues.

## Related

- `specs-proposal` — Path 1 (produces the spec update this skill diffs)
- `specs-issue` — NEXT STEP: expand the change log into epics/issues/sub-issues and file GitHub issues
- `github-issue-tracker` — execute the filed issues (global skill)
- `docs/changes/README.md` — change log conventions