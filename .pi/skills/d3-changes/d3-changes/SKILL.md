---
name: d3-changes
description: >
  Drive Step 3 of the d3 workflow: derive the code-change docs from a spec update by analyzing
  both the spec diff AND the current state of the repo's codebase. Resolves the repo, reviews the
  spec in place, inspects the existing code structure to understand what already exists, diffs new
  spec against old, and writes the changes as a semantic-versioned change log under docs/changes/,
  grouped by conventional-commit type with epic/issue/sub-issue mapping kept in mind. The code
  analysis ensures change entries are concrete and grounded in actual project structure. Use after
  d3-specs has applied spec updates, or when the user wants to move from a spec change to the
  implementation work list.
allowed-tools: Bash(read, grep, find, ls, mkdir, cp, mv, git)
---

# d3-changes

> Step 3 of the d3 workflow: spec diff + code analysis → change log. Writes what changed in the
> spec as a changelog-style record of what needs to change in code, informed by the actual codebase
> structure so entries are concrete and accurate. Grouped so the epic/issue/sub-issue mapping in
> Step 4 falls out naturally.

## The 5-Step d3 Workflow

```
Step 1: idea/chat → case study                                 (d3-proposals)
Step 2: case study → spec tree updates (docs/specs/)           (d3-specs)
Step 3: spec diff + code analysis → change log                 (this skill)
Step 4: change log → epics/issues/sub-issues in PM tool        (d3-issues)
```

## Steps

### 1. Resolve the repo

- The user names a repo, or the spec change already happened in a known repo (`repos/`, `infra/`).
- If the spec update was applied by `d3-specs`, the repo is whatever Step 2 touched.

### 2. Review the spec in place

- Read the updated spec files in `docs/specs/` — the current state is the source of truth.
- Identify which components/features the update touched (from `spec-refs` + Status History).
- Understand what each spec change actually requires: new data models, APIs, UI components,
  service integrations, configuration surfaces, etc.

### 3. Analyze the codebase

Before writing any change entries, survey the actual code to ground the change log in reality:

- **Project structure**: What's the repo's directory layout? Is it a monorepo, multi-module,
  single service? What language/build system? (`ls`, `find` top-level structure, check for
  `Cargo.toml`, `package.json`, `go.mod`, `pyproject.toml`, etc.)
- **Existing patterns**: What coding patterns are already in use? How are new modules typically
  added? What's the testing convention? Look at existing modules for style/pattern reference.
- **Existing code vs. spec changes**: For each spec change, ask: does analogous code already
  exist? What modules/files would need to be created or modified? Are there existing interfaces
  or abstractions the new code should integrate with?
- **Dependencies**: What new dependencies (libraries, services, APIs) would the change introduce?
  Check existing dependency files (`Cargo.toml`, `package.json`, `go.mod`, etc.).
- **Gaps between spec and code**: If the spec describes something that doesn't exist yet in code,
  note what needs to be built. If something partially exists, note what needs to change.

This analysis is **read-only** — you're gathering context, not modifying files. Use `find`, `grep`,
`ls`, and `read` to explore the codebase efficiently.

### 4. Diff new spec against old

```bash
git diff HEAD~1 -- docs/specs/   # or compare against the pre-update commit
```

- Extract from the diff the behavioral deltas: new requirements, changed requirements, new subfeatures.
- Ignore housekeeping (Status History rows, link fixes) unless they imply behavior.
- For an initial spec tree (single commit), use the initial commit as the baseline and document
  the entire tree as the first change set.

### 5. Write the change log (informed by code analysis)

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

> Derived from the spec diff of <spec code(s)> and informed by the codebase analysis. Groups the
> code changes implied by the spec update, grounded in actual project structure.

## Summary

<2-3 sentences: what the spec change requires in code>

## Changes

- **<module/file-path>** — <what needs to change, referencing actual files/directories that exist
  or need to be created>
- **<module/file-path>** — <what needs to change, referencing actual files/directories>

## Spec Refs

<the spec codes this change implements, e.g. S130000, S130100>

## Code Context

- **Project structure**: <language, build system, key directories relevant to this change>
- **Existing patterns**: <relevant coding patterns the implementation should follow>
- **Dependencies**: <new dependencies or services required>

## Notes

- <grouping intent for Step 4: which entries naturally form one epic, which are independent issues>
- <specific file paths or code areas that will be impacted>
```

Grouping rules — keep the epic/issue/sub-issue mapping in mind while writing:

- One change folder per coherent spec change; group entries that will land in the same epic under the
  same change folder.
- Each **Changes** bullet is written at issue granularity (one unit of code change, one future issue).
- If a bullet has sub-parts that could be parallelized, list them as indented sub-bullets (future
  sub-issues) — but don't label them epic/issue/sub-issue here; the Notes section captures the intent.
- **Changes bullets should reference actual or expected file paths** where possible, based on the
  codebase analysis from Step 3. This makes the change log immediately actionable in Step 4.

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

### 7. Hand off to Step 4

- Report the change folder path(s) and the grouping intent (Notes sections) for `d3-issues` to
  expand into epics/issues/sub-issues. Include any code-context insights that would help with
  implementation research.

## Related

- `d3-workflow` — entrypoint orchestrator
- `d3-specs` — Step 2 (produces the spec update this skill diffs)
- `d3-proposals` — Step 1 (origin of the case study)
- `d3-issues` — NEXT STEP: expand the change log into epics/issues/sub-issues and file in your PM tool
- `github-issue-tracker` — execute the filed issues (global skill)
- `docs/changes/README.md` — change log conventions