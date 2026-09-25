---
status: proposed
date: '2026-09-24'
supersedes: []
spec-refs: [010003]
tags:
  - adr
  - agent-runtime
  - thought
---

# 0003 — The thought system is a first-class, public part of every navi

## Context

The thought queue is the navi runtime's main loop (the "house model"): every
door — TUI, connectors, tools that schedule work — feeds the same queue, and no
path drives the agent around it. Today that queue lives in `internal/thought`
and is assembled only by `internal/daemon`. External authors cannot embed it,
and connectors have no supported handle to it. The queue's own API
(`Enqueue`, `GetThoughts`, `GetThought`, `RouteMessage`, `OnEvent`,
pause/resume/cancel/retry) already is the connector contract; it is simply
private and unnamed.

The thought system is what separates a navi from a generic agent harness. It is
not optional.

## Decision

The thought system is core to the navi base object and is always present; it is
not something an author adds.

```go
n.Thoughts.Add(prompt, navi.WithProvider("openrouter"), navi.WithModel("..."))
n.Thoughts.List()
n.Thoughts.Get(id)
n.Thoughts.Route(id, message)   // lifecycle-aware delivery
n.Thoughts.Subscribe(fn)
n.Thoughts.Pause(id) / Resume(id) / Cancel(id) / Retry(id)
```

- The `thought` package becomes public and is a leaf (it must not import `navi`).
- `navi.Navi` owns a thought system and exposes it as `n.Thoughts`.
- Connectors and portals receive a thought-system handle through their
  dependency contract, never through an import of `navi`.
- The existing persistence, crash recovery, slot/preemption, foreground-slot,
  and routing semantics are preserved; this is a visibility and ownership move,
  not a redesign of the queue.

## Consequences

- Connectors can spawn and manage thoughts; portals can observe and steer them.
  Both work against a stable public interface.
- `internal/thought` moves to public `thought/`, which carries semver
  obligations. Queue internals that are not part of the contract stay
  unexported; the public surface is the handle API plus event types.
- Every navi — daemon-style or binary-style — runs through the same loop by
  construction, not by convention.

## Alternatives considered

- **Expose a narrow SDK interface and inject the private implementation.**
  Attractive for keeping implementation freedom, but the queue's semantics are
  the product here, and hiding the type makes the public API harder to
  document. Rejected in favor of a public `thought` package with a narrow
  exported surface.