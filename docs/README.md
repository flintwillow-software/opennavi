# OpenNAVI Documentation

Design and architecture documentation for OpenNAVI — an SDK for building
**navis**, self-contained AI agent binaries.

## Where things live

| Question | Where |
|---|---|
| What is the architecture, and why is it shaped this way? | [`architecture/`](./architecture/) |
| Why did we make a specific tradeoff? | [`adr/`](./adr/) — numbered, immutable decisions |
| What's next, who owns it, what's blocked? | **Huly** (tracker) |
| What does the public API look like? | `go doc` / pkgsite (generated from source) |
| What does the system do? | Code and tests (tests are the executable specification) |
| What changed in a release? | [`../CHANGELOG.md`](../CHANGELOG.md) |

Durable design lives in this repository. Work state lives in Huly. See
[ADR 0001](./adr/0001-documentation-model.md) for the rationale.

## Architecture

| Document | Covers |
|---|---|
| [overview.md](./architecture/overview.md) | What a navi is, C4 context, package layout, boundaries |
| [runtime.md](./architecture/runtime.md) | The navi object, collections, thought system, modes, providers, lifecycle |
| [security.md](./architecture/security.md) | Permission ceiling, compile-time omission, secrets, trust |
| [extension-model.md](./architecture/extension-model.md) | Out-of-repo tools/skills/models/connectors/portals and navis |
| [rewrite-plan.md](./architecture/rewrite-plan.md) | Migration milestones (M0–M7) |

## Decision records

All records below are **accepted**. ADRs are immutable: to change a decision,
add a new ADR that supersedes the old one.

| ADR | Decision | Status |
|---|---|---|
| [0001](./adr/0001-documentation-model.md) | Documentation model: docs-as-code + ADRs + Huly | accepted |
| [0002](./adr/0002-navi-is-a-composition-root.md) | The navi is a composition root with typed collections | accepted |
| [0003](./adr/0003-thought-system-first-class.md) | The thought system is first-class and public | accepted |
| [0004](./adr/0004-contract-packages-public-implementations-private.md) | Contract packages public; implementations private | accepted |
| [0005](./adr/0005-build-time-modules-are-the-extension-path.md) | Build-time modules are the extension path | accepted |
| [0006](./adr/0006-compile-time-omission-plus-runtime-ceiling.md) | Compile-time omission + runtime ceiling security model | accepted |
| [0007](./adr/0007-thought-modes.md) | Thought modes | accepted |
| [0008](./adr/0008-secrets-interpolated-never-literal.md) | Secrets interpolated at runtime, never literals | accepted |
| [0009](./adr/0009-providers-set-with-per-thought-selection.md) | Providers are a set; thoughts select per run | accepted |
| [0010](./adr/0010-three-capability-layers.md) | Three capability layers: tools, connectors, portals | accepted |

## Conventions

- Architecture docs are updated in the same PR as the code they describe.
- ADRs are immutable: to change a decision, add a new ADR that supersedes the
  old one.
- Work items are created and tracked in Huly, not in repository documents.