---
status: draft
created: '2026-09-24'
updated: '2026-09-24'
tags:
  - architecture
  - security
  - permissions
---

# Security & Trust

> How a navi's capability is bounded, how credentials are handled, and what a
> binary signature attests to. Decisions: ADR
> [0005](../adr/0005-build-time-modules-are-the-extension-path.md),
> [0006](../adr/0006-compile-time-omission-plus-runtime-ceiling.md),
> [0008](../adr/0008-secrets-interpolated-never-literal.md).

## The model in one sentence

**A navi can only do what was linked into its binary, and only within the
permissions its binary declares.**

Two independent mechanisms compose:

1. **Compile-time omission.** Go statically links exactly the code reachable
   from `main`. A navi that never imports a `todo_create` tool module physically
   lacks the capability. The `go.mod` file records what was linked.
2. **Runtime ceiling.** The compiled permission policy and the active tool set
   gate what the linked code may be invoked to do. Runtime config and
   per-deployment policy may only *tighten* this ceiling.

Either alone is insufficient: omission gives no per-thought restriction; a
policy alone still ships dangerous code.

## Granular packaging

The catalog ships granular tools so authors can omit dangerous operations
rather than trust policy to restrain them:

```go
n.Tools.Add(tools.TodoRead(store))
n.Tools.Add(tools.TodoUpdate(store))
// todo_create and todo_delete are never imported — absent from the binary
```

What is not imported cannot be reached by a prompt, a mode, or a misconfigured
policy. This is the primary security control.

## Permissions

- The compiled `.Permissions` policy is the **ceiling**.
- `.config/navi/<name>/policy.yaml` and runtime config may only add restrictions.
- **Deny wins over allow.**
- Granting a navi more than its binary declares requires rebuilding and
  re-signing — never a config edit.

```
compiled policy  ── ceiling ──▶  runtime policy  ── effective ──▶  tool call
                                 (restrict only)
```

## Thought modes

Modes (ADR 0007) narrow capability per thought — e.g. `plan` is read-only.
A mode is always a subset of the compiled permission ceiling; it cannot
escalate. Modes let one binary present a safe planning surface and a full
execution surface without shipping two binaries.

## Provenance and audit

Every tool declares its origin (`builtin`, `sdk`, `author`, `extension`) and,
for build-time modules, its module path and version. Populated from
`runtime/debug.ReadBuildInfo()`, a running navi can report exactly which module
provided each capability. `go.sum` plus the binary signature form the
supply-chain record.

## Secrets

- Secrets are **never** literals in source, tests, or fixtures.
- Components declare credential **references** (env var name or file path),
  with a default name.
- Values are interpolated at runtime via the config merge chain
  (`defaults → file → env → flags`).
- Endpoint defaults may be compiled in; the operator may override them.
- Credential resolution is **fail-closed**: a missing credential disables the
  component with a clear error rather than silently degrading.

```go
connector.Discord(connector.Config{Credential: "DISCORD_TOKEN"})
```

## Trust boundary

What a signature attests to:

- the exact source and dependency graph that produced the binary,
- the compiled permission policy, tools, models, and connectors,
- the absence of any capability not linked in.

What it does **not** attest to (runtime, operator-controlled):

- credential values and endpoint overrides,
- runtime policy tightening,
- schedules.

Runtime plugins (S000600) are deferred (ADR 0005). If introduced, they register
into the same tool registry under a distinct origin and require an explicit
allow-signers declaration; they never widen the compiled policy.

## Related

- [overview.md](./overview.md) — boundaries table
- [runtime.md](./runtime.md) — modes and thought lifecycle
- [extension-model.md](./extension-model.md) — third-party components and trust