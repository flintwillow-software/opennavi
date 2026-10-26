---
status: proposed
date: '2026-10-26'
supersedes: []
amends: []
tags:
  - adr
  - navi
  - policy
  - authorization
  - sdk
  - testing
---

# 0033 — Compiling and inspecting the policy without serving

## Context

ADR 0021 decision 11 makes the authorization policy **compiled**: the ceiling is *derived*
from what a navi declares (every tool, every exposed type with its fields and actions), and
the declared roles and groups are checked against that ceiling once, at assembly, so a policy
naming something undeclared fails the run rather than being discovered per request. ADR 0022
decision 1 puts every enforcement point — attachment, object read, object action, tool call,
and enqueue — behind that one engine and one acting principal.

What the SDK does not offer is a way to reach that engine **without serving**. `compilePolicy`
is unexported and runs only inside `Run` (via `assemble`). `Policy()` exists but returns nil
until `Run` has assembled the navi, so an out-of-repo navi cannot compile its policy and ask
an authorization question at composition time.

That gap is not theoretical. Found while fixing NAVI-407: `navi-bastilli`'s operator role
granted no tool call, so a chat session — which runs as the enqueuing operator, not as the
navi (ADR 0022 decision 3) — had every tool refused at run time with
`no role or group grants call:<tool>`. The composition test could not catch it, because there
was no way to compile the policy and evaluate a decision; the test asserted on the *declared*
role's grant list instead. That assertion is weaker in three specific ways:

- it does not exercise the **derived ceiling**, so a `call:*` grant (which
  `CeilingFromDeclarations` rejects, because it lists tools by name) reads as covering
  everything to the test while failing the run — the first attempt at the fix used the
  wildcard and the service crash-looped;
- it does not exercise the **session layer** (ADR 0026), so a mode that would deny a call at
  run time is invisible to it;
- it does not exercise the **conditions** on a grant (ADR 0021 decision 11), so a grant
  scoped to a transport or a channel reads as unconditional to the test.

The pieces needed already exist and are public: `Policy`, `Policy.Decide`, `AccessRequest`,
`NewPrincipal`, `Call`, and `CeilingFromDeclarations`. What is missing is the compile step
and a navi-level way to reach it that does not require `Run`.

The question is where the compile step belongs. `Validate()` is the natural candidate — it is
already the documented "check a navi without running it" seam — but it currently checks shape
(duplicate names, mode references, portal addresses, default model) and touches no policy. The
alternatives are a separate exported `CompilePolicy()`, or a lazily-compiling `Check` on the
navi.

One fact decides much of this: **compilation depends only on declarations.** The ceiling is
derived from `n.Tools` and `n.Types`, both populated by `New` and the author's `Add` calls;
the roles and groups are `n.Roles` and `n.Groups`. Nothing in the compile step reads runtime
state — no datastore, no registry, no trust source, no clock. Compilation is therefore
available the moment the declarations are complete, is idempotent, and is cheap.

## Decision

1. **`CompilePolicy()` is exported** — `func (n *Navi) CompilePolicy() error`. It derives the
   ceiling from the navi's declarations and compiles the declared roles and groups against
   it, storing the result where `Policy()` reads it. It is the one compile step; `assemble`
   calls it rather than duplicating the logic.

2. **`Validate()` compiles as its final step.** An author who validates gets a usable policy:
   `n.Validate()` then `n.Policy().Decide(...)` is the composition-time check. This does not
   change what `Validate` *promises* — it already claims to be "the cross-cutting check Run
   performs", and `Run` already treats a compile failure as fatal — it makes the promise
   true for the policy as well as the shape. `Run` still compiles at assembly; compiling
   twice is harmless because the step is idempotent.

3. **`Policy()` keeps its meaning and loses its caveat.** It returns the compiled policy, or
   nil when nothing has compiled yet. The nil case is now "not yet compiled" rather than
   "not yet assembled", and both `Validate()` and `CompilePolicy()` clear it. `Run` is no
   longer the only path to a non-nil policy.

4. **`Decide` stays on `Policy`, and there is no lazy `Check` on the navi.** The composition
   test reaches the engine through `Validate()` + `Policy().Decide(AccessRequest{...})`. A
   navi-level `Check` that compiles lazily was rejected (see Alternatives): it would make the
   policy's validity depend on *when* it was first asked, so a role declared after the first
   `Check` would be silently absent from every later decision. Compilation is a step with a
   place in the sequence, and the sequence is the author's to state.

5. **A compile failure is fatal at `Validate`, exactly as it is at `Run`.** The referential
   rule is not weakened for the non-serving path: a role or group naming an undeclared tool,
   type, field, or action fails `Validate` with `ErrInvalid`, naming the role and the
   capability. An author who validates cannot ship a policy the run would reject.

6. **The portal-action path is covered by the same seam.** `Authorize(action, actor)` already
   exists and is public; with the policy compiled by `Validate`, it answers at composition
   time as it does when serving. No second entry point is added for portal actions.

## Consequences

**Made easy.** An out-of-repo navi can assert a real decision for every declared tool at
composition time, which is the acceptance criterion of NAVI-565:

```go
if err := n.Validate(); err != nil {
    return err
}
who, err := navi.NewPrincipal(principal.MustParse("user:operator"))
if err != nil {
    return err
}
for _, t := range n.Tools.All() {
    if d := n.Policy().Decide(navi.AccessRequest{
        Principal:  who,
        Capability: navi.Call(t.Def.Name),
    }); !d.Allowed {
        return fmt.Errorf("operator cannot call %s: %s", t.Def.Name, d.Reason)
    }
}
```

The assertion now covers the derived ceiling, the role and group layers, and any conditions
on the grants — the three things the declared-grant assertion could not see. The `call:*`
wildcard that crash-looped a service fails this test with `is outside the compiled ceiling`.

**Made harder.** `Validate` now has a side effect: it mutates the compiled policy. A caller
that treats `Validate` as a pure check and expects `Policy()` to stay nil will be surprised,
and a navi whose declarations change *after* `Validate` holds a policy compiled from the
earlier set — `CompilePolicy()` must be called again. The staleness is bounded and detectable
(the policy is a snapshot, not a live view), and `Run` recompiles unconditionally, so a
serving navi is never stale. It is the price of not adding a second, parallel validity
concept.

**Made impossible.** A test can no longer "prove" a role works by reading its grant list and
stopping there — not because the API forbids it, but because the cheaper assertion is now
strictly weaker than the one the SDK supports. And a policy that exceeds the ceiling can no
longer reach a run: it fails at whichever of `Validate` or `Run` the author calls first.

**Cost.** One exported method, one call added to `validate`, and a doc-comment correction on
`Policy()`. No new types, no new package, no runtime behavior change.

## Alternatives considered

- **A separate `CompilePolicy()` only, leaving `Validate` untouched.** Rejected as the *only*
  move: it works, but it puts the compile step outside the seam whose entire purpose is
  "check this navi without running it", so every author must know to call two methods in the
  right order. It is kept as *part* of the decision (point 1) because it is the explicit
  re-compile path after a late declaration.
- **A lazily-compiling `Check(principal, capability) Decision` on the navi.** Rejected: the
  first call fixes the policy, so a declaration that lands after it is silently absent from
  every later decision — a fail-open shape, in the one subsystem that must fail closed. It
  also hides the compile error inside a call whose signature has no room to return it
  (`Decision` has no error, and a refused decision is not the same as an uncompilable policy).
- **Compiling in `New`.** Rejected: nothing is declared yet, so the policy would be empty and
  the ceiling would contain only the built-in types.
- **Compiling in every `Add`.** Rejected: `Add` returns an error the author may ignore, and
  re-deriving the ceiling on every declaration is wasted work for a result that is only
  meaningful once the declarations are complete.
- **Exposing the ceiling alone (`Ceiling()`) and letting the author compile.** Rejected: the
  author would have to call `NewPolicy(ceiling, roles, groups)` and would have to remember
  that roles and groups are validated against the ceiling — re-implementing `compilePolicy`,
  which is exactly the duplication the SDK should own.

## Open questions

- **Should a compile failure be fatal for a *non-serving* navi in every case?** It is fatal at
  `Validate` and at `Run` today. A future "explain" mode that reports every policy problem at
  once (rather than the first) would want the compile step to accumulate errors; the current
  step returns the first, matching `Run`.
- **Is there a role for a `Decision`-returning helper that also carries the request context a
  portal binding would supply?** `AccessRequest` already takes a `RequestContext`, so a test
  may construct one, but the *facts* a real binding supplies (transport, channel, source
  address) are the binding's to know. Whether the SDK should offer a fixture for them, or
  leave each navi to name its own, is not decided here.
- **Does `Validate` compiling make the method name misleading?** It validates *and* compiles.
  A future rename (`Prepare`, `Check`) would be a breaking change for a pre-1.0 API and is
  deliberately not taken now.
