---
status: draft
created: '2026-07-29'
updated: '2026-08-13'
tags:
  - spec-tree
  - component
  - avatar
priority: high
spec-refs: [120000, 120100, 120200]
---

# 12 Avatar

> 3D avatar system with VRM/glTF support, emotion expression, lip-sync, and dedicated hardware device support.

## Features

| Code | Feature | Status | Key Requirements |
|------|---------|--------|------------------|
| [S120000 Avatar Rendering](00-avatar-rendering.md) | Avatar Rendering | planned | VRM/glTF rendering, idle animation, emotion, lip-sync |
| [S120100 Avatar State Machine](01-avatar-state-machine.md) | Avatar State Machine | planned | State machine, transitions, WebSocket event stream |
| [S120200 Hardware Device](02-hardware-device.md) | Hardware Device | planned | Pi Zero 2W, PiSugar Whisplay, wake/sleep |

> `spec-refs` lists the codes of the features in this component.

## Design Notes

- VRM/glTF avatar rendering with emotion blendshapes and lip-sync
- Avatar state machine with WebSocket event stream
- Dedicated flip hardware device with wake-on-lid-open

## Related

- None
