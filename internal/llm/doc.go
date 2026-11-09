// Package llm is the public provider layer: the unified
// Provider interface every adapter implements (Ollama, OpenRouter, and
// the mock test double), streaming and completion types, the fallback
// chain, and retry policy.
//
// The embedding contract is NOT here. model.Embedder is the interface and
// model/ollama the implementation (ADR 0040 decision 1): an embedder is
// supplied by the navi author at construction, so it has to live in a package
// an out-of-repo author can import, and this one is internal.
//
// Provider adaptation is SDK-owned: navi authors pick a
// provider via runtime config and never implement the transport.
package llm
