---
status: accepted
date: '2026-09-30'
supersedes: []
tags:
  - adr
  - context
  - skills
  - security
---

# 0013 — The thought queue is the context mechanism

## Context

The runtime's premise is that thoughts are **short-lived and bounded**: a thought
is a unit of intentional work, not a conversation that runs until the window
fills. That premise is what makes context manageable, and nothing has stated it.

What the code does today:

- **Assembly happens per turn, at run time.** `internal/agent`'s
  `TransformContext` converts messages, prepends `SystemPrompt`, injects
  `ContextFiles`, and applies provider transforms — on every turn of every run.
- **`ContextFiles` and `WorkDir` exist and are tested, but the navi never sets
  them** (`navi/runner.go` passes only provider, model, system prompt, and
  compaction). The file-source mechanism is built and unwired.
- **`Skills` is collected, validated, and frozen — and never read.** `n.Skills`
  appears only in `New`, `validateSkill`, and `freeze`, yet `runtime.md` claims
  skills are "baked into the system prompt at build time". The documentation is
  wrong in both directions: skills are neither baked nor loaded.
- **Compaction is opt-in and off by default** (`WithCompactBudget`,
  `--compact-budget` = 0), so the shipped behaviour already matches the intent
  below — but the machinery exists and is advertised as the answer to context
  limits.
- **Short-term memory does not exist yet** (NAVI-351).

The alternative — long-running thoughts salvaged by compaction — is the thing
this architecture exists to avoid. It makes a navi's behaviour a function of
summarization heuristics rather than of decisions about what work to do next.

## Decision

1. **The thought queue is the context mechanism.** Context pressure is resolved
   by bounding thoughts and *deferring* work into follow-up thoughts, not by
   compacting a long conversation. When a running thought approaches its budget
   it finishes a coherent unit, writes what the next thought needs into
   short-term memory, and queues follow-ups — declaring which skills those
   follow-ups should load. A task too large for one thought becomes a plan of
   thoughts, which is also what makes progress inspectable while it happens.

2. **Context is assembled at thought creation, not per turn.** The thought system
   owns execution, so assembly is a property of the thought: a queued thought
   carries its assembled context and the runner consumes it rather than
   rebuilding it. What was a rendering-time pipeline becomes a creation-time
   decision.

3. **The assembled context is recorded with the thought** as its first
   transcript entry (index 0). It is therefore readable through the same
   contract as the rest of the transcript (ADR 0011) and inspectable by any
   portal (ADR 0012). Frontmatter storage is *not* the contract — that is an
   `fs`-backend detail.

4. **Everything that shapes context is declared at build time.** Context
   sources, auto-loaded skills, and component-derived guidance are compile-time
   declarations. Runtime config may only narrow them (ADR 0006); it never adds a
   source. Users and the navi cannot change how a navi assembles context without
   a rebuild and a redeploy, and an owner gates that redeploy. **The rationale is
   the threat model: agent injection.** If a prompt can reach the assembler, then
   the assembler is an injection surface. It cannot.

5. **Skills are loaded selectively, never baked wholesale.** Three paths, all
   restricted to the compiled-in set:
   - **author-declared auto-load** — declared with the navi, always present;
   - **thought-declared request** — recorded when the thought is queued, so a
     thought can tell its follow-ups which skills to load;
   - **agent-chosen** — via a skill-load tool.

   Because skills are compiled in, the worst case of a manipulated skill request
   is loading a skill the author already shipped. A skill request is a menu
   selection, not a fetch.

6. **Core skills ship with the component types.** Adding a component type to a
   navi injects guidance telling the model what exists and how to use it. Both
   halves are required: **authored prose** for semantics, and a **mechanical
   inventory** derived from component metadata, so the model is never unaware of
   a capability the binary has.

7. **Compaction is not a mechanism.** It is removed rather than kept as a
   fallback. Deferral replaces it. If real usage later proves a need for a
   last-resort escape hatch, it is reintroduced under its own decision.

## Consequences

- **Context becomes deterministic and inspectable.** A thought's record shows the
  context it actually ran with, so "why did it do that" is answerable from the
  datastore rather than by speculation.
- **The runner stops assembling.** `internal/agent`'s per-turn responsibilities
  shrink; the system-prompt prepend remains a rendering detail.
- **Short-term memory becomes load-bearing.** It is the handoff that makes
  deferral work, so NAVI-351 is on the critical path for this decision and not a
  parallel nice-to-have — and it requires the datastore (ADR 0011).
- **No salvage path for an overrunning thought.** Without compaction, a thought
  whose assembled context exceeds the model's window fails rather than degrading.
  That is the intended trade: bounded thoughts are the control.
- **The compiled skill set is a capability surface**, greppable in `main.go` like
  tools and connectors.
- **`ContextFiles`/`WorkDir` need a home.** Either they become the file-source
  implementation under this decision, or they are deleted with compaction.

## Alternatives considered

- **Keep compaction as the safety net.** Rejected: it contradicts the premise,
  and a long conversation is precisely the thing the architecture avoids.
  Reintroducible later on evidence.
- **Runtime-configurable context sources.** Rejected: that is the injection
  surface this decision exists to close.
- **Bake every skill into the system prompt** (what `runtime.md` currently
  claims). Rejected: it spends context on every thought and turns the compiled
  skill set into a per-turn cost.
- **A separate context system alongside the thought system.** Deferred rather
  than rejected: the thought system already owns execution and is the vertical
  where this decision lives. Splitting now would create two owners of one
  decision. Revisit if assembly outgrows the thought.
- **Let portals mutate context assembly.** Rejected: portals observe and control
  scheduling. Rewriting how a navi assembles context is not a control action,
  and allowing it would reopen the injection surface.

## Open questions

1. **Budget.** Where a thought's token budget is declared, and whether overflow
   is a warning, a deferral trigger, or an assembly-time error.
2. **Format.** How authored skill prose and the mechanical component inventory
   compose into the assembled context.
3. **Timing.** Whether assembly at creation is synchronous with `Enqueue`
   (rejecting a thought that cannot be assembled) or deferred to claim time.
4. **The file source.** Whether `ContextFiles`/`WorkDir` survives as the
   mechanism for file content, or file content arrives only through
   skills/tools.
5. **Cross-thought context.** Whether a follow-up thought inherits any part of
   its predecessor's context by default, or only what short-term memory
   explicitly carries.

## Related

- [runtime.md](../architecture/runtime.md) — collections, thought system, agent runs
- [memory.md](../architecture/memory.md) — short-term memory as the inter-thought carrier
- [security.md](../architecture/security.md) — the injection boundary
- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — build-time omission, runtime ceiling
- [ADR 0011](./0011-persistence-is-a-datastore-component.md) — the thought as the persistence aggregate
- [ADR 0012](./0012-portals-attach-to-one-navi-stream.md) — how a portal reads a thought's context
