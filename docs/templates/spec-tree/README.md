---
status: draft
created: 'YYYY-MM-DD'
updated: 'YYYY-MM-DD'
tags:
  - spec-tree
  - specification
priority: medium
---

# Specification Root

> Auto-generated index of the ordered spec tree. Maintained by the `specs-index` skill — do not edit by hand.

## Structure

```
docs/specs/
├── README.md                    ← this file: generated index + requirement inventory
├── NN-component/
│   ├── README.md                ← component entry: purpose, feature table, conventions
│   ├── NN-feature.md            ← feature + subfeature definitions (spec codes)
│   └── NN-feature.md
└── NN-component/
    └── ...
```

## Components

| ID | Component | Status | Features | Spec Refs | Last Updated |
|----|-----------|--------|----------|-----------|--------------|
| 00 | <component-name> | draft | 2 | [000000] | YYYY-MM-DD |
| 01 | <component-name> | draft | 1 | [010000] | YYYY-MM-DD |

## Requirement Inventory

> Every `### Requirement:` heading across the tree, ordered by spec code.

| Code | Requirement | Component | Status |
|------|-------------|-----------|--------|
| [S000000 Req name](docs/specs/00-component/00-feature.md) | <requirement name> | 00 | draft |
| [S000001 Req name](docs/specs/00-component/00-feature.md) | <requirement name> | 00 | draft |

## Change Map

> Each S-code links to the versioned change that implemented it.

| Change | S-Code | Version | Spec Refs | Status |
|--------|--------|---------|-----------|--------|
| <name> | S000000 | 0.1.0 | [000000, 000001] | complete |

## Conventions

Spec codes: 6 digits = `component.feature.subfeature` flattened. Bare in frontmatter (`[000000]`), `S`-prefixed inline (`[S000000 name](link)`). See the live [docs/specs/README.md](../../specs/README.md) for the full conventions.