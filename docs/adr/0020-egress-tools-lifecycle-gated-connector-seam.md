---
status: accepted
date: '2026-10-09'
supersedes: []
amends: ./0014-connectors-are-bidirectional-service-integrations.md
tags:
  - adr
  - connector
  - tool
  - security
---

# 0020 — Egress tools: a lifecycle-gated connector seam

## Context

ADR 0014 established that a connector is a bidirectional service integration
and that its outbound half is **connector-owned tools the author adds
explicitly** — `connector.SendTool()` — never auto-registered, so every way a
navi can talk outward stays visible in `main.go`. It also named the helper as
specified work and left two questions open:

1. **Bind timing.** Does `SendTool()` take a handle the author passes that
   resolves against the started connector, or does the registry bind at `Start`?
2. **Failure semantics.** Does an unreachable service fail the tool call, or may
   a connector queue deliveries?

The helper still does not exist, and the reason is recorded in the `connector`
package's own doc comment: *"The exact SendTool signature binds the tool
registry, which is not a component package yet."* That is a real constraint, not
a note. `connector` is a **contract** package (ADR 0004), and the external-module
CI guard fails the build if a contract package's dependency closure reaches an
implementation package — its comment names `tool/registry` as the example. So a
`connector.SendTool()` whose signature mentions the registry would break the
boundary that makes an out-of-repo connector author's life predictable.

Two things have changed since ADR 0014, and together they unblock the helper:

- **`tool.Tool` / `tool.Def` are the contract-pure descriptor.** They mirror the
  registry's `ToolDefinition` field-for-field, and `navi` already bridges them
  mechanically (`toRegistryDefinition`). `tool` is a contract package.
- **`navi.Tools` is a `*Collection[tool.Tool]`.** The navi's declared tool
  surface is already expressed in the contract type, so a connector can hand its
  egress tool straight to the composition root without the registry being
  involved at all.

So the egress seam can be contract-pure, and the only thing actually missing is
the mechanism that makes an authoring-time tool safe against a `Start`-time
client.

## Decision

1. **An egress tool is an ordinary `tool.Tool`, built by `connector.SendTool`.**
   `SendTool` is a constructor in the contract `connector` package that
   references only `tool` and the standard library. It does not mention the
   registry, and the registry never learns that connectors exist.

2. **Bind timing: the tool closes over the connector's own handle.** `SendTool`
   returns a definition whose handler resolves the connector's live client at
   *call* time. Nothing binds at `Start`, and there is no registry-side hook.
   This is ADR 0002's authoring shape — construct the connector, then add the
   tool it hands you:

   ```go
   gh := github.New(github.Config{Credential: "GITHUB_TOKEN", Repository: "acme/widgets"})
   n.Connectors.Add(gh)
   for _, t := range gh.SendTools() {   // explicit: absent unless the author adds it
       n.Tools.Add(t)
   }
   ```

3. **Lifecycle gating is structural, not a convention each author remembers.**
   `connector.Live[C]` is a connector's gated handle to its service client: the
   connector sets it in `Start`, clears it in `Stop`, and an egress handler reads
   it through `Get`. `SendTool` builds the gate into the handler, so a call
   before the connector serves returns `ErrNotStarted` — a clean error, never a
   nil dereference. ADR 0014's consequence ("tools must tolerate an unstarted
   connector") becomes a property of the mechanism rather than a rule authors
   are trusted to follow.

4. **Failure semantics: an unreachable service fails the tool call.** Egress is
   a tool outcome, not a delivery queue. A connector that wants ordering and
   retry owns that decision separately; it is not smuggled in as a default,
   because a queue changes what a failed call means to the model and deserves
   its own record.

5. **Egress provenance is recorded, not inferred.** `SendTool` stamps the tool's
   `SourceInfo` with the connector's identity, so "what can this navi reach, and
   through which component?" is answerable from the catalog rather than by
   reading every handler. A definition that does not declare a real access level
   or a namespaced name is rejected at construction, so an egress tool cannot be
   registered in a shape that hides what it does.

6. **`security.md`'s Egress section is the trust record.** ADR 0014's open
   question 4 is answered by what already shipped: the section holds decisions
   4–6 (build-time declaration, no generic payload, call-site secrets) and this
   ADR adds the gating rule to it.

7. **An unset credential disables the capability, not the navi.** A connector
   whose `Start` fails aborts the whole run (`navi.serve`: *"a half-serving navi
   is not a state worth having"*), so making a missing token a `Start` error
   would let one unconfigured integration take down every other capability — a
   coding agent that cannot read a file because its GitHub token is absent.
   Instead `Start` succeeds, warns once naming the reference, and each egress
   call refuses at the call site, naming the reference and never a value. The
   service stays declared at build time (decision 4 of ADR 0014 is untouched),
   and ADR 0008's requirement is met where it matters — the failure is loud and
   actionable, never a silent degradation. This is also the egress precedent
   `tool/http` already set: without an `AuthResolver` its tool refuses the
   call rather than refusing to load.

8. **A credential is a token source, not a string.** The connector asks a
   `TokenSource` for its bearer token instead of resolving a value, because
   GitHub credentials are not all static. A personal access token is a string
   read from the environment — and so is the `GITHUB_TOKEN` a GitHub Actions
   workflow mints per run — but a **GitHub App installation** holds a private
   key, signs an RS256 JWT, exchanges it for an installation access token that
   **expires in an hour**, and must re-mint it. A resolver returning a string
   cannot express that, and resolving per request would mint a token per call,
   so the source owns its own caching and refresh and is asked only for a
   usable token. This is what makes the App case an *addition* rather than a
   refactor of the static path, and it is not only about the App: a source may
   be a *subprocess* — `gh auth token`, so a developer already logged in needs
   no exported variable — and such a source caches precisely because asking per
   request would spawn a process per egress call.

## Consequences

- **The egress seam is contract-pure and testable out of repo.** A third-party
  connector author imports `connector` and `tool` and nothing else, and can
  prove their tool fails closed before `Start` without a navi in the loop.
- **Capability by omission holds.** An egress tool exists in a binary only if the
  author added it; the connector being present does not put its outbound
  operations on the model's surface (ADR 0006).
- **The registry stays ignorant of connectors.** No new registration path, no
  bind-at-`Start` lifecycle, and the existing `n.Tools` → registry bridge is
  unchanged.
- **`SendTool` is a function, and `SendTools` is a convention.** A connector
  exposes its own accessor (a method returning the tools it built) because only
  the connector knows how many outbound operations it has. ADR 0002's
  `discord.SendTool()` is that accessor; the singular form in the earlier records
  is the same idea for a one-tool service.
- **A connector still owes an inbound half.** This ADR settles egress only; a
  connector that ships outbound operations and no inbound subscription is a
  partial connector, and the documentation should say so rather than implying
  the seam is the whole component.
- **Gating costs a mutex read per call.** Negligible next to a network request,
  and it removes a whole class of nil-client panics.
- **A misconfigured integration is visible but not fatal.** The capability is
  inert until the operator supplies the credential; the rest of the navi is
  unaffected. The cost is that a missing credential is discovered from a warning
  or a refused call rather than from a startup failure.
- **A credential that must be minted is an addition, not a refactor.** Because
  the seam yields a token rather than a string (decision 8), a GitHub App
  installation is a new `TokenSource` implementation plus configuration — the
  static path, the tools, the gate, and the template wiring are untouched.

## Alternatives considered

- **`SendTool` returns a `registry.ToolDefinition`.** The original intent, and
  the reason the helper was deferred. Rejected: it puts an implementation
  package in the contract closure and fails the external-module guard.
- **Bind at `Start` (registry-side hook).** Rejected: it makes the registry aware
  of connectors, gives the tool a second lifecycle that must agree with the
  connector's, and makes "is this capability in the binary?" a runtime question
  instead of a `main.go` question.
- **A generic `Service[C]` base type every connector embeds.** Rejected as
  premature: the gated handle is the only part that is genuinely shared, and a
  base type would constrain how connectors manage their own clients.
- **No mechanism — each connector gates its own client.** Rejected: it is the
  rule ADR 0014 already stated as a consequence and left unenforced, and the
  failure mode is a panic in production rather than a clean error.
- **Fail `Start` when the credential is unset.** Rejected: it makes one
  unconfigured integration abort the navi, and it turns a deployment gap into a
  total outage. See decision 7.
- **Declare the connector only when the credential is configured.** Rejected:
  it is a runtime widening of which services exist, which ADR 0014 decision 4
  forbids — and it would mean the composed binary's capability surface depends
  on the operator's environment rather than on `main.go`.
- **A connector-owned delivery queue.** Deferred, not rejected: see decision 4.

## Open questions

1. **Inbound subscription configuration** — which events a connector watches,
   and whether that is build-time declaration with runtime credentials or partly
   runtime (ADR 0014 open question 3). Unchanged by this record.
2. **Queued egress.** If a connector later wants at-least-once delivery, the
   decision is what a queued call returns to the model and how ordering across
   thoughts is defined.
3. **Credential resolution in the contract layer — answered in part.** A
   credential is a token source (decision 8), which settles the *shape*. The
   *home* is still open: `tool/http` declares an `AuthResolver` returning a
   string, which has the same limitation this decision fixes, and it cannot
   simply import this one — `connector` imports `tool`, so a shared seam would
   need its own leaf package. Until a second consumer exists, the seam lives
   here and `tool/http` is left alone.

## Related

- [ADR 0014](./0014-connectors-are-bidirectional-service-integrations.md) — the
  record this amends; resolves its open questions 1, 2, and 4
- [ADR 0010](./0010-three-capability-layers.md) — the three layers
- [ADR 0002](./0002-navi-is-a-composition-root.md) — the composition-root
  authoring shape this follows
- [ADR 0004](./0004-component-packages-public-implementations-private.md) — the
  contract boundary this keeps
- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — capability
  by omission
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credentials as
  references
- [security.md](../architecture/security.md) — the Egress section
