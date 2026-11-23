---
status: proposed
date: '2026-11-21'
supersedes: []
tags:
  - adr
  - portal
  - speech
  - stt
  - dependency
---

# 0047 — Speech input is a portal capability, not a connector or a provider

## Context

A navi is driven by text. Every door into the thought queue — a chat
connector, a cron trigger, a webhook, the TUI's steer input — delivers a
string, and the model layer's `Content` is a string
(`internal/llm/types.go`). Nothing in the navi can accept a human's voice.

The trigger for this record is [Whistle](https://cactuscompute.com/blog/whistle),
a 16.9 MB CPU-only speech recognition model that runs on the same Cactus engine
as Needle (the on-device tool-calling model). Three of its properties matter
here:

- **It is small enough to run on the device that runs the portal.** 16.9 MB, no
  dependencies, prebuilt for linux/arm64 (glibc and musl) among seventeen
  targets.
- **It streams.** `Stream` transcribes as audio arrives, returning the words
  two consecutive passes agreed on plus an unconfirmed tail — the
  live-captioning behaviour a push-to-talk key needs.
- **A CGO-free Go binding already exists** —
  [`github.com/zbiljic/cactus-go`](https://github.com/zbiljic/cactus-go) wraps
  Whistle (`NewWhistle`, `Transcribe`, `Stream`, `Embed`, `Complete`) and loads
  the native engine at runtime through `purego`. CGO-free matters: it keeps
  cross-compilation to an ARM64 device an ordinary `GOARCH=arm64 go build`.

The question this record answers is **where speech belongs in the three-layer
taxonomy** (ADR [0010](./0010-three-capability-layers.md)): a tool, a connector,
or a portal. That decision constrains everything downstream — what the
dependency touches, whether the SDK's model layer must learn about audio, and
whether a device is a new component type or an existing one.

Two facts about the existing architecture decide most of the answer:

1. **The taxonomy classifies by direction relative to the thought system.** A
   connector brings work *in* to the navi and owns a thought lifecycle; a portal
   is a *human-facing* observation and control surface. A microphone is a human
   input modality — it produces the text a human was going to type — which makes
   it structurally a keyboard, not a service integration.
2. **ADR 0004's rule is that component packages are dependency-light
   (stdlib-only).** Whistle arrives as a native engine plus a model archive. That
   is the same class of problem ADR [0041](./0041-go-libp2p-as-the-peer-transport-dependency.md)
   solved for libp2p, and it needs the same treatment: decide *which package*
   may import it, so only the navis that want voice pay its cost.

There is also a naming precedent to respect. `portal/view` is already a **shared
kit** sitting beside the complete portals (`portal/tui`, `portal/gateway`), and
ADR 0010 **retired the word "interface" as a capability label** when it split
`internal/interfaces` into `connector` and `portal`. This record extends both
conventions rather than inventing a parallel one.

## Decision

1. **Speech input is a portal capability.** A microphone is a human input
   modality, and the portal layer is where human intent becomes a prompt. The
   navi receives **text**; it never learns the text was spoken. Concretely: the
   portal owns capture and transcription, and calls the existing `Enqueue`
   path with a string, exactly as the TUI's steer input does.

2. **No connector, and no change to the model layer.** Speech is *not* a
   connector: a connector would make every voice-capable navi import an audio
   stack and would make "audio in" an agent concern, which is the conflation
   ADR 0010 exists to prevent. Because the portal hands the navi text, the
   provider contract is untouched — `model/` gains no audio input and
   `internal/llm`'s string `Content` stays a string. The `audio_*` fields
   Whistle's engine can return (audio tool calling) are **out of scope** here;
   if a navi ever wants them, that is a separate provider-level decision.

3. **The model runs on the device that runs the portal.** The portal owns the
   `.cact` weights and the engine; the navi daemon stays focused on the thought
   system, and portals stay focused on UX/UI. A handheld device running both
   the daemon and a portal, and a laptop portal talking to a cloud-hosted
   daemon, are the **same** architecture — only the weights' location differs.
   A thin device that streams audio to a navi-hosted transcriber is a
   *deployment variant*, not a different design.

4. **The dependency is confined to `portal/kit/stt/`.** That package is the only
   importer of `github.com/zbiljic/cactus-go`, and it wraps Whistle behind a
   small OpenNAVI interface (`Transcribe` / `Stream` / `Embed`) so the engine is
   reachable through a seam rather than directly. The contract package `portal/`
   stays stdlib-only (ADR 0004). This is ADR 0041's confinement pattern applied
   to a second dependency, and the kit *is* the boundary — there is no separate
   parent package to keep clean.

5. **`portal/kit/` is the shared-kit namespace; `portal/` holds complete
   portals.** A package that is a *part* of a portal lives under
   `portal/kit/`; a package that *is* a portal lives directly under `portal/`.
   The existing `portal/view` moves to `portal/kit/view` in the same change so
   the rule has no exception. The shape:

   ```
   portal/                 ← contract (Portal, Deps, Actor, Addressed) + complete portals
     portal.go             ← the contract, stdlib-only
     tui/                  ← complete portal
     gateway/              ← complete portal
     device/               ← complete portal (voice only: mic, button, LED, speaker)
     handheld/             ← complete portal (voice + view: the screened device)
     kit/
       view/               ← shared kit (moved from portal/view) — "Nothing here renders"
       stt/                ← shared kit: mic capture, push-to-talk state machine,
                              LED/button feedback, commit-on-release → Enqueue,
                              and the ONLY cactus-go importer (wraps Whistle)
   ```

   **There is no `voice/` parent.** Speech *input* is a kit of its own. Text-to-
   speech is deliberately **out of scope** for this record: it is a separate
   capability, it does not exist in the repo today, and this shape does not
   presume its package layout. When it arrives, its kit is decided on its own
   merits (see Open Questions), and if it and `stt` genuinely share an ALSA
   capture/playback substrate that substrate becomes a shared `kit/audio` kit
   beneath both — grouped by shared substrate, not by the fact that both make
   sound. Naming a parent `voice` would group by medium, which is the mistake
   ADR 0010 corrected for `interfaces`.

   The word **"interfaces" is not used** for this namespace: ADR 0010 retired it
   as a capability label, and in Go `interfaces` collides with the language's
   own vocabulary for the type contracts (`Portal`, `Connector`) this repo
   already names.

6. **A portal may present output directly or expose an egress tool; both are
   supported and neither is privileged.** The repo already has two outbound
   mechanisms and they do not conflict: a portal **renders** what the navi
   produces (the TUI draws `TurnAssistant` turns from the observation stream with
   no tool involved), and a navi may declare an **egress tool**
   (`connector.SendTool()`, ADR [0014](./0014-connectors-are-bidirectional-service-integrations.md))
   that lets the agent *choose* when to emit. A voice portal is not blocked on a
   new component kind: whether the navi speaks every turn (rendering) or decides
   when to speak (a `portal_say`-style tool) is a **per-navi composition choice**,
   visible in `main.go` exactly as ADR 0014 decision 2 requires. This record does
   not decide which a conversational portal should use — only that both exist and
   that neither requires extending the taxonomy.

7. **Push-to-talk streams; it does not block.** The TUI gains a `Talk` binding
   (declared once in `portal/tui/keys.go`, per NAVI-593 / ADR
   [0038](./0038-the-portal-tui-is-a-richer-client.md) decision 7, so dispatch
   and the advertised footer derive from the same data). While the key is held,
   audio is captured and fed to `Stream`; confirmed words render as they arrive
   and the unconfirmed tail is shown as pending. Transcription runs **off the
   Bubble Tea update loop** — a `tea.Cmd` returning a transcript message, the
   same shape as the existing `Pump`/`feedMsg` pattern — because a native call
   cannot be interrupted by context cancellation and would otherwise freeze the
   UI.

8. **A screenless device reuses the kits; it is not a separate stack.** The
   mic-only device (`portal/device`) composes `kit/stt` with button, LED and
   speaker feedback and no renderer; the screened device (`portal/handheld`)
   composes the same `kit/stt` with `kit/view`. Neither is a new component type —
   both are ordinary `Portal` implementations, and `Addressed` already
   establishes that a portal declares optional capabilities by implementing
   optional interfaces.

9. **One stream per process is a shared resource, and the design respects it.**
   The Cactus engine holds **one live stream per process** and returns
   `ErrStreamActive` to a second `Stream` or a concurrent speech call. If a navi
   runs two voice-capable portals in one process, they must serialize access
   through one shared handle; `portal/kit/stt` owns that serialization rather
   than each portal reimplementing it.

## Consequences

- **The thought system and the provider layer are untouched.** No SDK change to
  `model/`, `internal/llm`, or the thought queue is required for voice — the
  portal produces a string and calls `Enqueue`. This is the property that makes
  voice cheap to add to *any* navi.
- **Only navis that want voice carry the engine.** `portal/` and every other
  component package stay stdlib-only; the NAVI-355 contract-purity check keeps
  it that way, because `portal/kit/stt` is not a contract package.
- **`portal/view` moves to `portal/kit/view`.** A mechanical move, but it touches
  every importer (`portal/tui`, tests) and must land with the ADR's rule so the
  convention has no grandfathered exception.
- **The tree now says "part vs. whole" structurally.** A reader scanning
  `portal/` sees complete portals; a reader scanning `portal/kit/` sees the
  pieces they compose. This is the clarity that a top-level `ui/` tree would
  have *lost*, by moving parts and wholes into different trees.
- **`cactus-go` is a community, early-development dependency** ("its API may
  change"). The wrapper interface in `portal/kit/stt` is the seam: a breaking
  change, an abandoned project, or a swap to a cgo binding or a hosted API
  touches one package and nothing above it.
- **Cross-compilation stays ordinary.** Because `cactus-go` is CGO-free, building
  for the ARM64 test device is a `GOARCH`/`GOOS` build, not a toolchain project.
- **Adding `cactus-go` is the four-file class of change ADR 0045 describes.** It
grows the root module graph (`purego` and its closure), so the `go.mod`/`go.sum`
refresh, the `internal/testdata/` modules, and — if the directive moves — the CI
workflow's `go.work` heredoc all move **in the same PR as the dependency**, and
the PR is verified with ADR 0045 decision 4's command
(`GOTOOLCHAIN=go1.26.0 GOWORK=off GOFLAGS=-mod=readonly go build ./...`), not the
local toolchain. This record's own measured cost is deferred to that PR.
- **A device portal is cheap to write and cheap to trust.** It is a `Portal`
  implementation in an out-of-repo module importing `portal/kit/stt` — an
  ordinary Go module, per ADR [0005](./0005-build-time-modules-are-the-extension-path.md).

## Alternatives considered

- **Speech as a connector.** Rejected: it would make every voice-capable navi
  import an audio stack and make audio an agent concern, inverting ADR 0010's
  direction test. A microphone does not "bring work in" from a service; it
  produces a human's prompt.
- **Speech as a provider/model capability** (audio input in `model/`, `Content`
  becomes multimodal). Rejected *for this record*: it changes the SDK's core
  contract for a capability that a portal can deliver without touching it. If
  audio tool calling is ever wanted, that is a separate provider ADR.
- **A top-level `ui/` or `interfaces/` tree beside the component packages.**
  Rejected: it splits *parts* from *wholes* into different trees, so
  `portal.Portal` and the things that are portals end up apart, and the reader
  loses the layer map. `interfaces` is additionally a retired label (ADR 0010)
  and a Go-vocabulary collision.
- **A `portal/<x>` naming convention with a CI check, no `kit/` directory.**
  Rejected in favour of the structural namespace: the convention is invisible
  until you read a doc comment, whereas `portal/kit/` says "part" in the import
  path. (The CI check is still worth having; it is not a substitute for the
  directory.)
- **A `portal/kit/voice/` parent holding both `stt` and a future `tts`.**
  Rejected: it groups by *medium* rather than by *direction*, and this repo
  classifies capabilities by direction (ADR 0010). Speech input and speech
  output are separate capabilities, TTS does not exist in the repo today, and
  the parent would hold one real child and one imagined one. If the two later
  share an ALSA capture/playback substrate, that substrate becomes `kit/audio`
  beneath both, grouped by shared code rather than by the fact that both make
  sound. Naming the kit `voice` would repeat the mistake ADR 0010 corrected for
  `interfaces`.
- **Folding speech *output* into this record** (deciding TTS's package shape,
  engine, or mechanism now). Rejected: it would decide a capability that does not
  exist yet against a design that has not met it. This record fixes the *input*
  path and explicitly leaves output to its own decision (Open Questions 6-7),
  because the two are independent and the input path is what the near-term work
  needs.
- **`portal/kit/voice/stt` (nesting STT under a `voice` parent)**, or a two-
  package `voice` + `voice/stt` split. Rejected: the second package existed only
  to create a dependency boundary, but `stt` is already the boundary, so the
  split buys nothing. The push-to-talk state machine is not a peer of the
  transcriber — it exists to feed one — so it belongs *inside* `kit/stt`.
- **Writing our own Go binding** (cgo against `libcactus.a`, or a fresh
  `purego` wrapper). Rejected: `cactus-go` already exists, is CGO-free, and
  covers Whistle's streaming and embedding APIs; writing one is cost without a
  capability.
- **Blocking transcription on key release** (record, then transcribe). Rejected
  as the primary interaction: it gives no feedback until the utterance ends.
  Streaming is the model's own mode and the behaviour a push-to-talk key should
  have.

## Open questions

1. **Commit-on-release vs. confirm-on-release for a screenless device.** The TUI
   lands the transcript in the steer input and the human presses enter; a device
   with no screen must either enqueue on button release (walkie-talkie) or offer
   a confirmation gesture (a second press, a held LED). This decides whether
   `kit/stt` produces *a string* or *a committed thought*.
2. **Where the weights are fetched and cached on a device.** `cactus-go` caches
   under `~/.cache/cactus/`; a device image may want them baked in or pinned at
   first run. `FetchWhistle` exists for offline setup.
3. **Whether `Embed` earns its place initially.** Speech embeddings enable
   wake-word and speaker similarity without decoding, but they are a second use
   of the one-stream-per-process resource. The wrapper should expose them; the
   first device need not use them.
4. **Audio capture and resampling.** `cactus-go` takes 16 kHz mono `float32`
   PCM; microphone capture and resampling "belong to the caller" (upstream's
   words). Whether OpenNAVI ships that (`soxr`-like) or leaves it to the device
   module is undecided.
5. **Speech output (TTS): engine and mechanism.** A conversational portal —
   "talk with your navi" — needs synthesis as well as recognition, and it is a
   near-term goal. Undecided: which engine (the personal-assistant design names
   Piper or eSpeak, with viseme output for lip-sync), and whether a voice portal
   presents output by **rendering** the assistant's stream or by exposing an
   **egress tool** the agent calls (decision 6 permits both). Whether TTS gets
   its own `kit/tts`, and whether `kit/audio` is warranted as a shared
   capture/playback substrate, is decided when the implementation shows what is
   actually shared.
6. **Whether a conversational portal is a new complete portal or the TUI with
   voice.** A portal that speaks and listens could be `portal/conversational` or
   the existing `portal/tui` gaining `kit/stt` (and later TTS). This record does
   not decide it; the package shape above leaves room for either.
7. **Wake word.** The existing personal-assistant design assumed Porcupine or
   Snowboy for always-on wake-word detection. Whether that stays a separate
   engine or is subsumed by a future Whistle capability is open.

## Related

- [ADR 0010](./0010-three-capability-layers.md) — the three capability layers,
  and the retirement of "interface" as a capability label
- [ADR 0004](./0004-component-packages-public-implementations-private.md) — the
  component-purity rule this record applies
- [ADR 0041](./0041-go-libp2p-as-the-peer-transport-dependency.md) — the
  dependency-confinement pattern this record reuses
- [ADR 0045](./0045-the-go-directive-and-the-ci-workspace.md) — what adding a
  dependency costs the build, and how such a PR is verified
- [ADR 0038](./0038-the-portal-tui-is-a-richer-client.md) — the TUI's keymap
  discipline the `Talk` binding extends
- [ADR 0005](./0005-build-time-modules-are-the-extension-path.md) — how an
  out-of-repo device portal is distributed
- `docs/case-study/personal-assistant-navi/SOLUTION.md` — the hardware device
  design (Pi Zero 2W + Whisplay HAT) this fills the missing STT box for
