---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - agent-config
priority: high
spec-refs: [020000, 020100]
---

# 02 Agent Config

> Skills and persona — baked in at build time, defining how a navi behaves and presents itself.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S020000 Skills System](00-skills-system.md) | Skills System | draft | Two-tier skills, compilation, activation, system prompt |
| [S020100 Persona System](01-persona-system.md) | Persona System | draft | Persona file formats, loading, configuration, CLI |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Two-tier skills: compiled-in (mandatory) and runtime-loaded (optional)
- Persona as system prompt — no special runtime enforcement, just text generation shaping
- Skills compiled into the system prompt at build time

## Related

- [Navi Core](../00-navi-core/README.md)
- [Agent Runtime](../01-agent-runtime/README.md)
