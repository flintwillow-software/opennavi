---
name: d3-index
description: >
  Regenerate the docs/specs/README.md index. Walks the spec tree, reads frontmatter spec-refs,
  and builds the component table, requirement inventory, and change map. Maintenance step in the
  d3 workflow (proposal → changes → issue) — index regenerates the tree index after direct spec
  edits or d3-renumber. Implementation is tracked as GitHub issues (d3-issue + github-issue-tracker),
  not via the index.
allowed-tools: Bash(grep, find, sed, awk, sort)
---

# d3-index

> Regenerate the ordered spec tree index. Reads frontmatter, never parses headings.

## Steps

### 1. Walk the spec tree

For each `docs/specs/NN-component/` directory:

- Read `README.md` frontmatter for `spec-refs` (feature codes for this component).
- For each `NN-feature.md` file, read frontmatter for `spec-refs` (subfeature codes defined in this file).

### 2. Build the component table

| ID | Component | Status | Features | Spec Refs | Last Updated |
|----|-----------|--------|----------|-----------|--------------|

### 3. Build the requirement inventory

For each feature file, extract `### Requirement:` headings and pair with their spec codes (from the file's `spec-refs` array). Fall back to the file's `spec-refs` order if heading-to-code mapping is ambiguous.

| Code | Requirement | Component | Status |
|------|-------------|-----------|--------|

### 4. Build the change map

Read `docs/changes/README.md` Completed Versions table for the change map.

| Change | S-Code | Version | Spec Refs | Status |
|--------|--------|---------|-----------|--------|

### 5. Write `docs/specs/README.md`

Overwrite with the updated tables. Preserve the Status section and conventions sections.

## Related

- `d3-changes` — calls this skill after a change log is written
- `d3-renumber` — calls this after renumbering