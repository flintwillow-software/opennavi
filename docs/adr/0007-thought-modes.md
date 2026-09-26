---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000200, 010104, 130400]
tags:
  - adr
  - security
  - thought
---

# 0007 — Thought modes: named tool/permission/model sets per thought

## Context

Permission policy and the active tool set are today global to a navi run. But
the appropriate capability depends on what a thought is *doing*: a planning
thought should be read-only, while an execution thought may write and run
commands. The registry already supports this via `ToolAccess` levels and
`FindFilter.MaxAccess`, and `Thought` already carries per-thought `Provider`
and `Model` overrides. There is no way to name and select a combination.

## Decision

Introduce **thought modes** — named sets of tools, permissions, and optional
model/prompt bindings that a thought selects:

```go
type Mode struct {
    Name        string
    Tools       []string          // active subset; empty = all
    Permissions PermissionPolicy
    Model       string            // optional: mode implies model
    Prompt      string            // optional system-prompt fragment
}

n.Modes.Add(navi.PlanMode)      // read-only
n.Modes.Add(navi.ExecuteMode)   // write/execute
```

- A `Thought` carries a mode; the runner resolves the tool surface, permission
  ceiling, and model from it.
- Modes compose with per-thought provider/model overrides: "run this in plan
  mode on the cheap model; escalate to execute mode on the coding model."
- Built-in modes (`plan`, `execute`) ship with the SDK; authors may define
  their own.
- The mode's permission set is subject to the compiled policy ceiling
  (ADR 0006): a mode can only restrict, never escalate.

## Consequences

- One navi can serve qualitatively different work without shipping duplicate
  binaries.
- Safety becomes orthogonal to capability: the same linked tool set yields a
  read-only planning surface and a full execution surface by mode.
- Adds a concept to `Thought`, the runner, and the public surface; requires a
  built-in `mode` selector in the thought-management tools and portals.

## Alternatives considered

- **Per-thought tool lists only.** No named, reusable, author-defined sets and
  no prompt/model binding. Rejected as the primary mechanism.
- **Separate navis per capability level.** Duplicates binaries and loses the
  shared thought/history. Rejected.