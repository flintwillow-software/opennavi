---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - avatar
priority: high
spec-refs: [120000, 120001, 120002, 120003, 120004, 120005, 120006, 120007]
---

# 00 Avatar Rendering

> 3D avatar rendering with VRM/glTF format support, idle animation, emotion expression, lip-sync, spring bone physics, and custom model loading.

**Code:** S120000

## 00 Overview

Avatar rendering is the visual layer of the OpenNavi personal assistant. It renders a 3D avatar character using the VRM 1.0 format (or glTF 2.0 as fallback), supporting a range of expression and animation features that make the assistant feel alive and present.

The rendering pipeline is driven by a WebSocket event stream from the Avatar State Manager and runs in a Web UI (Three.js + three-vrm) or optionally as a native renderer. The Web UI serves as the primary rendering target, with the hardware device consuming it in Chromium kiosk mode.

### User Stories

- **US-001**: As a home user, I want to see my navi's 3D avatar, so that the assistant feels like a real companion.
- **US-002**: As a home user, I want the avatar to express emotions (happy, thinking, concerned), so that I can read the assistant's state at a glance.
- **US-003**: As a home user, I want the avatar to lip-sync when speaking, so that the assistant feels more natural.
- **US-004**: As a home user, I want to customize the avatar's appearance, so that it reflects my taste.
- **US-005**: As a home user, I want the avatar to show idle animations (breathing, blinking, subtle movements), so that it feels alive even when silent.
- **US-009**: As a developer, I want to supply my own VRM avatar model, so that the avatar is unique to my navi.

### Design: Rendering Pipeline

```
WebSocket event -> three-vrm expression system -> VRM model -> Three.js scene -> WebGL 2.0 -> canvas
     |                              |
     |                              v
     +-> phoneme -> viseme blink -> expression blending
                                    |
                                    v
                              Spring bone simulation
                                    |
                                    v
                              Look-at controller (subtle gaze)
```

### Tech Stack

- **Three.js** r160+ -- WebGL 2.0 renderer
- **@pixiv/three-vrm** -- VRM 1.0 loader, expression, spring bone, look-at
- **WebSocket API** -- Native browser WebSocket, no polyfill
- **ES modules** -- Modern import syntax, no bundler required for development

### VRM / glTF Avatar Format Support

- **VRM 1.0** (`.vrm`) -- Primary format. Full blendshape, bone, spring bone, and MToon shader support.
- **glTF 2.0** (`.glb`, `.gltf`) -- Fallback format. Limited to bone animations only; no blendshapes.

### Architecture

The Avatar Renderer is part of the broader avatar system:

```
Navi Binary (Go) -> Avatar State Manager -> WebSocket Server -> Avatar Renderer (Web/Embedded)
```

The state manager emits JSON events (state_change, emotion, phoneme, animation) over WebSocket. The renderer consumes these events and drives the 3D avatar accordingly.

---

## 01 VRM/glTF Rendering

The system SHALL render a 3D avatar using VRM/glTF format.

### Requirement: VRM/glTF Rendering (FR-1.1)

The system SHALL render a 3D avatar using VRM 1.0 or glTF 2.0 format.

#### Scenario: VRM model loads and displays correctly

- GIVEN a valid VRM 1.0 model file (`.vrm`)
- WHEN the renderer loads the model
- THEN the model SHALL display with correct geometry and textures
- AND the renderer SHALL support VRM blendshapes, bones, spring bones, and MToon shaders

#### Scenario: glTF fallback loading

- GIVEN a glTF 2.0 model file (`.glb` or `.gltf`)
- WHEN the renderer loads the model
- THEN the model SHALL display with bone animations
- AND the renderer SHALL log a warning about limited blendshape support

#### Scenario: Corrupted VRM file

- GIVEN a corrupted or invalid VRM model file
- WHEN the renderer attempts to load it
- THEN the system SHALL display the fallback avatar
- AND the system SHALL log an error

### Design Notes

- VRM model loader uses `@pixiv/three-vrm` for VRM 1.0 support
- GLB binary format is the expected distribution format for VRM 1.0
- Mesh complexity target: < 50,000 triangles for full detail, with auto-LOD generation for Pi Zero 2W
- Default avatar (geometric face with basic blendshapes) used as fallback when no VRM file is provided

---

## 02 Idle Animation

The system SHALL support idle animation (breathing, blinking, subtle sway).

### Requirement: Idle Animation (FR-1.2)

The system SHALL support idle animation including breathing, blinking, and subtle body sway.

#### Scenario: Breathing animation

- GIVEN the avatar is in IDLE state
- WHEN the idle animation loop is active
- THEN the avatar SHALL display a subtle breathing motion (chest rise/fall)
- AND the breathing rate SHALL be approximately 12 breaths per minute

#### Scenario: Periodic blinking

- GIVEN the avatar is in IDLE state
- WHEN the idle animation loop is active
- THEN the avatar SHALL blink periodically every 3-5 seconds
- AND the blink duration SHALL be approximately 150ms

#### Scenario: Subtle sway

- GIVEN the avatar is in IDLE state
- WHEN the idle animation loop is active
- THEN the avatar SHALL perform a subtle head sway or body shift
- AND the sway SHALL be randomized to avoid appearing mechanical

### Design Notes

- Idle animation loop runs continuously via `requestAnimationFrame`
- Blink timer is randomized between 3-5 seconds for natural feel
- Breathing is driven by a sinusoidal function on the chest bone Y position
- Idle animations are authored as part of the VRM model's built-in animations or procedurally generated

---

## 03 Emotion Expression

The system SHALL support emotion expression via VRM blendshapes.

### Requirement: Emotion Expression (FR-1.3)

The system SHALL support emotion expression via VRM blendshapes.

#### Scenario: Emotion event triggers blendshape change

- GIVEN the avatar is rendered and a WebSocket connection is active
- WHEN an emotion event with `emotion: "happy"` is received
- THEN the avatar's blendshapes SHALL change to show a smile
- AND the expression SHALL be visible within 200ms of the event

#### Scenario: Multiple emotions blend correctly

- GIVEN the avatar is currently expressing an emotion
- WHEN a new emotion event arrives with different parameters
- THEN the system SHALL smoothly blend from the current expression to the new one
- AND the blend duration SHALL respect the `easing` and `duration_ms` fields in the event

#### Scenario: Missing blendshape for emotion

- GIVEN a VRM model that lacks the blendshape for a requested emotion (e.g., no `happy` blendshape)
- WHEN the emotion is triggered
- THEN the emotion SHALL be ignored
- AND the expression SHALL remain neutral
- AND a warning SHALL be logged

### Design Notes

- Supported VRM blendshapes: `happy`, `angry`, `sad`, `surprised`, `relaxed`, `neutral`
- Emotion events contain a `blendshapes` map with target blendshape names and intensities
- Expression blending uses `easeOutQuad` easing for natural transitions
- The emotion store tracks current emotion, intensity, mood, and expression history

---

## 04 Lip-Sync

The system SHALL support lip-sync animation from phoneme timing data.

### Requirement: Lip-Sync (FR-1.4)

The system SHALL support lip-sync animation from phoneme timing data.

#### Scenario: Phoneme event triggers viseme

- GIVEN the avatar is in SPEAKING state
- WHEN a phoneme event with `phoneme: "AE"` and `viseme: "wide"` is received
- THEN the avatar's mouth SHALL form the corresponding viseme shape
- AND the mouth movement SHALL last for the specified `duration_ms`

#### Scenario: Sequential phoneme events produce continuous speech

- GIVEN a sequence of phoneme events with timing data
- WHEN the renderer processes them in order
- THEN the avatar's mouth SHALL move in sync with the phoneme timing
- AND the viseme sequence SHALL match the phoneme timing without gaps

#### Scenario: Phoneme timing drift correction

- GIVEN phoneme events with cumulative timing error
- WHEN the renderer processes each event
- THEN the lip-sync SHALL resync on each new phoneme
- AND there SHALL be no cumulative drift

### Design Notes

- Phoneme events are received from the TTS engine as part of the WebSocket event stream
- Each phoneme event maps to a viseme (mouth shape) using a phoneme-to-viseme mapping table
- Supported phonemes cover English phonemes initially, extensible to other languages
- The viseme system blends between mouth shapes for smooth transitions

---

## 05 Spring Bone Physics

The system SHALL support spring bone physics for hair, clothing, and accessories.

### Requirement: Spring Bone Physics (FR-1.5)

The system SHALL support spring bone physics for hair, clothing, and accessories.

#### Scenario: Spring bone simulation is active

- GIVEN a VRM model with spring bone definitions (hair, clothing, accessories)
- WHEN the avatar is animating or moving
- THEN the spring bones SHALL simulate realistic physics (sway, bounce)
- AND the simulation SHALL run at the renderer's frame rate

#### Scenario: Model without spring bones

- GIVEN a VRM model without spring bone definitions
- WHEN the avatar loads
- THEN the system SHALL skip spring bone simulation
- AND no physics effects SHALL be applied

### Design Notes

- Spring bone physics are handled by `@pixiv/three-vrm`'s built-in spring bone system
- Physics run on the CPU in the Web UI renderer
- On Pi Zero 2W, spring bone complexity may be reduced to maintain 30 FPS

---

## 06 Custom VRM Loading

The system SHALL allow loading a custom VRM file at navi start.

### Requirement: Custom VRM Loading (FR-1.6)

The system SHALL allow loading a custom VRM file at navi start.

#### Scenario: Custom VRM model at startup

- GIVEN a custom VRM file is provided at navi start
- WHEN the avatar loads
- THEN the custom model SHALL be used instead of the default avatar
- AND the renderer SHALL apply all expression and animation features to the custom model

#### Scenario: No custom VRM provided

- GIVEN no custom VRM file is provided at navi start
- WHEN the avatar loads
- THEN the system SHALL use the built-in default avatar (geometric face with basic blendshapes)
- AND a log message SHALL indicate the default avatar is in use

### Design Notes

- Custom VRM files are loaded from a configurable path in the navi configuration
- The system validates the VRM file for required components (blendshapes, bones) before loading
- Missing optional components (spring bones) are logged but do not prevent loading

---

## 07 Non-Functional Requirements

### NFR-1: Performance

- Avatar state changes SHALL propagate to the renderer in < 200ms (p95)
- Web UI renderer SHALL maintain 60 FPS on desktop, 30 FPS on Pi Zero 2W
- WebSocket event stream SHALL handle 60+ events/second without queuing
- Hardware device cold boot to avatar visible SHALL be < 15 seconds

#### Scenario: High-throughput event handling

- GIVEN a WebSocket connection
- WHEN 100 events are sent in 1 second
- THEN all events SHALL be received without queuing
- AND zero events SHALL be dropped

#### Renderer Performance Targets

| Platform | FPS | Model Complexity | Notes |
|----------|-----|------------------|-------|
| Desktop (Chrome) | 60 | 50k triangles | Full detail |
| Desktop (Firefox) | 60 | 50k triangles | Full detail |
| Pi Zero 2W (Chromium) | 30 | 15k triangles | Auto-LOD, simple background |
| Pi Zero 2W (kiosk) | 30 | 10k triangles | Solid background, reduced effects |

### NFR-2: Power (Hardware Device)

- Continuous operation on battery SHALL be a minimum of 6 hours
- Idle (SLEEPING state) power draw SHALL be < 0.5W
- Active (SPEAKING state) power draw SHALL be < 2.5W
- Battery recharge time SHALL be < 2 hours from USB-C

#### Scenario: Battery life at full charge

- GIVEN the hardware device at full charge
- WHEN rendering continuously
- THEN the battery SHALL last at least 6 hours

### NFR-3: Form Factor

- Device dimensions SHALL be approximately 80mm x 50mm x 20mm (closed)
- Total BOM cost SHALL be approximately $77
- Case SHALL be printable on a standard FDM printer (200mm x 200mm build plate)

### NFR-4: Reliability

- Avatar renderer SHALL recover automatically from WebSocket disconnection
- Hardware device SHALL auto-restart on crash (watchdog timer)
- Device SHALL survive unexpected power loss (read-only filesystem root)

#### Scenario: WebSocket disconnection recovery

- GIVEN an active WebSocket connection between the renderer and state manager
- WHEN the connection drops
- THEN the renderer SHALL transition the avatar to IDLE state
- AND the renderer SHALL attempt to reconnect with exponential backoff
- AND normal operation SHALL resume upon reconnection

#### Scenario: Rapid reconnection

- GIVEN 10 WebSocket disconnections in 5 seconds
- WHEN the renderer attempts to reconnect each time
- THEN the reconnection interval SHALL use exponential backoff
- AND the maximum reconnect interval SHALL be 30 seconds
- AND the system SHALL not flood the server with connection attempts

---

## Implementation Notes

### Phase 2: Web UI Rendering (Three.js + three-vrm)

**Goal:** Functional avatar renderer in browser with VRM model support.
**Exit criteria:** Avatar renders with idle animation, emotion expression, and lip-sync.

| Task | Priority | Size |
|------|----------|------|
| HTML shell with Three.js scene, lighting, camera | P1 | S |
| VRM 1.0 model loader (GLB binary via three-vrm) | P1 | M |
| Idle animation loop (breathing, blink timer, subtle sway) | P1 | M |
| Emotion expression system (VRM blendshapes from WebSocket events) | P1 | M |
| Lip-sync system (viseme sequences from phoneme events) | P1 | M |
| Spring bone simulation (hair, clothing, accessories) | P2 | S |
| Look-at controller (subtle gaze following) | P2 | S |
| Background and lighting customization | P2 | S |
| Fallback avatar (geometric face if no VRM file) | P2 | S |
| WebSocket connection manager (reconnect, backoff, heartbeat) | P1 | M |
| Web UI bundled into navi binary (embedded HTTP server) | P1 | M |

### Phase 4: Native Renderer (Go/CGO/OpenGL WebGPU)

**Goal:** Desktop native renderer as an alternative to the Web UI. Deferred until Phase 2 and Phase 3 ship.

| Task | Priority | Size |
|------|----------|------|
| OpenGL ES 3.0 context creation (window or fullscreen) | P2 | L |
| WebGPU backend evaluation (wgpu-native) | P2 | M |
| VRM model loader (Go-native GLB parser) | P2 | L |
| Expression system (blendshape blending) | P2 | M |
| Transparent window overlay (always-on-top) | P3 | M |
| Idle animation and lip-sync | P2 | M |

### Order of Operations

1. Phase 1 (Avatar State Manager) first -- foundation for everything else
2. Phase 2 (Web UI Rendering) after Phase 1 -- primary rendering target
3. Phase 3 (Hardware Device) after Phase 2 -- reuses Web UI in kiosk mode
4. Phase 4 (Native Renderer) deferred -- optional, revisit after hardware device ships

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from avatar product-spec files |