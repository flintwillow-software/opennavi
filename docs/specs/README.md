---
status: draft
created: '2026-07-29'
updated: '2025-08-13'
tags:
  - spec-tree
  - specification
---

# Specification Root

> Ordered specification tree for OpenNavi — a platform for building navis, which are self-contained AI agent binaries distributed as single executable files.

## Status

- Components: 15
- Changes: 0 in-flight
- Latest version: 0.2.0 — additions: tool catalog (13), agent roles (14), binary distribution, team deployment, LM Studio provider, board-based work discovery

## Conventions

### Spec Codes

Every subfeature has a **spec code**: 6 digits = `component.feature.subfeature` flattened (`00.01.02` → `000102`).

| Context | Format | Example |
|---------|--------|---------|
| Frontmatter / programmatic | bare 6 digits | `spec-refs: [000000, 000100]` |
| Inline links / display | `S` prefix | `[S000000 foo feature](docs/specs/00-core-component/00-foo-feature.md)` |
| File path | numbered `NN-` folders + files | `docs/specs/00-core-component/00-foo-feature.md` |

- `spec-refs` in a **feature file** lists the codes of the subfeatures it defines.
- `spec-refs` in a **component README** lists the codes of its features.
- `spec-refs` in a **change proposal** lists the codes the change touches.

### Numbering

- **Component**: 2-digit zero-padded folder (`00`, `01`, …).
- **Feature**: 2-digit zero-padded file (`NN-feature-name.md`).
- **Subfeature**: 2-digit zero-padded `##` heading — every feature file starts with `## 00`.
- Requirements use `### Requirement:` headings with one SHALL + GIVEN/WHEN/THEN/AND scenarios.

### Amendment Model

- **Default: append** — new subfeatures go at the end of a feature file, new features at the end of the component, new components at the end of the tree.
- **Insertion**: when adjacency matters, the `specs-renumber` skill rewrites numbers + frontmatter codes + inline links + paths atomically.
- Numbers are **positional sort prefixes**; the kebab-name carries identity.

### Change Traceability

Each change is named for its primary spec code: `docs/changes/<version>/<NN>-<kebab>-S<primary-code>/`.

- **Change frontmatter**: `s-code: S000000` + `spec-refs: [000000, 000100]`
- **Spec file frontmatter**: `spec-refs: [000000, 000001]` — codes of subfeatures defined here.
- **Traceability chain**: spec code → change folder → version → git tag.

## Change Map

| Change | S-Code | Version | Spec Refs | Status |
|--------|--------|---------|-----------|--------|
| — | — | — | — | — |

## Components

| ID | Component | Status | Features | Spec Refs | Last Updated |
|----|-----------|--------|----------|-----------|--------------|
| 00 | [Navi Core](00-navi-core/README.md) | draft | 8 | [000000—000700] | 2026-08-15 |
| 01 | [Agent Runtime](01-agent-runtime/README.md) | draft | 5 | [010000—010400] | 2025-08-13 |
| 02 | [Agent Config](02-agent-config/README.md) | draft | 2 | [020000—020100] | 2025-08-13 |
| 03 | [Session Context](03-session-context/README.md) | draft | 3 | [030000—030200] | 2025-08-13 |
| 04 | [Multi-Agent](04-multi-agent/README.md) | draft | 7 | [040000—040600] | 2025-08-13 |
| 05 | [Messaging](05-messaging/README.md) | draft | 4 | [050000—050300] | 2025-08-13 |
| 06 | [Memory](06-memory/README.md) | draft | 1 | [060000] | 2025-08-13 |
| 07 | [LLM Provider](07-llm-provider/README.md) | draft | 1 | [070000] | 2025-08-13 |
| 08 | [Operations](08-operations/README.md) | draft | 11 | [080000—081000] | 2026-08-15 |
| 09 | [Cloud Platform](09-cloud-platform/README.md) | planned | 4 | [090000—090300] | 2025-08-13 |
| 10 | [Integrations](10-integrations/README.md) | planned | 6 | [100000—100500] | 2025-08-13 |
| 11 | [Creative Pipeline](11-creative-pipeline/README.md) | planned | 5 | [110000—110400] | 2025-08-13 |
| 12 | [Avatar](12-avatar/README.md) | planned | 3 | [120000—120200] | 2025-08-13 |
| 13 | [Tool Catalog](13-tool-catalog/README.md) | active | 5 | [130000—130400] | 2026-09-12 |
| 14 | [Agent Roles](14-agent-roles/README.md) | draft | 10 | [140000—140900] | 2026-08-15 |

## Related

- [Changes](../../changes/README.md) — in-flight and completed changes
- [Templates](../../templates/README.md) — spec tree and change proposal skeletons
- [Case Studies](../../case-study/) — documented project outcomes