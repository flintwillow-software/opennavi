---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - navi-core
priority: high
spec-refs: [000000, 000100, 000200, 000300, 000400, 000500, 000600, 000700]
---

# 00 Navi Core

> The foundation of the OpenNavi platform — what a navi is, how it's defined, built, packaged, and trusted.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S000000 Platform Overview](00-overview.md) | Platform Overview | draft | Vision, trust model, architecture |
| [S000100 Project Layout](01-navi-project-layout.md) | Project Layout | draft | Go project conventions, directory structure |
| [S000200 Navi Definition](02-navi-definition.md) | Navi Definition | draft | Definition format, components, validation |
| [S000300 Builder & Compiler](03-builder-compiler.md) | Builder & Compiler | draft | Build toolchain, interface selection, distribution |
| [S000400 Interface Set](04-interface-set.md) | Interface Set | draft | Delivery modes, interface selection, configuration |
| [S000500 Trust & Signing](05-trust-signing-model.md) | Trust & Signing | draft | Code signing, verification, extension trust |
| [S000600 Extension Contract](06-extension-contract.md) | Extension Contract | draft | Plugin protocol, sandboxing, lifecycle |
| [S000700 Binary Distribution](07-binary-distribution.md) | Binary Distribution | draft | Homebrew tap, direct download, signing, updates |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Compiled binaries, not runtime loading — tools and skills compiled into the navi at build time
- Interface selection at build time — only selected delivery modes are compiled in
- Extensions as signed standplugins — separate binaries with their own trust model
- Signature verification before execution — users verify before running

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
- [Agent Config](../02-agent-config/README.md)
- [Messaging](../05-messaging/README.md)
- [Operations](../08-operations/README.md)
