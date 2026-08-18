---
status: draft
created: 'YYYY-MM-DD'
updated: 'YYYY-MM-DD'
tags:
  - change
  - template
---

# Change Directory

> This directory is a versioned change proposal. Each change lives under `docs/changes/<semver>/<NN>-<kebab-name>-S<code>/`.

## Files

| File | Purpose |
|------|---------|
| `proposal.md` | Why this change exists, what it changes, what's out of scope |
| `design.md` | Technical approach, architecture, decisions |
| `tasks.md` | Numbered implementation checklist (1.1, 1.2, …) |
| `specs/` | Future-state copies of affected spec files, mirroring `docs/specs/` paths |

## S-Code Convention

The `S<code>` in the folder name is the **primary spec code** this change implements. Spec codes are 6 digits = `component.feature.subfeature` flattened (`00.01.00` → `000100`). Bare in frontmatter (`spec-refs: [000000, 000100]`), `S`-prefixed in display and folder names (`S000000`). Unique within a version directory.

## Lifecycle

1. `specs-propose` → scaffolds this directory
2. Human + AI iterate on proposal, design, tasks
3. `specs-apply` → implements tasks
4. `specs-sync` → copies `specs/` into `docs/specs/`, appends spec-refs to feature frontmatter, regenerates index, writes commit message, deletes this dir
5. Git tag matches the version directory name