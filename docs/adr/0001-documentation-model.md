---
status: accepted
date: '2026-09-24'
supersedes: []
tags:
  - adr
  - process
  - documentation
---

# 0001 — Documentation model: docs-as-code + ADRs + Huly

## Context

The opennavi spec tree (`docs/specs/`, 14 components / ~100 feature files),
the versioned change log (`docs/changes/`), and the case-study tree
(`docs/case-study/`) were written before Huly existed. At the time they had to
carry three distinct things at once:

1. the durable architecture and its rationale,
2. the requirement inventory (S-codes, SHALL statements),
3. the *planning* surface — what was proposed, what was in flight, what shipped.

Huly now owns (3). Keeping the spec tree means maintaining two planning tracks.
Worse, a large share of the spec tree describes code that does not exist — the
SDK authoring surface (`sdk.Build`) is a validation stub, and roughly half the
tree specifies unbuilt subsystems. Speculative requirements are not
maintenance; they go stale the moment the design moves and they give false
confidence about what the system does.

The repository is pre-1.0 with no external adopters. There is no compliance or
contract reason to maintain a stable requirement inventory today.

## Decision

Adopt a minimal, docs-as-code documentation model and retire the d3 document
pipeline for this repository:

- **Architecture description** lives in `docs/architecture/` — the current
  intended architecture, updated in the same PRs as code.
- **Decision records** live in `docs/adr/` — immutable, numbered MADR-style
  files. Decisions are superseded, never edited.
- **Work and planning** live in Huly — epics, issues, status, priority,
  assignee, blockers, roadmap.
- **Public API contract** is generated from source (`go doc` / pkgsite), not
  mirrored in prose.
- **Behavior** is specified by tests. Tests are the executable specification.
- `CHANGELOG.md` stays as release history.

Repository = durable knowledge ("what must be true" and "why"). Huly = ephemeral
state ("what's next, who, blocked"). Design prose never goes in Huly; work state
never lives in repo docs.

## Consequences

- The dual-truth problem ends: one source of truth per question.
- We give up the S-code requirement inventory. When the SDK reaches a stable
  public contract (1.0), add a focused, test-linked contract document for that
  surface only — not a tree.
- Documentation must move with code. A PR that changes architecture updates the
  relevant architecture doc or adds an ADR in the same review.
- The `d3-*` skills are not removed workspace-wide; other repositories may still
  choose a formal spec tree. This decision is scoped to opennavi.
- `docs/specs/`, `docs/changes/`, `docs/case-study/`, and `docs/templates/` are
  removed. Git history preserves their content.

## Alternatives considered

- **Keep d3, drop only `docs/changes/`.** Leaves the speculative spec tree and
  the requirement inventory as a second source of truth. Rejected.
- **Keep the spec tree as the durable "what", Huly for work.** This was the
  original split, but the tree's value (traceability for a stable contract)
  does not apply pre-1.0, and its cost (rot, duplication) applies today.
  Rejected for now; revisitable at 1.0.
- **Move design prose into Huly.** Huly is built for work items, not long-form
  architecture; prose there is unfindable and forks from the repo. Rejected.