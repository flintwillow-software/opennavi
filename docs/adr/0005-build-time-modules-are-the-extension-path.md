---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000200, 000600]
tags:
  - adr
  - extensions
  - security
---

# 0005 — Build-time Go modules are the extension path; runtime plugins are deprioritized

## Context

The spec describes runtime extensions (S000600): separately compiled, signed
binaries loaded over JSON-RPC with an allowed-signers trust model. Runtime
plugins make sense for end-user-installed capability without a rebuild, but
they are weaker on security, consistency, and maintainability: provenance is
harder to trace, capability is spread across a plugin directory, and behavior
can vary per install.

A build-time Go module gives a stronger property for the same need: the
capability surface is exactly the repo's `go.mod` + `main.go`, verifiable from
the source and attested by the binary signature. In production you read the
source and know what the navi can do.

## Decision

**Build-time Go modules are the primary extension path.** Tools, skills,
models, connectors, and portals are distributed as ordinary Go modules that a
navi repo imports and pins. Runtime plugins are deprioritized for core
capability.

- Provenance is the module path and version; populate `SourceInfo` from
  `runtime/debug.ReadBuildInfo()` so a running navi can report which module
  provided each tool.
- `go.sum` plus the binary signature form the supply-chain record.
- If runtime plugins return, they register into the same tool registry under a
  distinct `Origin` and an explicit allow-signers declaration — one registry,
  two trust levels. This is deferred, not forbidden.

## Consequences

- Adding capability requires a rebuild. This is the accepted cost of
  consistency and auditability.
- Capability audit is "read `main.go` and `go.mod`", not "inspect a plugin dir
  plus config at each layer."
- The S000600 extension interface is out of scope until a concrete need for
  no-rebuild capability appears.

## Alternatives considered

- **Runtime plugins first.** Rejected for the reasons above; the security and
  consistency cost is paid by every navi, while the no-rebuild benefit applies
  to a minority of deployments.
- **Go's `plugin` package.** Requires identical toolchains and dependency
  versions, is fragile and non-portable. Rejected outright.