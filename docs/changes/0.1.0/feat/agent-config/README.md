# 0.1.0 — feat: agent-config

> Derived from initial spec tree component 02 (Agent Config). Provides the configuration surface for navi agents.

## Summary

Implement agent configuration: YAML-based agent config file format, environment variable overrides, config merge strategy (defaults → file → env → CLI flags), and config validation against the navi definition schema.

## Changes

- **`pkg/config/`** — Config parser: Load and merge config from multiple sources (file, env, CLI flags). Validate against navi.yaml schema.
- **`pkg/config/provider/`** — Provider config: LLM provider configuration (endpoint, API key, model name, local/cloud mode).
- **`pkg/config/tools/`** — Tool config: Per-tool access control overrides, timeout configuration, allowed/blocked tool lists.
- **`internal/config/defaults/`** — Default configuration values baked into the binary at build time.

## Spec Refs

[020000, 020100]

## Code Context

- **Project structure**: Go SDK, depends on navi-core navi.yaml schema
- **Dependencies**: spf13/viper (config), in-cli flags
- **Existing patterns**: Standard layered config pattern (defaults → file → env → flags)

## Notes

- Issue: "Agent Configuration System"
- Depends on: navi-core (navi.yaml parsing for config schema)
- Relatively isolated — can be implemented independently once the navi.yaml schema is stable

## Tracked

- Epic: Agent Configuration System (#154) — https://github.com/flintwillow-software/opennavi/issues/154 [priority: P1]
  - Issue: Implement config parser & merge (#155) — https://github.com/flintwillow-software/opennavi/issues/155 [priority: P1]
  - Issue: Implement provider config (#156) — https://github.com/flintwillow-software/opennavi/issues/156 [priority: P1]
  - Issue: Implement tool config (#157) — https://github.com/flintwillow-software/opennavi/issues/157 [priority: P1]
  - Issue: Implement default configurations (#158) — https://github.com/flintwillow-software/opennavi/issues/158 [priority: P1]
