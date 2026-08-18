# 0.1.0 — feat: messaging

> Derived from initial spec tree component 05 (Messaging). Implements the message protocol, platform adapters, hook/cron scheduling, and communication adapters (email, SMS, calendar, MQTT).

## Summary

Implement the messaging subsystem: message protocol and gateway for async message delivery, platform adapter interface and routing, hook and cron scheduling implementation, and platform adapters for email, SMS, calendar, and MQTT.

## Changes

- **`pkg/messaging/protocol/`** — Message protocol: Envelope format, delivery guarantees (at-least-once), routing headers, wire format.
- **`pkg/messaging/gateway/`** — Gateway: HTTP/gRPC gateway for receiving external messages and dispatching to agent runtime.
- **`pkg/messaging/router/`** — Adapter routing: Route incoming messages to the correct adapter handler based on source type.
- **`pkg/messaging/hooks/`** — Hook/cron scheduling: Webhook receiver, cron expression parser, scheduled message dispatch.
- **`pkg/messaging/adapters/email/`** — Email adapter: IMAP/SMTP integration for sending and receiving email.
- **`pkg/messaging/adapters/sms/`** — SMS adapter: Twilio or similar integration.
- **`pkg/messaging/adapters/calendar/`** — Calendar adapter: CalDAV integration for calendar events.
- **`pkg/messaging/adapters/mqtt/`** — MQTT adapter: MQTT broker integration for IoT/pub-sub messaging.

## Spec Refs

[050000, 050100, 050200, 050300]

## Code Context

- **Project structure**: Go SDK, builds on agent-runtime (tool dispatch)
- **Dependencies**: gRPC, cron library (robfig/cron), SMTP/IMAP client, Twilio SDK, CalDAV client, MQTT client
- **Existing patterns**: Adapter pattern — each platform adapter implements a common interface

## Notes

- Epic: "Messaging & Communication"
- Depends on: agent-runtime (for dispatching incoming messages as tool calls), session-context (webhook response)
- Platform adapters can be built in parallel once the adapter interface and routing are defined
- Hook/cron subsystem is critical for autonomous operation (polling, scheduled tasks)