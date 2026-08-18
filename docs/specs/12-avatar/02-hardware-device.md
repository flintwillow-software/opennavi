---
status: draft
created: '2026-08-13'
updated: '2026-08-13'
tags:
  - spec-tree
  - feature
  - avatar
priority: high
spec-refs: [120200, 120201, 120202, 120203, 120204, 120205, 120206]
---

# 02 Hardware Device

> Dedicated flip-style hardware device: Raspberry Pi Zero 2W with PiSugar Whisplay HAT, 30 FPS rendering on the 1.69" LCD, lid-open wake / lid-close sleep, and a 3D-printed flip case.

**Code:** S120200

## 00 Overview

The hardware device gives the OpenNavi personal assistant a physical home -- always on, always visible on the desk. It is a flip-style device built around a Raspberry Pi Zero 2W compute module and a PiSugar Whisplay HAT (1.69" LCD, WM8960 audio codec, MEMS microphones, speaker, RGB LEDs, integrated LiPo battery). The device runs the Web UI avatar renderer in Chromium kiosk mode, consumes the WebSocket event stream, and supports wake-on-lid-open and sleep-on-lid-close for natural interaction.

### User Stories

- **US-006**: As a maker, I want to build a dedicated flip device, so that the navi has a physical home on my desk.
- **US-007**: As a maker, I want the device to wake on lid-open and sleep on lid-close, so that interaction is natural.

### Software Stack

```
Boot -> Raspberry Pi OS Lite -> Chromium kiosk -> WebSocket -> Navi avatar HTML
                                          |
                                          v
                                   PiSugar Whisplay
                                   LCD (SPI) + audio (I2S)
```

The kiosk browser approach reuses the Web UI renderer code, simplifying the build at the cost of higher resource usage than a native app.

### Bill of Materials

| Component | Model | Cost | Qty |
|-----------|-------|------|-----|
| Compute module | Raspberry Pi Zero 2W | $15 | 1 |
| Display/audio HAT | PiSugar Whisplay HAT (1.69" LCD, WM8960 codec, MEMS mics, speaker, RGB LEDs) | $40 | 1 |
| Battery | Integrated in Whisplay HAT | included | 1 |
| MicroSD card | 32GB class 10 | $6 | 1 |
| USB-C cable | Power/data | $3 | 1 |
| 3D-printed case | PLA filament (~$20/kg, ~$2 per print) | $2 | 1 |
| Standoffs + screws | M2.5 nylon | $3 | 1 set |
| GPIO header | 2x20 female header | $2 | 1 |
| **Total** | | **~$77** | |

---

## 01 Pi Zero 2W Compute Module

The device SHALL use Raspberry Pi Zero 2W as the compute module.

### Requirement: Compute Module (FR-3.1)

The device SHALL use a Raspberry Pi Zero 2W as the compute module.

#### Scenario: Device boots to avatar

- GIVEN a Pi Zero 2W running Raspberry Pi OS Lite
- WHEN the device boots
- THEN the LCD SHALL show the boot logo followed by the avatar
- AND the avatar SHALL be visible in Chromium kiosk mode

#### Scenario: Reboot stability

- GIVEN the device is running the avatar renderer
- WHEN the device is rebooted 50 times
- THEN all 50 reboots SHALL boot successfully to the avatar

#### Scenario: Crash recovery

- GIVEN the avatar renderer crashes on the device
- WHEN the watchdog timer detects the crash
- THEN the device SHALL auto-restart the renderer
- AND the avatar SHALL return to the display without manual intervention

### Design Notes

- Runs Raspberry Pi OS Lite (minimal Linux) as the base OS
- Pi Zero 2W rendering budget: 30 FPS at 15k triangles (Chromium) or 10k triangles (kiosk, solid background)
- Read-only filesystem root for resilience against unexpected power loss
- Watchdog timer auto-restarts the device on crash
- Performance budget on device: memory usage < 256MB, CPU < 30% idle / < 70% speaking

---

## 02 PiSugar Whisplay HAT

The device SHALL use PiSugar Whisplay HAT for display, audio, and microphone.

### Requirement: Whisplay HAT (FR-3.2)

The device SHALL use the PiSugar Whisplay HAT for display, audio, and microphone.

#### Scenario: Display initialization

- GIVEN a Pi Zero 2W with the PiSugar Whisplay HAT attached
- WHEN the device boots
- THEN the 1.69" LCD SHALL initialize via SPI
- AND the display SHALL show the boot logo followed by the avatar

#### Scenario: Audio output

- GIVEN the WM8960 codec is configured
- WHEN a 1kHz tone playback test file is played
- THEN the speaker SHALL emit the tone
- AND the output SHALL be free of distortion

#### Scenario: Microphone input

- GIVEN the dual MEMS microphones are configured
- WHEN a 5-second test phrase is recorded
- THEN the audio SHALL be recorded successfully
- AND the recording SHALL be playable without clipping

#### Scenario: RGB LED control

- GIVEN the 4 addressable RGB LEDs are connected
- WHEN a control script cycles through colors
- THEN all 4 LEDs SHALL display the correct colors

### PiSugar Integration Spec

| Feature | Spec |
|---------|------|
| **Display** | 1.69" LCD via SPI, 320x390 resolution, 60Hz refresh |
| **Audio** | WM8960 codec via I2S, stereo output, mono speaker |
| **Microphone** | Dual MEMS mics, far-field pickup |
| **RGB LEDs** | 4 addressable LEDs, used for wake-word glow, listening pulse, thinking animation |
| **Battery** | Integrated LiPo, 1200mAh, USB-C charging |
| **Lid sensor** | GPIO interrupt on lid open/close -> wake/sleep state |

### Design Notes

- Linux kernel drivers handle the HAT (SPI display, I2S audio); no Go library needed
- Audio configuration covers WM8960 codec, speaker output, and mic input
- The 4 RGB LEDs provide ambient feedback: wake-word glow, listening pulse, thinking animation

---

## 03 30 FPS Rendering

The device SHALL render the avatar at minimum 30 FPS on the 1.69" LCD.

### Requirement: Minimum 30 FPS (FR-3.3)

The device SHALL render the avatar at a minimum of 30 FPS on the 1.69" LCD.

#### Scenario: Continuous rendering at 30 FPS

- GIVEN the Pi Zero 2W running the avatar renderer
- WHEN the avatar is animating
- THEN the frame rate SHALL be >= 30 FPS
- AND the frame rate SHALL hold over a 60-second test

#### Scenario: Rendering with lip-sync and effects

- GIVEN the avatar is speaking with lip-sync and effects enabled
- WHEN measuring frame rate over 60 seconds
- THEN the frame rate SHALL be >= 30 FPS
- AND the p95 frame time SHALL be < 33ms

### Design Notes

- Performance targets: 60 FPS idle (no effects), 30 FPS speaking (lip-sync + effects)
- Auto-LOD generation reduces model complexity on Pi Zero 2W
- Kiosk mode uses a solid background with reduced effects to meet the FPS budget
- LCD refresh: 60Hz via SPI

### Performance Benchmarks (Pi Zero 2W)

| Benchmark | Target | Method |
|-----------|--------|--------|
| FPS (idle, no effects) | 60 FPS | Chromium devtools FPS counter, 60s avg |
| FPS (speaking, lip-sync, effects) | 30 FPS | Chromium devtools FPS counter, 60s avg |
| Frame time p95 | < 33ms | `performance.now()` in requestAnimationFrame |
| WebSocket latency | < 50ms p95 | Timestamp echo, 1000 samples |
| Cold boot to avatar | < 15s | Stopwatch, power-on to avatar visible |
| Memory usage | < 256MB | `free -m` on device |
| CPU usage (idle) | < 30% | `top` batch mode, 60s sample |
| CPU usage (speaking) | < 70% | `top` batch mode, 60s sample |

---

## 04 Lid-Open Wake / Lid-Close Sleep

The device SHALL support lid-open wake and lid-close sleep.

### Requirement: Lid Wake/Sleep (FR-3.4)

The device SHALL support lid-open wake and lid-close sleep.

#### Scenario: Lid open wakes the device

- GIVEN the hardware device with the lid closed
- WHEN the lid opens
- THEN the display SHALL wake and the avatar SHALL become visible
- AND the avatar SHALL be visible within 1 second
- AND the avatar state SHALL transition to IDLE with a wake animation

#### Scenario: Lid close sleeps the device

- GIVEN the hardware device with the lid open
- WHEN the lid closes
- THEN the display SHALL sleep and the avatar SHALL disappear
- AND the avatar state SHALL transition to SLEEPING
- AND the display SHALL turn off

#### Scenario: Lid sensor reliability

- GIVEN the physical device
- WHEN the lid is opened and closed 100 times
- THEN all 100 transitions SHALL be correct wake/sleep transitions
- AND no false transitions SHALL occur

#### Scenario: Lid sensor bounce filtering

- GIVEN 10 rapid open/close events are simulated within 1 second
- WHEN the lid sensor processes them
- THEN a debounce filter SHALL produce a single state transition
- AND the state SHALL not thrash between wake and sleep

### Design Notes

- Lid sensor is a GPIO interrupt on lid open/close
- Lid close triggers the IDLE -> SLEEPING transition (eyes close, breathing slows)
- Lid open triggers the SLEEPING -> IDLE transition (eyes open, quick wake animation)
- Debounce filtering prevents state thrashing from sensor noise
- Power management: SLEEPING idle draw < 0.5W; battery monitoring with low-battery LED flash and graceful shutdown

---

## 05 3D-Printed Flip Case

The device SHALL have a 3D-printed flip case.

### Requirement: 3D-Printed Flip Case (FR-3.5)

The device SHALL have a 3D-printed flip case.

#### Scenario: Case assembly

- GIVEN the 3D-printed flip case and hardware components
- WHEN the device is assembled
- THEN all components SHALL fit inside the case
- AND the STL files SHALL print correctly

#### Scenario: Printability

- GIVEN a standard FDM printer with a 200mm x 200mm build plate
- WHEN the case parts are printed
- THEN all parts SHALL print without supports
- AND the case SHALL be printable from PLA filament

### 3D-Printed Case Design Spec

- Flip-style form factor (similar to flippable handheld devices)
- Hinge mechanism with snap-fit lid
- Cutouts for LCD, microphone holes, speaker grille, USB-C port
- Ventilation slots for Pi Zero 2W thermal management
- Printable on 200mm x 200mm FDM printer, no supports required
- Approximate closed dimensions: 80mm x 50mm x 20mm

### Design Notes

- PLA filament cost: ~$20/kg, ~$2 per print
- Case assembly verified by printing the STL files and confirming component fit
- Thermal management: 1-hour continuous render keeps CPU temp < 80C (no throttling)

---

## 06 Wake Word Detection

The device SHALL support wake word detection.

### Requirement: Wake Word Detection (FR-3.6)

The device SHALL support wake word detection.

#### Scenario: Wake word triggers listening

- GIVEN the hardware device is powered on
- WHEN the user speaks the wake word
- THEN the device SHALL detect the wake word
- AND the avatar SHALL transition to LISTENING state
- AND the RGB LEDs SHALL glow to indicate wake-word activation

#### Scenario: Wake word engine on Pi Zero 2W

- GIVEN the device is running on a Pi Zero 2W
- WHEN the wake word engine is running
- THEN the engine SHALL run offline on-device
- AND the engine SHALL not degrade rendering performance below 30 FPS

### Design Notes

- Candidate engines: Porcupine or another offline engine that fits the Pi Zero 2W budget
- Wake-word glow uses the RGB LEDs; the listening pulse and thinking animation use the same LEDs
- Wake word detection depends on audio configuration (MEMS mics, WM8960 codec)
- Wake word also wakes the avatar from SLEEPING state (alongside lid open)

---

## Implementation Notes

### Phase 3: Hardware Device Support

**Goal:** Raspberry Pi Zero 2W + PiSugar Whisplay HAT running the avatar in kiosk mode.
**Exit criteria:** Device boots, displays avatar, handles lid-open/lid-close.

| Task | Priority | Size | Depends On |
|------|----------|------|------------|
| Raspberry Pi OS Lite image + configuration | P1 | M | -- |
| Chromium kiosk mode setup (autostart, fullscreen, no cursor) | P1 | M | OS image |
| PiSugar Whisplay HAT kernel drivers (SPI display, I2S audio) | P1 | M | OS image |
| Lid sensor GPIO handling (open -> wake, close -> sleep) | P1 | M | OS image |
| RGB LED control (wake-word glow, listening pulse, thinking animation) | P2 | S | GPIO handling |
| Audio configuration (WM8960 codec, speaker output, mic input) | P1 | M | OS image |
| Wake word detection (Porcupine or offline engine) | P2 | M | Audio configuration |
| Auto-LOD for VRM models on Pi Zero 2W | P2 | M | Phase 2 (VRM loader) |
| Power management (battery monitoring, low-battery indicator) | P2 | S | Lid sensor handling |
| 3D-printed case STL files | P1 | M | -- |
| Assembly guide | P1 | S | All hardware tasks |
| Watchdog timer (auto-restart on crash) | P1 | S | OS image |

### Integration Points

| System / Module | Integration Type | Notes |
|-----------------|------------------|-------|
| PiSugar HAT | GPIO + I2S + SPI | Linux kernel drivers, no Go library needed |
| Web UI renderer | Chromium kiosk | Reuses the Web UI renderer in kiosk mode |
| WebSocket event stream | Network | Consumes the avatar event stream from the navi |

### Order of Operations

Phase 3 runs after Phase 2 -- the hardware device reuses the Web UI renderer in kiosk mode. Driver and OS work can start in parallel with Phase 2.

### Design Decisions

| Decision | Context | Chosen | Consequences |
|----------|---------|--------|--------------|
| Kiosk browser vs. native app | Hardware device display | Chromium kiosk mode | Reuses Web UI code, simpler to build. Higher resource usage than native. |
| Phoneme vs. viseme | Lip-sync data | Phoneme events from TTS engine | More accurate lip-sync. Requires TTS engine that emits phoneme timing. |
| VRM 1.0 vs. 0.x | Avatar format | VRM 1.0 | Forward-compatible, better expression system. Fewer existing models. |

---

## Status History

| Date | Status | Notes |
|------|--------|-------|
| 2026-08-13 | Draft | Created from avatar product-spec files |