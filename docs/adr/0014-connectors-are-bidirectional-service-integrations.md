---
status: accepted
date: '2026-09-30'
supersedes: []
amends: ./0010-three-capability-layers.md
tags:
  - adr
  - connector
  - security
  - taxonomy
---

# 0014 — Connectors are bidirectional service integrations; egress is connector-owned tools

## Context

ADR 0010 classified navi capabilities into three layers and described
connectors as *"Transport integrations that bring work in"* with direction
*"Inbound, to the navi"*. That wording assumed one direction, and it does not
match how a service integration actually works.

A GitHub connector is the clearest case. Inbound, it turns external stimuli into
thoughts: the navi is mentioned in an issue, or in a channel it watches, and a
thought lands on its queue saying *you need to address this*. Outbound, it needs
to let the navi **reply** — to the issue, in the thread. The outbound half cannot
be `http_request` for every service: authentication, protocols, and payload
semantics are service-specific, and pushing them into the prompt makes the model
responsible for API details it should never see.

Meanwhile `connector.SendTool()` — named in ADR 0002, ADR 0010, `runtime.md`, and
`rewrite-plan.md` as *the* egress pattern — **does not exist in code**. And
`security.md` has no section on egress at all. The outbound direction is
documented as a pattern, unimplemented, and unthreat-modelled.

## Decision

1. **A connector is a bidirectional integration with an external service.** Its
   inbound half turns external stimuli into thoughts — that is how a service gets
   the navi's attention. Its outbound half supplies the tools that let the navi
   act on that service. Both halves share the connection, the configuration, and
   the credentials, which is why they belong to one component.

2. **Egress is connector-owned tools, added explicitly.** A connector exposes its
   outbound operations as ordinary tool definitions the author adds —
   `connector.SendTool()` — and never auto-registers them. Every way a navi can
   talk outward stays visible in `main.go`. This keeps ADR 0010's existing
   property ("egress is deliberate and greppable") and finally specifies the
   mechanism that delivers it.

3. **There is no generic egress payload schema.** What gets sent is
   service-specific: a message, a thread reply, an update, a reaction. A generic
   webhook component with a standard envelope was the wrong abstraction — real
   services do not share a payload, so a common shim would be a
   lowest-common-denominator every connector then bypasses. A navi that wants a
   generic outbound webhook gets an egress tool for it.

4. **Integrations are navi-declared at build time.** Which services a navi can
   reach is decided by composing the navi — assigning intent, deliberately.
   Runtime config and IAM-style mode permissions may narrow what a component is
   allowed to do; they never widen which services exist (ADR 0006, ADR 0013).

5. **Role separation is structural, not prompted.** A navi's role boundary is
   *which components and tools exist in its binary*, not what its prompt says.
   Two navis with identical prompts but different connector and tool sets have
   different capabilities, and no prompt can talk one into using a tool it does
   not have. Separating roles at the system level is the point.

6. **Secrets are read at the call site, only.** A credential is resolved inside
   the request that needs it, from a runtime reference (ADR 0008), and is never
   written to the thought transcript, the datastore, or logs.

## Consequences

- **ADR 0010 is amended, not superseded.** Its Connectors row is corrected from
  *"Inbound, to the navi"* to bidirectional. The three layers, and their
  classification by relationship to the thought system, stand: tools run inside
  a thought, connectors manage thought lifecycles, portals observe and steer.
- **No new component kind.** The taxonomy is preserved rather than extended —
  the outbound half of an integration has no independent life.
- **`connector.SendTool()` becomes specified work**, not a planned helper. Until
  it lands, a navi's only outbound path is `http_request`.
- **Tools must tolerate an unstarted connector.** Egress tools are bound at
  authoring time but their client arrives at `Start`, so a call before the
  connector is serving has to fail cleanly rather than panic on a nil client.
- **Service unreachability becomes a tool outcome**, not a background queue
  (unless a connector chooses to build one).
- **Sysadmin-legible capability.** "What can this navi reach, and what can it
  say?" is answerable by reading its connector set and the tools each exposes.

## Alternatives considered

- **A fourth "egress" component kind.** Rejected: the outbound half of an
  integration needs the same client, config, and credentials as the inbound half.
  Splitting them duplicates the connection and the credential for no conceptual
  gain.
- **A generic webhook-delivery component with a standard payload.** Rejected: see
  decision 3. This was the premise of the original issue and it does not survive
  contact with real services.
- **Route all egress through `http_request`.** Rejected: it cannot express
  service semantics or auth, and it puts API details in front of the model.
- **Auto-register a connector's egress tools.** Rejected in ADR 0010 and
  re-rejected here: it hides the capability surface.

## Open questions

1. **Bind timing.** Whether `SendTool()` takes a handle the author passes that
   resolves against the started connector, or the registry binds at `Start` —
   the same lifecycle question `Deps` answers for `Start` itself.
2. **Failure semantics.** Whether an unreachable service fails the tool call or a
   connector is permitted to queue deliveries, which would make egress a
   delivery queue with ordering and retry semantics of its own.
3. **Subscription configuration.** Which inbound events a connector watches:
   build-time declaration (consistent with ADR 0013) with runtime credentials, or
   partially runtime.
4. **Where egress trust is documented.** `security.md` needs a section to hold
   decisions 4–6; whether that is an addition there or a dedicated document is
   open.

## Related

- [ADR 0010](./0010-three-capability-layers.md) — the three layers this amends
- [ADR 0002](./0002-navi-is-a-composition-root.md) — composition root, explicit collections
- [ADR 0006](./0006-compile-time-omission-plus-runtime-ceiling.md) — capability by omission
- [ADR 0008](./0008-secrets-interpolated-never-literal.md) — credentials as references
- [ADR 0013](./0013-the-thought-queue-is-the-context-mechanism.md) — build-time-only declarations
- [extension-model.md](../architecture/extension-model.md) — third-party components
