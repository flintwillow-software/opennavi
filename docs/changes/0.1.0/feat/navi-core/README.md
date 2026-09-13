# 0.1.0 — feat: navi-core

> Derived from the initial spec tree of component 00 (Navi Core) and informed by codebase analysis. Groups the foundational platform changes for the OpenNavi SDK.

## Summary

Implement the core OpenNavi platform: navi definition format, project layout conventions, builder/compiler pipeline, interface set (CLI/TUI/Gateway), trust & signing model, binary distribution, and extension contract. This is the foundation that all other components build on.

> Update (2026-09-13, go-first authoring per #263 / S000207): navi definitions are **Go-only** — authors call `sdk.Build(naviDef{...})` (S000200 §01); there is no `navi.yaml` and no YAML definition parsing anywhere. The public surface is the SDK layout of S000800 §01 (`sdk/`, `agent/`, `tools/`, `store/`, `llm/` public; `internal/` private) rather than a `pkg/` tree. Starter templates: `templates/navi-code/` and `templates/navi-daemon/` (S000313).

## Changes

- **`sdk/`** — Public SDK entry point: `sdk.Build(naviDef{...})` and the definition types (name, version, tools, skills, personality, interfaces, extensions, permissions).
- **builder/compiler** — Takes a Go navi definition (via `sdk.Build`) → produces a compiled binary with all dependencies compiled in.
- **`pkg/interfaces/`** — Interface set abstraction: CLI (cobra/spf13), TUI (Bubble Tea), Gateway (HTTP/gRPC). Selected at build time, compiled into binary.
- **`pkg/trust/`** — Trust & signing model: Code signing key generation, binary signing, signature verification on first run.
- **`pkg/project/`** — Project layout conventions: Standard directory structure for navi projects per S000100 (`cmd/<navi>/main.go` entry point; runtime config under `.config/navi/`).
- **`pkg/extensions/`** — Extension contract: Plugin interface for loading third-party tools/providers at build time.
- **`cmd/navi-init/`** — Scaffolding: `navi init` command that creates a new navi project from an embedded starter template (`--template code|daemon`, default `code`).
- **`Makefile`** — Build pipeline: Builder invocation, signing step, cross-platform compilation targets, Homebrew tap publishing.

## Spec Refs

[000000, 000100, 000200, 000300, 000400, 000500, 000600, 000700]

## Code Context

- **Project structure**: Go-based SDK. No existing code yet — this is the initial build.
- **Dependencies**: cobra (CLI), Bubble Tea (TUI), gRPC (Gateway), libsodium or similar (signing), spf13/viper (config)
- **Existing patterns**: N/A — greenfield build

## Notes

- Forms the epic "OpenNavi Platform Foundation"
- All other components depend on navi-core being implemented first (Go definition types, project layout, builder)
- Sub-issues can parallelize: interface implementations (CLI vs TUI vs Gateway) are independent once the interface abstraction is defined

## Tracked

> Historical: issues below were filed against the original navi.yaml-based plan and are superseded by the go-first restructure above; Huly (NAVI-48) is the canonical tracker.

- Epic: OpenNavi Platform Foundation (#138) — https://github.com/flintwillow-software/opennavi/issues/138 [priority: P0]
  - Issue: Implement core navi definition data model (navi.yaml) (#139) — https://github.com/flintwillow-software/opennavi/issues/139 [priority: P0]
  - Issue: Implement builder/compiler pipeline (#140) — https://github.com/flintwillow-software/opennavi/issues/140 [priority: P0]
  - Issue: Implement interface set abstraction (CLI/TUI/Gateway) (#141) — https://github.com/flintwillow-software/opennavi/issues/141 [priority: P0]
  - Issue: Implement trust & signing model (#142) — https://github.com/flintwillow-software/opennavi/issues/142 [priority: P0]
  - Issue: Implement project layout conventions (#143) — https://github.com/flintwillow-software/opennavi/issues/143 [priority: P0]
  - Issue: Implement extension contract (#144) — https://github.com/flintwillow-software/opennavi/issues/144 [priority: P0]
  - Issue: Implement scaffolding tool (navi init) (#145) — https://github.com/flintwillow-software/opennavi/issues/145 [priority: P0]
  - Issue: Implement build pipeline & Makefile (#146) — https://github.com/flintwillow-software/opennavi/issues/146 [priority: P0]
