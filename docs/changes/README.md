---
status: active
created: '2025-08-13'
updated: '2026-08-15'
tags:
  - changes
  - versioning
---

# Changes

> Versioned semantic-version change log. Each version directory (`0.0.0/`, `0.1.0/`, …) holds change
> folders grouped by conventional-commit type. This index is maintained by the `specs-status` skill.

## Convention

```
docs/changes/
├── README.md            ← this file: tracker + conventions
├── 0.1.0/               ← semantic version
│   ├── feat/            ← conventional-commit type (feat, fix, chore, refactor, docs, test, perf)
│   │   └── change-name/ ← kebab-case, e.g. tool-catalog
│   │       └── README.md ← the change log entry (written by specs-changes, stamped by specs-issue)
│   └── fix/
│       └── ...
└── 0.2.0/
    └── ...
```

- **Version directories**: semver (`0.1.0`, `0.2.0`, `1.0.0`). Git tag `v<version>` on release.
- **Type directories**: conventional-commit type (`feat`, `fix`, `chore`, `refactor`, `docs`, `test`, `perf`).
- **Change folders**: kebab-case name, e.g. `tool-catalog`, `incident-response`.
- **Change log entry**: `README.md` inside the change folder — a changelog-style entry written by
  `specs-changes` (Path 2) with the spec diff as input, later stamped with issue IDs by `specs-issue`
  (Path 3) for traceability.
- **Spec codes**: 6 digits = `component.feature.subfeature` flattened. Bare in frontmatter
  (`spec-refs: [000000]`), `S`-prefixed in display (`S000000`).
- **Lifecycle**: spec update (Path 1) → change log written (Path 2) → issues filed + stamped (Path 3).

## Active Versions

| Version | Type | Change | Spec Refs | Status | Issue |
|---------|------|--------|-----------|--------|-------|
| — | — | — | — | — | — |