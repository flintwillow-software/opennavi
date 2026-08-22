---
name: d3-renumber
description: >
  Consolidation pass: renumber components, features, and subfeatures to close gaps or insert sections.
  Rewrites all frontmatter spec-refs, inline links, and file paths atomically in one commit. Maintenance
  step in the d3 workflow (proposal → changes → issue) — exceptional, not routine.
  Implementation is tracked as GitHub issues (d3-issue + github-issue-tracker), not via renumbering.
allowed-tools: Bash(grep, find, sed, git, mv)
---

# d3-renumber

> Renumber and rewrite references atomically. The kebab-name carries identity; the number is a positional sort prefix.

## When to Renumber

- **Insertion**: a change needs to add a component between existing `02` and `03` → all subsequent components shift.
- **Consolidation**: gaps accumulate from deletions (e.g., `00`, `02` → close the gap, make it `00`, `01`).
- **Promotion**: a feature outgrew its file → promote to a component, shift others.

## Steps

### 1. Compute the new numbering

- Components: sequential `00`, `01`, `02`… ordered by position.
- Features within each component: sequential `00`, `01`, `02`… ordered by position.
- Subfeatures within each feature file: sequential `00`, `01`, `02`… ordered by position.

### 2. Build the code map

For each old code → new code:

```
000000 → 000000  (unchanged)
000100 → 000200  (shifted)
020000 → 010000  (reordered)
```

### 3. Rename folders and files

```bash
# Rename component folders
mv docs/specs/02-old-name docs/specs/01-old-name

# Rename feature files
mv docs/specs/01-old-name/01-feature.md docs/specs/01-old-name/00-feature.md
```

### 4. Rewrite frontmatter spec-refs

For every `.md` file under `docs/specs/` and `docs/changes/`, apply the code map to `spec-refs` arrays.

### 5. Rewrite inline links

For every `[S<code> ...](...)` link under `docs/specs/` and `docs/changes/`, rewrite the S-code and update the path to match the new folder/file structure.

### 6. Update Status History

Don't rewrite historic S-codes in Status History — they refer to the change code, not the spec position. Only rewrite `spec-refs` arrays.

### 7. Regenerate the index

Call `d3-index` to rebuild `docs/specs/README.md`.

### 8. Commit

```bash
git add -A && git commit -m "specs: renumber for consolidation"
```

## Related

- `d3-index` — regenerate index after renumbering
- `d3-changes` — normal amendment (renumber is exceptional, not routine)