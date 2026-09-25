---
status: proposed
date: '2026-09-24'
supersedes: []
spec-refs: [000200, 130400]
tags:
  - adr
  - security
  - permissions
---

# 0006 — Security model: compile-time omission plus a runtime ceiling

## Context

A distributed navi is a signed binary whose capability surface should be
verifiable. Two independent mechanisms can enforce what a navi can do, and they
compose:

1. **What is compiled in.** Go statically links exactly the code reachable from
   `main`. A navi that never imports `todo_create` physically lacks it. The
   `go.mod` file is the record of what was linked.
2. **What the runtime permits.** The compiled permission policy and the active
   tool set gate what the linked code may be invoked to do.

Either alone is weaker. Compile-time omission alone gives no per-thought
restriction; a runtime policy alone still ships dangerous code that could be
reached if the policy is misconfigured.

## Decision

Adopt both, and make the security boundary explicit:

- **Compile-time omission is a first-class security control.** Granular
  packaging is encouraged — a navi may link `todo_read` and `todo_update`
  without `todo_create` or `todo_delete`. What is not imported cannot run and
  cannot be reached by a prompt.
- **The compiled permission policy is a ceiling.** Runtime configuration and
  `.config/navi/<name>/policy.yaml` may only *tighten* it. Granting a navi more
  than its binary declares requires rebuilding and re-signing, never a config
  edit.
- **Deny wins over allow**, as today (S000200).
- **Provenance is declared** on every tool (`builtin`, `sdk`, `author`,
  `extension`) and exposed for audit.

## Consequences

- A navi's capability surface is the intersection of what was linked and what
  the policy permits; both are source-verifiable.
- The tool catalog should ship granular tools rather than coarse
  create-or-nothing bundles, so authors can omit dangerous operations.
- Operators cannot escalate a binary's capability at runtime; that is the
  intended trust property.
- This is a differentiator worth stating plainly in the README and security
  doc, because the opposite (runtime-granted capability) is common in other
  harnesses.

## Alternatives considered

- **Runtime policy as the sole control.** Ships capability that only policy
  restrains; a policy bug exposes it. Rejected.
- **Compile-time omission as the sole control.** Prevents per-thought or
  per-mode restriction (see ADR 0007). Rejected.