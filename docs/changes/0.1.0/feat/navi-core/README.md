# 0.1.0 — feat: navi-core

> Derived from the initial spec tree of component 00 (Navi Core) and informed by codebase analysis. Groups the foundational platform changes for the OpenNavi SDK.

## Summary

Implement the core OpenNavi platform: navi definition format (navi.yaml), project layout conventions, builder/compiler pipeline, interface set (CLI/TUI/Gateway), trust & signing model, binary distribution, and extension contract. This is the foundation that all other components build on.

## Changes

- **`pkg/navi/`** — Core navi definition data model: Parse and validate `navi.yaml` (name, version, tools, providers, identity, trust config).
- **`cmd/navi-build/`** — Builder/compiler: Takes a navi project + navi.yaml → produces a compiled binary with all dependencies compiled in.
- **`pkg/interfaces/`** — Interface set abstraction: CLI (cobra/spf13), TUI (Bubble Tea), Gateway (HTTP/gRPC). Selected at build time, compiled into binary.
- **`pkg/trust/`** — Trust & signing model: Code signing key generation, binary signing, signature verification on first run.
- **`pkg/project/`** — Project layout conventions: Standard directory structure for navi projects (agents/, tools/, providers/, navi.yaml).
- **`pkg/extensions/`** — Extension contract: Plugin interface for loading third-party tools/providers at build time.
- **`cmd/navi-init/`** — Scaffolding: `navi init` command that creates a new navi project from template.
- **`Makefile`** — Build pipeline: Builder invocation, signing step, cross-platform compilation targets, Homebrew tap publishing.

## Spec Refs

[000000, 000100, 000200, 000300, 000400, 000500, 000600, 000700]

## Code Context

- **Project structure**: Go-based SDK. No existing code yet — this is the initial build.
- **Dependencies**: cobra (CLI), Bubble Tea (TUI), gRPC (Gateway), libsodium or similar (signing), spf13/viper (config)
- **Existing patterns**: N/A — greenfield build

## Notes

- Forms the epic "OpenNavi Platform Foundation"
- All other components depend on navi-core being implemented first (navi.yaml parsing, project layout, builder)
- Sub-issues can parallelize: interface implementations (CLI vs TUI vs Gateway) are independent once the interface abstraction is defined

## Tracked

- Epic: OpenNavi Platform Foundation (#138) — https://github.com/flintwillow-software/opennavi/issues/138 [priority: P0]
  - Issue: Implement core navi definition data model (navi.yaml) (#139) — https://github.com/flintwillow-software/opennavi/issues/139 [priority: P0]
  - Issue: Implement builder/compiler pipeline (#140) — https://github.com/flintwillow-software/opennavi/issues/140 [priority: P0]
  - Issue: Implement interface set abstraction (CLI/TUI/Gateway) (#141) — https://github.com/flintwillow-software/opennavi/issues/141 [priority: P0]
  - Issue: Implement trust & signing model (#142) — https://github.com/flintwillow-software/opennavi/issues/142 [priority: P0]
  - Issue: Implement project layout conventions (#143) — https://github.com/flintwillow-software/opennavi/issues/143 [priority: P0]
  - Issue: Implement extension contract (#144) — https://github.com/flintwillow-software/opennavi/issues/144 [priority: P0]
  - Issue: Implement scaffolding tool (navi init) (#145) — https://github.com/flintwillow-software/opennavi/issues/145 [priority: P0]
  - Issue: Implement build pipeline & Makefile (#146) — https://github.com/flintwillow-software/opennavi/issues/146 [priority: P0]
