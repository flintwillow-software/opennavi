---
status: proposed
date: '2026-09-24'
supersedes: []
spec-refs: [010104, 070000]
tags:
  - adr
  - llm-provider
  - thought
---

# 0009 — Providers are a set with a configurable default; thoughts select per-run

## Context

Provider selection today is config-only: `internal/daemon.Assemble` builds the
primary provider and optional fallback from `config.ProviderConfig`. There is no
way for a thought to choose a different model for different work, even though
`Thought.Provider` and `Thought.Model` per-thought overrides already exist and
the runner already resolves them. The navi also has no way to discover what is
available.

Different work wants different models: image generation, strong coding, cheap
summarisation. A single global default cannot express that.

## Decision

- **The navi declares a set of models/providers** at build time; one is marked
  default:
  ```go
  n.Models.Add(model.Ollama("qwen3.8:27b", model.Default()))
  n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4"))
  ```
- **Runtime config sets the active default and overrides endpoints/credentials**
  (ADR 0008), following the merge chain.
- **A built-in tool exposes the available set** (`model_list`), so the agent can
  reason about which model to use.
- **Thoughts select provider/model per run** via existing overrides:
  ```go
  n.Thoughts.Add(prompt,
      navi.WithProvider("openrouter"),
      navi.WithModel("anthropic/claude-sonnet-4"),
  )
  ```
  Unknown provider names fail the thought with a clear error; no silent
  fallback.
- **Fallback chains remain available** at the provider-config layer.

## Consequences

- One navi routes work to the right model dynamically — a planning thought can
  run cheap, an execution thought can escalate.
- Discovery (`model_list`) makes the choice available to the agent, not just to
  the operator.
- Extends, rather than replaces, the existing per-thought override fields and
  the fallback/retry layer.

## Alternatives considered

- **Single global default with a fallback.** Cannot express task-specific
  routing. Rejected.
- **Config-only selection (operator picks one model per deployment).** Wastes
  the per-thought override machinery and forces redeploys to change routing.
  Rejected.