---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - messaging
priority: high
spec-refs: [050000, 050100, 050200, 050300]
---

# 05 Messaging

> Unified messaging gateway — protocol, platform adapters, hooks, cron, and cross-platform delivery.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S050000 Message Protocol & Gateway](00-message-protocol-gateway.md) | Message Protocol & Gateway | draft | Unified protocol, gateway architecture, routing |
| [S050100 Platform Adapters & Routing](01-platform-adapters-routing.md) | Platform Adapters & Routing | draft | Telegram, Discord, Slack adapters, multi-agent routing |
| [S050200 Hooks, Cron & Implementation](02-hooks-cron-impl.md) | Hooks, Cron & Implementation | draft | Hook system, cron scheduler, auth, implementation |
| [S050300 Email, SMS, Calendar & MQTT](03-email-sms-calendar-mqtt-adapters.md) | Email, SMS, Calendar & MQTT | draft | Email, Zendesk, MQTT, SMS, Calendar adapters |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- Single Message type for all platforms and scenarios
- Platform adapters compiled in at build time
- TLS/mTLS for security, HMAC for webhooks
- Cron scheduler for fully autonomous agent behavior

## Related

- [Session Context](../03-session-context/README.md)
- [Multi-Agent](../04-multi-agent/README.md)
