---
status: draft
created: '2026-09-24'
updated: '2026-09-30'
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
> [0008](../adr/0008-secrets-interpolated-never-literal.md),
> [0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md),
> [0014](../adr/0014-connectors-are-bidirectional-service-integrations.md).

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
todos, _ := todo.Tools(naviID, state)
for _, t := range todos {
    switch t.Def.Name {
    case "todo_read", "todo_update":
        n.Tools.Add(t)
    }
}
// todo_create and todo_delete are never declared — absent from the binary
```

What is not imported cannot be reached by a prompt, a mode, or a misconfigured
policy. This is the primary security control.

## Permissions

- The compiled `Permissions` policy is the **ceiling**.
- Runtime config may only add restrictions.
- **Deny wins over allow.**
- Granting a navi more than its binary declares requires rebuilding — and, once
  artifact signing lands, re-signing — never a config edit.

```
compiled policy  ── ceiling ──▶  runtime policy  ── effective ──▶  tool call
                                 (restrict only)
```

## Thought modes

Modes (ADR 0007) narrow capability per thought — e.g. `plan` is read-only.
A mode is always a subset of the compiled permission ceiling; it cannot
escalate. Modes let one binary present a safe planning surface and a full
execution surface without shipping two binaries.

## Context and the injection boundary

Agent injection is treated as the primary threat, not a content-filtering
problem. The control is that **nothing reachable from a prompt can change how a
navi assembles context** ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)).

- **Context sources are build-time declarations.** Runtime config may narrow
  them; it can never add one. Changing what a navi assembles requires a rebuild
  and a redeploy, and the owner gates that redeploy.
- **Skills are a menu selection, not a fetch.** All three loading paths —
  author-declared auto-load, thought-declared request, agent-chosen tool — can
  only select from the **compiled-in** set. A manipulated skill request can at
  worst load a skill the author already shipped. There is no path from a prompt
  to new instructions arriving at runtime.
- **Role separation is structural.** What a navi may do is determined by which
  components and tools exist in its binary, not by what its prompt says. Two
  navis with identical prompts but different component sets have different
  capabilities, and neither can be talked into using a tool it does not have
  ([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)).
- **The assembled context is recorded**, so what a thought actually ran with is
  auditable after the fact rather than inferred.

The residual risk is accepted and named: content *inside* a thought's context
(tool output, a fetched page, an external message) can still attempt to steer
the model. That is bounded by the capability surface, not by the assembler —
which is exactly why the capability surface is the thing kept small.

## Provenance and audit

Every tool declares its origin (`builtin`, `sdk`, `author`, `extension`) and,
for build-time modules, its module path and version. Populated from
`runtime/debug.ReadBuildInfo()`, a running navi can report exactly which module
provided each capability. `go.mod`/`go.sum` plus build info are the
supply-chain record; artifact signing is deferred (see below).

## Secrets

- Secrets are **never** literals in source, tests, or fixtures.
- Components declare credential **references** (env var name or file path),
  with a default name.
- Values are interpolated at runtime via the config merge chain
  (`defaults → file → env → flags`).
- Endpoint defaults may be compiled in; the operator may override them.
- Credential resolution is **fail-closed**: a missing credential disables the
  component's capability with a clear error rather than silently degrading. For
  a connector's egress tools that means the *capability* is disabled, not the
  navi: the connector stays declared (ADR 0014 decision 4) and each call refuses
  at the call site naming the reference, so one unconfigured integration cannot
  abort a run whose other capabilities are fine (ADR 0020 decision 7).

```go
model.OpenRouter("anthropic/claude-sonnet-4", model.WithCredential("OPENROUTER_API_KEY"))
```

## Egress

Outbound capability is a capability, and is bounded the same way inbound work is
([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)).

- **Egress lives in connector-owned tools.** A connector exposes its outbound
  operations as ordinary tool definitions the author adds explicitly via
  `connector.SendTool()` — never auto-registered. Every way a navi can talk
  outward is therefore visible in `main.go`, the same property tools and
  connectors already have.
- **Integrations are declared by composing the navi.** Which services a navi can
  reach is a build-time decision that assigns intent. IAM-style mode permissions
  narrow what a connected component may do; they never widen which services
  exist.
- **There is no generic egress payload schema.** Payloads are service-specific
  (a message, a reply, a reaction), so there is no shared envelope to
  accidentally over-share through.
- **Secrets are read at the call site only** — resolved inside the request that
  needs them, from a runtime reference (ADR 0008), and never written to the
  thought transcript, the datastore, or logs. A secret never appears in history
  because history is written where the secret is not read.
- **An egress tool must tolerate an unstarted connector**: tools are bound at
  authoring time but the client arrives at `Start`, so a call before the
  connector serves fails cleanly rather than dereferencing a nil client. This is
  enforced by the mechanism rather than trusted to each author: the connector
  publishes its client into a `connector.Live[C]` handle in `Start`, clears it
  in `Stop`, and `connector.SendTool` builds the gate into the handler, so the
  call returns `connector.ErrNotStarted` (ADR 0020).
- **An egress tool cannot hide what it does.** `SendTool` refuses a descriptor
  with no name, no namespace, no access level, no input schema, or no recovery
  strategy, and stamps the tool's provenance from its namespace — so "what can
  this navi reach, and through which component?" is answerable from the catalog
  rather than by reading every handler.

## Trust boundary

Artifact signing is **not implemented yet**; the supply-chain record today is
the module graph (`go.mod`/`go.sum`) plus build info. When signing lands
(deferred, ADR 0005), a signature will attest to:

- the exact source and dependency graph that produced the binary,
- the compiled permission policy, tools, models, and connectors,
- the absence of any capability not linked in.

What it does **not** attest to (runtime, operator-controlled):

- credential values and endpoint overrides,
- runtime policy tightening,
- schedules.

Runtime plugins are deferred (ADR 0005). If introduced, they register
into the same tool registry under a distinct origin and require an explicit
allow-signers declaration; they never widen the compiled policy.

## Related

- [overview.md](./overview.md) — boundaries table
- [runtime.md](./runtime.md) — modes and thought lifecycle
- [extension-model.md](./extension-model.md) — third-party components and trust