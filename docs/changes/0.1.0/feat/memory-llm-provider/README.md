# 0.1.0 — feat: memory and llm-provider

> Derived from initial spec tree components 06 (Memory) and 07 (LLM Provider). Implements agent memory/recall and the multi-provider LLM abstraction layer.

## Summary

Implement the memory subsystem (SQLite-backed conversation memory with recall) and the LLM provider abstraction (local via Ollama/LM Studio, cloud via Anthropic/OpenAI/OpenRouter, with unified streaming and token counting).

## Changes

- **`pkg/memory/`** — Memory store: Conversation history persistence to SQLite, semantic recall via embeddings, memory compaction/pruning.
- **`pkg/memory/index/`** — Memory index: Embedding-based search for relevant past conversations.
- **`pkg/llm/`** — LLM provider interface: Unified provider abstraction (chat completion, streaming, token counting).
- **`pkg/llm/anthropic/`** — Anthropic provider: Claude API integration with streaming.
- **`pkg/llm/openai/`** — OpenAI provider: GPT API integration with streaming.
- **`pkg/llm/openrouter/`** — OpenRouter provider: Multi-model routing via OpenRouter.
- **`pkg/llm/ollama/`** — Ollama provider: Local LLM inference via Ollama API.
- **`pkg/llm/lmstudio/`** — LM Studio provider: Local LLM inference via LM Studio API.
- **`pkg/llm/registry/`** — Provider registry: Provider selection, fallback chains, hybrid mode (local → cloud).

## Spec Refs

[060000, 070000]

## Code Context

- **Project structure**: Go SDK, depends on agent-runtime (for integrating with the agent loop)
- **Dependencies**: Ollama API client, LM Studio API client, Anthropic SDK, OpenAI SDK, OpenRouter API
- **Existing patterns**: Provider abstraction pattern — common interface, different backends

## Notes

- Epic: "Memory & LLM Provider Layer"
- LLM providers are independent of each other — can be built in parallel once the provider interface is defined
- Memory subsystem is optional at runtime (agent can operate without persistence)
- Local providers (Ollama, LM Studio) are critical for offline/private use cases