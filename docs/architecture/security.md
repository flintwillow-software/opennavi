---
status: draft
created: '2026-09-24'
updated: '2026-09-30'
tags:
  - architecture
  - security
  - permissions
---

# Security & Trust

> How a navi's capability is bounded, how credentials are handled, and what a
> binary signature attests to. Decisions: ADR
> [0005](../adr/0005-build-time-modules-are-the-extension-path.md),
> [0006](../adr/0006-compile-time-omission-plus-runtime-ceiling.md),
> [0008](../adr/0008-secrets-interpolated-never-literal.md),
> [0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md),
> [0014](../adr/0014-connectors-are-bidirectional-service-integrations.md).

## The model in one sentence

**A navi can only do what was linked into its binary, and only within the
permissions its binary declares.**

Two independent mechanisms compose:

1. **Compile-time omission.** Go statically links exactly the code reachable
   from `main`. A navi that never imports a `todo_create` tool module physically
   lacks the capability. The `go.mod` file records what was linked.
2. **Runtime ceiling.** The compiled permission policy and the active tool set
   gate what the linked code may be invoked to do. Runtime config and
   per-deployment policy may only *tighten* this ceiling.

Either alone is insufficient: omission gives no per-thought restriction; a
policy alone still ships dangerous code.

## Granular packaging

The catalog ships granular tools so authors can omit dangerous operations
rather than trust policy to restrain them:

```go
todos, _ := todo.Tools(naviID, state)
for _, t := range todos {
    switch t.Def.Name {
    case "todo_read", "todo_update":
        n.Tools.Add(t)
    }
}
// todo_create and todo_delete are never declared — absent from the binary
```

What is not imported cannot be reached by a prompt, a mode, or a misconfigured
policy. This is the primary security control.

## Permissions

- The compiled `Permissions` policy is the **ceiling**.
- Runtime config may only add restrictions.
- **Deny wins over allow.**
- Granting a navi more than its binary declares requires rebuilding — and, once
  artifact signing lands, re-signing — never a config edit.

```
compiled policy  ── ceiling ──▶  runtime policy  ── effective ──▶  tool call
                                 (restrict only)
```

## Identity and trust

Authorization answers "may this principal do this?"; identity answers "who is
this?". They are separate mechanisms with separate records: **capability is
compiled, identity is resolved at runtime** (ADR 0021).

- A **principal** is an identity with authenticators attached. Kinds are open
  (`user`, `service`, `connector`, `navi`, `group`), and a principal holds a set
  of keys, so rotating or revoking a key never changes who the principal is. A
  **role** is a compiled capability set and is not a principal; a **group** is a
  named collection of principals, optionally authenticatable itself.
- **Trust sources are declared at build time and resolved at runtime**, exactly
  as a credential reference is (ADR 0008). A navi declares its sources in
  `main.go`: principals inline (`navi.NewPrincipals`), an OIDC identity provider
  (`navi.NewOIDC`), a keyed trust store (`navi.NewFileTrustStore`), or a
  combination in one ordered `navi.TrustSources` set.
- **The provider supplies identity and claims; the navi supplies policy.** An
  identity provider that could supply policy would be runtime-granted
  capability, which compile-time omission forbids.
- **Credentials are typed by principal kind — and a key belongs to any kind.**
  Humans normally authenticate through an OIDC session, and services and peer
  navis present an Ed25519 keypair; but the *scheme* is orthogonal to the
  *kind*. A physical device's Ed25519 key attaches to its **user's** principal,
  so the device authenticates as `user:alice` and its work is attributed to the
  person rather than to a service (ADR 0022 decision 2). Every credential has a
  validity period and can be revoked; expiry and revocation are runtime
  narrowing, so trust can be rotated without a rebuild while capability cannot
  change without one.
- **Trust resolution fails closed, and the compiled principals break the
  glass.** An unknown or unauthenticated principal is refused, never defaulted
  open; and because sources resolve in order, an unreachable identity provider
  cannot disable the principals compiled into the binary.
- **A remote portal presents a credential; the navi resolves it.** The client
  never asserts an identity. The gateway frames what the client sent
  (`Authorization: Bearer <oidc-token>`, or a `Navi-Ed25519` signed request that
  binds the method, path, body digest, and time) and hands it to
  `portal.Authenticator`; the navi answers with a verified principal reference
  or a refusal, and the portal acts on that answer (ADR 0022 decision 8). A
  gateway with no authenticator authenticates nobody. The in-process console is
  the exception by construction: it is the binary the operator is sitting at,
  with no transport, so it is trusted structurally and does not come through the
  seam at all. That is a statement about its **input channel** — a terminal
  carries the operator's intent — and not about being in-process: a portal whose
  input is ambient (a room microphone, a sensor) must authenticate as a real
  principal, or whoever is in the room holds root-equivalent capability.
- **The input channel is a policy fact, and it is not authority.**
  `navi.OnChannel` makes the console-versus-microphone distinction expressible
  rather than only documented, and it says *where intent arrived*, not *who
  authored it*: a microphone grant is exercisable by whoever is audible, so
  scoping it narrowly (no `shell_exec`, confirmation for consequential actions,
  speaker verification later) is a deployment decision this vocabulary cannot
  make. Because a binding declares its transport (the in-process binding
  included — ADR 0019 calls it a transport), "local only" is expressible as
  `OnTransport("in-process")`; a request with no binding context matches no
  transport condition at all. Both label kinds are load-bearing: a binding must
  not reuse a transport label or a channel label for a different path, or a
  scoped grant widens silently from "this device's microphone" to "any audio,
  anywhere". Where locality matters, name both facts
  (`OnChannel(...)` together with `OnTransport(...)`).
- **The development default is explicit and named.** `portal.AllowAllAuthenticator`
  trusts every connection, credential or none; the reference navi wires it only
  behind `--dev-allow-all`. It is never the shipped model.

**Policy is compiled; identity is resolved.** The other half of the split above
(ADR 0021 decisions 3–5 and 11). A *capability* is a closed verb — `connect`,
`read`, `act`, `call`, `enqueue` — over a *referential* target, so it names a
tool, a type, a field, or an action the navi already declares and nothing else.
A *role* is a trust policy plus a permission policy; a *group* is the permission
side of a collection whose membership comes from the trust source. Evaluation is
default deny, explicit deny wins, allow required, intersected with the compiled
ceiling and any session policy, with every condition passing — and the ceiling
is *derived* from the declarations, so naming something undeclared is
unreachable by construction rather than by a list somebody maintains.

Conditions (`OnTransport`, `OnChannel`, `OnNetwork`, `Between`, `OnClaim`) are
predicates on the **request**, which is where decision 9's "network is a
condition, never an identity" is enforced: a mesh peer and a LAN client are
told apart by a fact about the request, not by a principal that encodes
transport. Note the asymmetry, because it reads backwards to anyone thinking in
least-privilege terms: role and group grants **accumulate**, so adding a role
never narrows what a principal may do — the subtractive moves are removing a
role or group, or adding a deny, which wins across every layer.

Portals never see any of this vocabulary; they ask the navi (ADR 0022 decision
8).

## Delegated authorization

A thought runs as the navi. Its tools are the navi's tools, so without
delegation a principal who may enqueue a thought can cause the navi to call a
tool that principal could not call directly — the confused-deputy problem. The
object surface has the same shape from the other side: a portal's write path
and a tool's write path are the same mutation, so authorizing one and not the
other leaves a bypass (ADR 0022).

- **Four enforcement points, one engine, one acting principal.** Attachment,
the object read, the object action, and the tool call all reach the same
compiled policy about the same principal; `enqueue` is the fifth verb, checked
at the portal boundary. The tool path and the object path cannot drift because
they ask the same engine.
- **The acting principal travels with the request facts.** `portal.Actor`
carries the verified principal together with `portal.RequestInfo` — the
transport and input channel the binding declares, the source address, and the
time. A condition is evaluated against the facts the binding supplied, so a
transport- or channel-scoped grant matches on the portal path rather than
refusing for the wrong reason; a request with no binding context matches no
such condition, which is the fail-closed direction. The navi maps an action and
its facts onto a capability and a request context; the vocabulary never reaches
a portal.
- **A thought carries its originator and lineage.** The principal is recorded
at the door, and a follow-up inherits it: a thought with no origin that declares
a dependency takes that dependency's principal, request facts, and lineage, so
`thoughts_create` chains a plan without the tool knowing who it acts for. A
thought with neither is **system-originated** and acts as the navi's own
service principal, `navi:<name>`, which holds the compiled ceiling by
construction — that is what keeps fail-closed from disabling the runtime's own
machinery (cron, the closing check, recovery).
- **Every tool call is authorized per call, as the originator.** The check is
per-call rather than a surface filtered once at thought creation, so a
revocation takes effect on the next call and a long-running thought does not
outlive it. A refusal is **loud**: the engine's reason travels back to the call
site, so a refused `enqueue` or tool call cannot look like nothing happened.
- **Authorization fails closed on a missing principal.** An action with no
resolved principal is refused. The object surface's default actor is the
in-process console (trusted structurally); a network binding re-scopes the
surface to the connection's verified principal before every read and action, so
the surface no longer acts as one declared principal for every connection.
- **The mode chosen at enqueue is a session policy** (ADR 0022 decision 4, ADR
0026): it narrows what that thought's calls may do and can never widen, because
the engine requires every layer — the ceiling, the principal's roles and
groups, and the session — to permit. The session is expressed as wildcard
allows plus a deny-list (`Permit` is allow-required, so a set of bare denials
would deny everything) and denies what the mode's `PermissionPolicy` and tool
subset exclude, plus `act:*` when the mode is capped at read-only. It is never
compared with the ceiling, because it grants nothing. The **selectable set** is
checked against the originator at the door: a mode that permits a tool the
principal may not call is refused loudly when the thought is enqueued, rather
than silently narrowed when it runs, while a more restrictive mode is always
selectable. The choice rides `thought.Thought.Mode` and is enforced per call at
the same place as every other capability decision.
- **Read scoping and field sensitivity compose** (ADR 0022 decision 7, ADR
0025). A field marked `Sensitive` is one a principal may see only with a grant,
not one the surface removes for everyone: projection is **principal-aware**, and
the descriptor's flag is the declaration that a grant is required. The grant is
`read:<type>.<field>`, which a `read:<type>` grant deliberately does not cover,
so the read gate decides whether the type is readable and the field grant
decides which fields; a field the principal may not read is absent, never null.
Which *rows* a principal may see is a separate question, answered by a **scope**
a type declares — a closed, referential rule that a named field must equal the
acting principal's reference. The surface ANDs it with the caller's query (the
scope wins a conflict, so a caller may narrow but never widen) and enforces it on
the way out, and the provider applies it before paging; a row the scope excludes
is reported as absent, not refused, because whether an id exists is itself scoped
information (ADR 0025 decision 3, ADR 0028). A delta never carries a sensitive field, because the live window is
shared by every connection. The in-process console sees sensitive fields because
the service principal holds the ceiling, not because the surface special-cases
a console.

## Thought modes

Modes (ADR 0007) narrow capability per thought — e.g. `plan` is read-only.
A mode is always a subset of the compiled permission ceiling; it cannot
escalate. Modes let one binary present a safe planning surface and a full
execution surface without shipping two binaries.

## Context and the injection boundary

Agent injection is treated as the primary threat, not a content-filtering
problem. The control is that **nothing reachable from a prompt can change how a
navi assembles context** ([ADR 0013](../adr/0013-the-thought-queue-is-the-context-mechanism.md)).

- **Context sources are build-time declarations.** Runtime config may narrow
  them; it can never add one. Changing what a navi assembles requires a rebuild
  and a redeploy, and the owner gates that redeploy.
- **Skills are a menu selection, not a fetch.** All three loading paths —
  author-declared auto-load, thought-declared request, agent-chosen tool — can
  only select from the **compiled-in** set. A manipulated skill request can at
  worst load a skill the author already shipped. There is no path from a prompt
  to new instructions arriving at runtime.
- **Role separation is structural.** What a navi may do is determined by which
  components and tools exist in its binary, not by what its prompt says. Two
  navis with identical prompts but different component sets have different
  capabilities, and neither can be talked into using a tool it does not have
  ([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)).
- **The assembled context is recorded**, so what a thought actually ran with is
  auditable after the fact rather than inferred.

The residual risk is accepted and named: content *inside* a thought's context
(tool output, a fetched page, an external message) can still attempt to steer
the model. That is bounded by the capability surface, not by the assembler —
which is exactly why the capability surface is the thing kept small.

## Self-improvement and the capability barrier

A navi may **learn** and it may **propose**, but it may not **widen its own
capability**.

- **Learning is the navi's.** Short-term and long-term memory, notes, and
  thoughts are the navi's own state; it may develop and keep them freely.
- **Capability is the operator's.** Adding a tool, a connector, or a service —
  or changing what a navi can reach — is a **human act**: a rebuild and
  redeploy, or a config change (ADR 0006, ADR 0013).
- **A navi may propose.** It may explain a change to its operator, or open a PR
  against a repository it can reach, for a human to review. It may not *effect*
  the change itself.
- **Nothing on the network adds capability.** A peer, a discovered service, or
  a rendezvous point cannot hand a navi a new tool; discovery only points at a
  provider, and a human grants access (ADR 0032).

Without the barrier, the compiled ceiling (ADR 0006) would be advisory: a navi
that could add its own tools, or accept one a stranger offered, would have a
capability surface decided at runtime by whatever it met.

## Provenance and audit

Every tool declares its origin (`builtin`, `sdk`, `author`, `extension`) and,
for build-time modules, its module path and version. Populated from
`runtime/debug.ReadBuildInfo()`, a running navi can report exactly which module
provided each capability. `go.mod`/`go.sum` plus build info are the
supply-chain record; artifact signing is deferred (see below).

**Attribution** answers a second "who", and it is deliberately two records (ADR
0021 decision 12, ADR 0027):

- The **interaction record** is the navi's own state. A thought carries its
  originator, the request facts it arrived with, its lineage, its mode, and its
  transcript; `origin` and `lineage` are read-only fields on the thought type,
  so "on whose behalf did this happen" is answerable through the object surface
  like any other state, and it persists with the thought.
- The **trust ledger** is operator-facing: an append-only, bounded record of the
  trust decisions the navi makes — a credential it **authenticated** (which
  principal, when, and by which scheme) and a credential it **refused** (with
  the claimed principal and the reason). It never holds a credential. Reading it
  is a capability (`read:trust_event`): the ledger is exposed as a read-only
  type, so a deployment decides who may audit trust and an operator portal
  renders it like any other type. A grant or revocation happens *outside* the
  navi — a compiled declaration, or an operator editing a trust store — so the
  ledger does not pretend to know an author for it; a future operator
grant/revocation path would append `granted`/`revoked` events naming the
  operator.

## Secrets

- Secrets are **never** literals in source, tests, or fixtures.
- Components declare credential **references** (env var name or file path),
  with a default name.
- Values are interpolated at runtime via the config merge chain
  (`defaults → file → env → flags`).
- Endpoint defaults may be compiled in; the operator may override them.
- Credential resolution is **fail-closed**: a missing credential disables the
  component's capability with a clear error rather than silently degrading. For
  a connector's egress tools that means the *capability* is disabled, not the
  navi: the connector stays declared (ADR 0014 decision 4) and each call refuses
  at the call site naming the reference, so one unconfigured integration cannot
  abort a run whose other capabilities are fine (ADR 0020 decision 7).

```go
model.OpenRouter("anthropic/claude-sonnet-4", model.WithCredential("OPENROUTER_API_KEY"))
```

## Egress

Outbound capability is a capability, and is bounded the same way inbound work is
([ADR 0014](../adr/0014-connectors-are-bidirectional-service-integrations.md)).

- **Egress lives in connector-owned tools.** A connector exposes its outbound
  operations as ordinary tool definitions the author adds explicitly via
  `connector.SendTool()` — never auto-registered. Every way a navi can talk
  outward is therefore visible in `main.go`, the same property tools and
  connectors already have.
- **Integrations are declared by composing the navi.** Which services a navi can
  reach is a build-time decision that assigns intent. IAM-style mode permissions
  narrow what a connected component may do; they never widen which services
  exist.
- **There is no generic egress payload schema.** Payloads are service-specific
  (a message, a reply, a reaction), so there is no shared envelope to
  accidentally over-share through.
- **A developer's local login is a development convenience, not a deployment
  credential.** A source may read the GitHub CLI's stored token (`gh auth
  token`) so a local run needs no exported variable, but that acts as the
  *person's* GitHub identity rather than a scoped service one. A deployment
  should prefer an explicit credential reference, or a GitHub App installation
  whose token is short-lived and scoped to one installation.
- **A minted credential's key is the secret that outlives it.** A GitHub App
  installation holds a private key and mints an installation token from it, so
  the key — not the token — is the long-lived secret: it is reference-resolved
  at the point of use (ADR 0008), never stored, and never written to an error,
  a log, or a struct dump. Only the short-lived token is cached, in memory, and
  it is dropped the moment GitHub rejects it (a 401 re-mints once rather than
  failing the call). The exchange is single-flighted, so concurrent egress calls
  do not each mint.
- **Secrets are read at the call site only** — resolved inside the request that
  needs them, from a runtime reference (ADR 0008), and never written to the
  thought transcript, the datastore, or logs. A secret never appears in history
  because history is written where the secret is not read.
- **An egress tool must tolerate an unstarted connector**: tools are bound at
  authoring time but the client arrives at `Start`, so a call before the
  connector serves fails cleanly rather than dereferencing a nil client. This is
  enforced by the mechanism rather than trusted to each author: the connector
  publishes its client into a `connector.Live[C]` handle in `Start`, clears it
  in `Stop`, and `connector.SendTool` builds the gate into the handler, so the
  call returns `connector.ErrNotStarted` (ADR 0020).
- **An egress tool cannot hide what it does.** `SendTool` refuses a descriptor
  with no name, no namespace, no access level, no input schema, or no recovery
  strategy, and stamps the tool's provenance from its namespace — so "what can
  this navi reach, and through which component?" is answerable from the catalog
  rather than by reading every handler.

## Trust boundary

Artifact signing is **not implemented yet**; the supply-chain record today is
the module graph (`go.mod`/`go.sum`) plus build info. When signing lands
(deferred, ADR 0005), a signature will attest to:

- the exact source and dependency graph that produced the binary,
- the compiled permission policy, tools, models, and connectors,
- the absence of any capability not linked in.

What it does **not** attest to (runtime, operator-controlled):

- credential values and endpoint overrides,
- runtime policy tightening,
- schedules.

Runtime plugins are deferred (ADR 0005). If introduced, they register
into the same tool registry under a distinct origin and require an explicit
allow-signers declaration; they never widen the compiled policy.

## Related

- [overview.md](./overview.md) — boundaries table
- [runtime.md](./runtime.md) — modes and thought lifecycle
- [extension-model.md](./extension-model.md) — third-party components and trust