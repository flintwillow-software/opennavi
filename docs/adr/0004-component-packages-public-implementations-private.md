---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000800, 130400]
tags:
  - adr
  - sdk
  - layering
---

# 0004 — Component packages are public and dependency-light; implementations are private

## Context

For a third-party tool, connector, or portal to be written in its own repository,
the interface it satisfies must be importable without pulling in the runtime.
Today the reverse holds: the composition logic is private (`internal/daemon`)
while the pieces are public. An external author can import `agent`, `llm`,
`store`, and `tools/registry`, but cannot assemble a navi, and any extension
that wants helper types risks dragging heavy dependencies (SQLite drivers, HTTP
clients) into every consumer.

## Decision

Split the component packages from their implementations and keep them free of
heavy dependencies:

```
tool/          ← Tool, Def, Handler, Schema, Access   (stdlib-only)
thought/       ← Thought, ThoughtSystem handle, events
connector/     ← Connector interface + Deps
portal/        ← Portal interface + Deps
model/         ← Model + Provider descriptor
skill/         ← Skill value (embedded content)
navi/          ← composition root; imports all of the above
store/sqlite/  ← implementation (imported only by the navi author)
llm/ollama/    ← implementation
```

Three rules:

1. **Extensions import component packages, never `navi`.** `acme/navi-jira`
   requires `opennavi/tool`, not `opennavi/navi`. The graph stays acyclic and
   the extension does not depend on runtime internals.
2. **Interfaces are defined by the consumer/leaf and satisfied structurally.**
   `connector.Deps` exposes `thought.ThoughtSystem`, `memory.API`,
   `model.Registry`; `navi.Navi` satisfies them without the connector naming
   `navi`.
3. **Component packages stay dependency-light.** No SQLite, no HTTP client, no
   provider SDKs behind `tool` or `thought`.

## Consequences

- Third-party components are normal Go modules with independent semver,
  pinned by the navi repo's `go.mod` — the de-fork path.
- The public surface is larger and carries real semver obligations; this must
  be deliberate. Implementation packages remain swap-friendly.
- Binary size and supply chain stay tight: a tool that does not need SQLite
  does not pull it in.
- `internal/` shrinks to genuinely internal implementation (queue mechanics,
  compaction, metrics internals, CLI plumbing).

## Alternatives considered

- **One `sdk` umbrella package re-exporting everything.** Simple, but every
  extension inherits the full dependency graph and cycles become likely.
  Rejected.
- **Define interfaces in `navi` and have extensions import `navi`.** Couples
  every extension to the composition root. Rejected.