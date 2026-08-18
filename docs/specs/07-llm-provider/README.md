---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - llm-provider
priority: high
spec-refs: [070000]
---

# 07 LLM Provider

> LLM abstraction layer — unified provider interface, streaming, token counting, and provider selection.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S070000 LLM Provider Layer](00-llm-provider-layer.md) | LLM Provider Layer | draft | Provider interface, adapters, token counting, fallback |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Unified Provider interface with streaming support
- Provider adapters for Anthropic, OpenAI, OpenRouter, LM Studio
- Token counting, tool calling normalization, and retry logic

## Related

- [Agent Runtime](../01-agent-runtime/README.md)
