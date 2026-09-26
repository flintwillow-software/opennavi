---
status: accepted
date: '2026-09-24'
supersedes: []
spec-refs: [000400, 130400]
tags:
  - adr
  - taxonomy
  - runtime
---

# 0010 — Three capability layers: tools, connectors, portals

## Context

The retired interface set (S000400) conflated two different things: **delivery
surfaces** the human uses to observe a running navi, and **transport
integrations** that bring work in from the outside. Both were called
"interfaces," and their relationship to the thought system was unstated. That
made it hard to answer a simple question — when I add this thing to a navi,
does it run *inside* a thought or does it *create* one?

## Decision

Navi capabilities are classified into exactly three layers, by their
relationship to the thought system:

| Layer | What it is | Direction | Thought lifecycle |
|---|---|---|---|
| **Tools** | Agent-callable actions (file, shell, HTTP, todo, a domain API) | Outbound, invoked *by* the navi | No — they run inside a thought |
| **Connectors** | Transport integrations that bring work in (gateway webhooks, Discord/Slack/MQTT/email adapters, schedules) | Inbound, *to* the navi | Yes — spawn / attach / steer |
| **Portals** | Human observation and control surfaces (CLI, TUI, WebUI) | Bidirectional, human-facing | Yes — list / schedule / steer |

Additional rules:

- **Cron is a thought-system primitive**, exposed through a connector. A
  recurring routine is a thought factory, not a generic scheduler.
- **Egress tools** (`discord_send`, `websocket_send`) are tools that close over
  a connector's live client. They are added **explicitly** by the author via
  `connector.SendTool()` — never auto-registered — so the capability surface
  stays visible in `main.go`.
- Connectors and portals receive their dependencies through a per-domain
  `Deps` struct (`connector.Deps`, `portal.Deps`) and **never import `navi`**.

## Consequences

- The word "interface" is retired as a capability label. `internal/interfaces`
  splits into `connector` and `portal` (M3).
- Adding a component has a predictable answer: tools extend what a thought can
  do; connectors and portals extend how work arrives and how it is steered.
- Egress is deliberate and greppable: every way a navi can talk outward is an
  explicit `n.Tools.Add(...)` in the navi's source.

## Alternatives considered

- **Keep "interfaces" as one layer.** Preserves the naming but leaves the
  thought-lifecycle question unanswered and keeps conflating UI with transport.
  Rejected.
- **Connectors own their egress tools implicitly (auto-register).** Less code in
  `main.go`, but hides capability and violates the "read the source, know the
  surface" property. Rejected.