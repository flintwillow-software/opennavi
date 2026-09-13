// Package llm is the public provider layer (S000800 §01): the unified
// Provider contract every adapter implements (Ollama, OpenRouter, and
// the mock test double), streaming and completion types, the fallback
// chain, retry policy, and the Embedder interface.
//
// Provider adaptation is SDK-owned (S000800 §02): navi authors pick a
// provider via runtime config and never implement the transport.
package llm
