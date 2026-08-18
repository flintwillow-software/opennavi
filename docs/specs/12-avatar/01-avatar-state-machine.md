---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - avatar
priority: high
spec-refs: [120100, 120101, 120102, 120103, 120104]
---

# 01 Avatar State Machine

> Avatar state machine with six states, context-driven transitions, a WebSocket JSON event stream, and a priority-based animation queue.

**Code:** S120100

## 00 Overview

The Avatar State Machine is the brain of the avatar system. It tracks the avatar's current state (IDLE, LISTENING, THINKING, SPEAKING, EMOTING, SLEEPING), transitions between states based on navi conversation context, and emits JSON events over WebSocket so that any renderer can consume the state stream.

The state manager runs inside the Navi binary (Go). It is driven by the personality engine (emotion, mood, context) and emits events that are consumed by the Web UI renderer and the hardware device. It includes three core components:

- **State Machine** -- six states with defined transitions
- **Animation Queue** -- priority-based queue for smooth transitions between states
- **Emotion Store** -- tracks current emotion, intensity, mood, and expression history

### User Stories

- **US-008**: As a developer, I want a WebSocket event stream of avatar state, so that I can build custom renderers.

### Architecture

```
Navi Binary (Go)
  Personality Engine -> emotion, mood, context
    -> Avatar State Manager
       State Machine: IDLE <-> LISTENING <-> THINKING <-> SPEAKING
                      IDLE <-> EMOTING <-> IDLE
                      IDLE -> SLEEPING -> IDLE (wake)
       Animation Queue: [{id, type, blendshapes, duration, easing}]
       Emotion Store: {current, intensity, mood, expression_history}
    -> WebSocket Server: ws://navi-host:8080/avatar
       Events: state_change, emotion, phoneme, animation
    -> Avatar Renderer (Web/Embedded)
```

### State Diagram

```
                     +----------+
                     |  IDLE    | <---------------------------+
                     +----+-----+                             |
                          |                                   |
               +----------+-----------+                       |
               v          v           v                       |
          +---------+ +---------+ +----------+               |
          |LISTENING| | EMOTING | | SLEEPING |               |
          +----+----+ +----+----+ +----+-----+               |
               |           |           | (lid open / wake)   |
               v           |           +---------------------+
          +---------+      |
          | THINKING|      |
          +----+----+      |
               |           |
               v           |
          +---------+      |
          | SPEAKING|      |
          +----+----+      |
               |           |
               +-----------+
```

---

## 01 State Machine

The system SHALL maintain an avatar state machine with states: IDLE, LISTENING, THINKING, SPEAKING, EMOTING, SLEEPING.

### Requirement: State Machine States (FR-2.1)

The system SHALL maintain an avatar state machine with the states IDLE, LISTENING, THINKING, SPEAKING, EMOTING, and SLEEPING.

#### Scenario: Initial state is IDLE

- GIVEN the avatar system is initialized
- WHEN the state machine starts
- THEN the initial state SHALL be IDLE

#### Scenario: Valid state transitions

- GIVEN the avatar is in a valid state
- WHEN a transition trigger arrives for a defined transition
- THEN the state machine SHALL transition to the target state
- AND the state machine SHALL emit a state_change event

#### Scenario: Invalid state transition

- GIVEN the avatar is in SLEEPING state
- WHEN a LISTENING transition trigger arrives (e.g., `user_speaking = true`)
- THEN the transition SHALL be rejected
- AND an error SHALL be logged
- AND the state SHALL remain unchanged

#### Scenario: Conversation cycle

- GIVEN the avatar is in IDLE state
- WHEN a full conversation cycle completes (user speaks, navi thinks, navi responds)
- THEN the state machine SHALL transition IDLE -> LISTENING -> THINKING -> SPEAKING -> IDLE
- AND each transition SHALL emit a state_change event

### State Descriptions

| State | Animation | Triggers | Duration |
|-------|-----------|----------|----------|
| **IDLE** | Subtle breathing, periodic blink (every 3-5s), slight head sway | Conversation start, after speaking ends | Indefinite |
| **LISTENING** | Slight head tilt, attentive posture, micro-nods | User speech detected, wake word | Until user stops speaking |
| **THINKING** | Gaze shift (up/right), subtle hand gesture, furrowed brow | LLM processing, tool execution | 0.5-10s (LLM latency) |
| **SPEAKING** | Lip-sync active, emphasis gestures, periodic blink | Navi speech starts | Duration of speech |
| **EMOTING** | Expression change via blendshapes (happy, sad, concerned, surprised, etc.) | Personality state change, conversation context | 2-5s, blend back to base |
| **SLEEPING** | Eyes closed, slowed breathing animation, dimmed display | Lid close, inactivity timeout, sleep command | Indefinite (until wake) |

### State Transitions

| From | To | Condition | Animation |
|------|----|-----------|-----------|
| IDLE | LISTENING | Wake word detected / user starts speaking | Quick head turn toward user |
| LISTENING | THINKING | User finishes speaking, LLM requested | Brief gaze shift, thinking posture |
| THINKING | SPEAKING | LLM response received, TTS starts | Forward lean, mouth shape ready |
| SPEAKING | IDLE | Speech ends | Relax to idle posture |
| SPEAKING | EMOTING | Emotional content in speech | Expression change during speech |
| EMOTING | IDLE | Expression complete | Blend back to neutral over 2s |
| IDLE | SLEEPING | Lid close / timeout / sleep command | Eyes close, breathing slows |
| SLEEPING | IDLE | Lid open / wake word | Eyes open, quick wake animation |

### Unit Tests: State Machine Transitions

| Test | Input | Expected Output |
|------|-------|-----------------|
| IDLE -> LISTENING | `user_speaking = true` | State = LISTENING, event emitted |
| LISTENING -> THINKING | `user_speaking = false, llm_requested = true` | State = THINKING, event emitted |
| THINKING -> SPEAKING | `llm_response_ready = true` | State = SPEAKING, event emitted |
| SPEAKING -> IDLE | `speech_complete = true` | State = IDLE, event emitted |
| IDLE -> EMOTING | `emotion = "happy", duration = 3000` | State = EMOTING, emotion set, timer started |
| EMOTING -> IDLE | `emotion_timer_expired` | State = IDLE, emotion blends back to neutral |
| IDLE -> SLEEPING | `lid_closed = true` | State = SLEEPING, event emitted |
| SLEEPING -> IDLE | `lid_open = true` | State = IDLE, wake animation plays |
| Invalid transition | `state = SLEEPING, user_speaking = true` | Transition rejected, error logged, state unchanged |

---

## 02 Context-Driven Transitions

The system SHALL transition states based on navi conversation context.

### Requirement: Context-Driven Transitions (FR-2.2)

The system SHALL transition states based on navi conversation context, including user speech detection, LLM processing state, TTS state, and personality state changes.

#### Scenario: User starts speaking

- GIVEN the avatar is in IDLE state
- WHEN user speech is detected (wake word or voice activity)
- THEN the state SHALL transition to LISTENING
- AND a quick head turn animation SHALL play

#### Scenario: User finishes speaking and LLM is requested

- GIVEN the avatar is in LISTENING state
- WHEN the user stops speaking and an LLM request is made
- THEN the state SHALL transition to THINKING

#### Scenario: LLM response ready

- GIVEN the avatar is in THINKING state
- WHEN the LLM response is received and TTS starts
- THEN the state SHALL transition to SPEAKING
- AND the avatar SHALL adopt a forward lean with mouth shape ready

#### Scenario: Speech ends

- GIVEN the avatar is in SPEAKING state
- WHEN the speech completes
- THEN the state SHALL transition to IDLE
- AND the avatar SHALL relax to idle posture

#### Scenario: Emotional content in speech

- GIVEN the avatar is in SPEAKING state
- WHEN the speech contains emotional content
- THEN the state SHALL transition to EMOTING
- AND an expression change SHALL play during speech

#### Scenario: Emotion expression completes

- GIVEN the avatar is in EMOTING state
- WHEN the emotion timer expires
- THEN the state SHALL transition to IDLE
- AND the expression SHALL blend back to neutral over 2 seconds

#### Scenario: Lid close triggers sleep

- GIVEN the hardware device lid is open and the avatar is active
- WHEN the lid closes (or an inactivity timeout or sleep command occurs)
- THEN the state SHALL transition to SLEEPING
- AND the avatar eyes SHALL close and breathing SHALL slow

#### Scenario: Lid open wakes the avatar

- GIVEN the avatar is in SLEEPING state
- WHEN the lid opens (or a wake word is detected)
- THEN the state SHALL transition to IDLE
- AND a quick wake animation (eyes open) SHALL play

### Design Notes

- The personality engine provides emotion, mood, and context that flow into the Avatar State Manager
- State transitions are driven by conversation context: user speech detection, LLM processing, TTS output, and personality state changes
- The Emotion Store tracks `{current, intensity, mood, expression_history}` and informs EMOTING transitions
- Rapid state transitions are handled by the state machine without state corruption

---

## 03 WebSocket Event Stream

The system SHALL emit state changes as JSON events over WebSocket.

### Requirement: WebSocket Event Stream (FR-2.3)

The system SHALL emit state changes as JSON events over WebSocket, with events delivered to connected clients.

#### Scenario: State change event delivery

- GIVEN a renderer is connected to the WebSocket endpoint
- WHEN the avatar state changes
- THEN the state manager SHALL emit a JSON state_change event
- AND the WebSocket SHALL deliver the event to the client within 200ms

#### Scenario: Valid state_change event processing

- GIVEN the renderer receives a valid state_change event
- WHEN the event has the form `{"type":"state_change","state":"SPEAKING","previous_state":"THINKING"}`
- THEN the rendered state SHALL update to SPEAKING

#### Scenario: Malformed JSON

- GIVEN the renderer receives malformed JSON (e.g., `{invalid`)
- WHEN the message is parsed
- THEN the connection SHALL stay open
- AND an error SHALL be logged

#### Scenario: Unknown event type

- GIVEN the renderer receives an event with an unknown type (e.g., `{"type":"unknown_event"}`)
- WHEN the message is processed
- THEN the event SHALL be ignored
- AND a warning SHALL be logged

#### Scenario: Missing required field

- GIVEN the renderer receives a state_change event without the required `state` field
- WHEN the message is processed
- THEN the event SHALL be ignored
- AND an error SHALL be logged

#### Scenario: Multiple WebSocket clients

- GIVEN 10 clients are connected to the WebSocket endpoint simultaneously
- WHEN state events are emitted
- THEN all clients SHALL receive the same event stream
- AND no client SHALL receive duplicate events

#### Scenario: Large event batch

- GIVEN 1000 events are sent within 500ms
- WHEN the renderer processes them
- THEN all events SHALL be processed
- AND no frames SHALL be dropped

### Connection

```
ws://navi-host:8080/avatar
```

Events are JSON objects, one per line (newline-delimited JSON).

### Event Types

#### state_change

```json
{
  "type": "state_change",
  "state": "SPEAKING",
  "previous_state": "THINKING",
  "timestamp": 1691678400000,
  "transition_duration_ms": 300
}
```

#### emotion

```json
{
  "type": "emotion",
  "emotion": "happy",
  "intensity": 0.8,
  "blendshapes": {
    "happy": 0.8,
    "mouthSmile": 0.7,
    "eyeOpen": 0.3
  },
  "duration_ms": 3000,
  "easing": "easeOutQuad"
}
```

#### phoneme

```json
{
  "type": "phoneme",
  "phoneme": "AE",
  "viseme": "wide",
  "start_ms": 0,
  "duration_ms": 120,
  "intensity": 0.9
}
```

#### animation

```json
{
  "type": "animation",
  "animation_id": "greeting_wave",
  "priority": "normal",
  "loop": false,
  "duration_ms": 2000,
  "blendshapes": {
    "handWave": 1.0
  }
}
```

### Design Notes

- The WebSocket server endpoint (`/avatar`) is served on the navi's existing HTTP port via the embedded HTTP server
- Events are serialized as newline-delimited JSON, one event per line
- Supported event types: `state_change`, `emotion`, `phoneme`, `animation`
- WebSocket delivers events to all connected clients (1:1 navi-to-renderer is the primary deployment, up to 10 clients supported)
- The renderer recovers automatically from disconnection with exponential backoff (max 30s reconnect interval)

---

## 04 Animation Queue

The system SHALL queue animations for smooth transitions between states.

### Requirement: Animation Queue (FR-2.4)

The system SHALL queue animations for smooth transitions between states, with priority-based interruption support.

#### Scenario: Animation queued during state transition

- GIVEN a state transition occurs
- WHEN the transition requires an animation
- THEN the animation SHALL be queued with its priority
- AND the animation SHALL play when the renderer is ready

#### Scenario: Higher-priority animation interrupts

- GIVEN two rapid animation requests are queued
- WHEN the second request has higher priority
- THEN the queue SHALL interrupt the first animation
- AND the higher-priority animation SHALL play first

#### Scenario: Animation queue handling under stress

- GIVEN rapid state transitions every 100ms
- WHEN the animation queue processes them
- THEN the queue SHALL handle the transitions without state corruption
- AND all events SHALL be logged

### Design Notes

- The animation queue holds entries of the form `{id, type, blendshapes, duration, easing}`
- Priority-based queue: higher-priority animations interrupt lower-priority ones
- Smooth transitions between states use easing functions (e.g., `easeOutQuad`)
- Queue depth is bounded; overflow events are logged

---

## Implementation Notes

### Phase 1: Avatar State Manager (Data Model)

**Goal:** Avatar state machine as a Go data model with event emission.
**Exit criteria:** AvatarStateManager struct exists, state transitions work, events emit over WebSocket.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| AvatarStateManager struct (state, emotion, speech state, animation queue) | P1 | M | -- |
| State machine implementation (IDLE, LISTENING, THINKING, SPEAKING, EMOTING, SLEEPING) | P1 | M | AvatarStateManager struct |
| Emotion store (current emotion, intensity, mood, history) | P1 | S | AvatarStateManager struct |
| Animation queue (priority-based, smooth transitions) | P1 | M | State machine |
| WebSocket server endpoint (`/avatar`) | P1 | M | -- |
| Event serialization (state_change, emotion, phoneme, animation) | P1 | S | WebSocket server |
| Personality engine integration (emotion/mood-driven state) | P2 | S | Emotion store |
| Unit tests for state machine transitions | P1 | S | State machine |

### Integration Points

| System / Module | Integration Type | Notes |
|-----------------|------------------|-------|
| Personality engine | Go function call | Emotion/mood state flows into AvatarStateManager |
| Navi HTTP server | Embedded HTTP | WebSocket endpoint served on the navi's existing HTTP port |
| Web UI | Embedded filesystem | HTML, JS, CSS bundled into navi binary via `embed.FS` |
| TTS engine | Go function call | Phoneme timing data emitted as avatar events |

### Order of Operations

Phase 1 is implemented first -- the state manager is the foundation. Everything else (Web UI renderer, hardware device, native renderer) consumes its events.

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from avatar product-spec files |