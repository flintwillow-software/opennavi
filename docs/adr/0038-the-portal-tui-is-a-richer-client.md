---
status: proposed
date: '2026-10-28'
supersedes: []
tags:
  - adr
  - portal
  - tui
  - presentation
---

# 0038 — The portal TUI is a richer client: a persistent home, a filterable queue, and a real presentation layer

## Context

The portal TUI (`portal/tui/`) is the human door into a navi (ADR 0012/0019).
Its architecture is sound and stays: it renders **descriptor-driven** screens
through the `portal/view` toolkit (ADR 0017), writes through **one** path
(`object.Surface.Act`, ADR 0019 decision 7), and is fed by a **push** pump woken
by the mailbox rather than a timer (ADR 0019 decision 5). The same model serves
in-process and remote behind the `Transport` seam.

The **presentation and interaction layers** are what did not keep up, and four
defects make the TUI feel cobbled together rather than deliberate:

1. **It cannot see the terminal.** `tea.WindowSizeMsg` is never handled — the
   model has no width or height field at all. Rendering assumes 80 columns
   (`repeat("-", 60)`, `fmt.Sprintf("%-*s", …)`), so the UI never fills the
   terminal, and table columns are padded by byte length rather than rune width,
   so a CJK or emoji value in a cell corrupts the row.
2. **Actions are gated on list mode.** Every action key (`s` pause, `r` resume,
   `c` cancel, `R` retry, `p`/`P` priority, `a` attach) is guarded by
   `m.mode == ModeList` or goes through `m.selected()`, which returns false
   outside list mode. The chat screen — where a user most wants to steer or
   cancel the thought they are watching — is therefore **inert**. A key that
   renders in the help line but does nothing is worse than a missing key.
3. **There is no home.** The default screen is a flat list of the selected type.
   A navi's whole purpose is the thought it is running now, and the TUI gives it
   no privileged place — the running thought is one row among two hundred, and
   steering it requires selecting the row, pressing `a`, typing, and pressing
   enter.
4. **`Confirm` is declared but unenforced.** The `thought` descriptor sets
   `Confirm: true` on `cancel` (`navi/objecttypes.go`). The model reads
   `view.Action.Confirm` only to print a `!` in the detail view
   (`renderDetail`); no code path ever asks. Pressing `c` cancels a running
   thought immediately, on one keystroke, with no undo.

There is also a **capability gap the TUI is not at fault for**: there is no way
to display a model's reasoning. `model.Reasoning` (ADR 0034) is a *request-side*
parameter — it asks a provider to think. The provider stream
(`internal/llm/types.go`) has no reasoning-delta event, the agent
(`internal/agent/types.go`) has no reasoning event kind, and the portal
observation surface carries none. `UsageInfo.ThinkingTokens` counts thinking but
never shows it. So the pi/opencode-style reasoning display this work wants
**requires an upstream feature** and cannot be delivered by the TUI alone.

Finally, the dependency question. Bubble Tea is the repo's first third-party
dependency, an explicitly approved exception scoped to NAVI-319 with a doc
comment recording it. `lipgloss` is already in the module graph as an *indirect*
dependency via Bubble Tea, and `bubbles` — the companion component library — is
not present. Building a proper TUI by hand means reimplementing viewports,
rune-aware truncation, text input with a cursor, and help generation, badly.

## Decision

1. **The portal TUI uses the full Bubble Tea stack.** `lipgloss` (styling,
   layout, rune-width measurement) and `bubbles` (viewport, textarea, list,
   help, key, spinner) become direct dependencies. This supersedes the
   NAVI-319 "Bubble Tea only" scoping: the exception is now to the Charm stack
   as a whole, still scoped to `portal/tui/`. The no-new-deps convention holds
   everywhere else.

2. **Presentation is not part of the portal ABI.** The ADR 0017 object surface,
   the ADR 0019 stream, and the `portal/view` toolkit are unchanged by this
   work. The TUI remains a *renderer* of descriptors: a new object type still
   needs no new screen. Anything this work cannot express through the existing
   descriptors is a signal that the toolkit is missing a capability, and that
   capability is added to `portal/view` rather than special-cased in the TUI.

3. **Home is the default screen and it shows the running thought.** The TUI
   opens on a home screen that presents the currently-running thought as a
   conversation, with a **persistent steer input** that is focused on arrival —
   steering the running thought is the default action and costs no keystroke to
   reach. When nothing is running, home renders an empty state that offers
   enqueue. The queue is a **separate page**, not the default.

4. **The queue is a page with filters.** A dedicated queue view lists thoughts
   as a table with a filter bar and sort control. Filtering uses the existing
   `object.Query.Filter` — the surface already implements equality and `$in`
   (`navi/types.go`), and the TUI currently never passes it. No new backend
   query capability is introduced.

5. **Opening a thought reuses the home chat component.** The conversation
   renderer is one component with two contexts (home's running thought, a
   thought opened from the queue); an opened thought adds a declared-detail
   pane beside it. There is no second chat implementation.

6. **The TUI is terminal-aware.** The model handles `tea.WindowSizeMsg`, sizes
   every component from it, and measures text with `lipgloss.Width` so
   truncation and column layout are rune-correct. Layout is responsive: panes
   collapse on narrow terminals rather than overflowing.

7. **Keys are a context-sensitive keymap, and the help line is generated from
   it.** Actions are bound to the focused object rather than to a mode, so a
   thought can be acted on wherever it is shown. The footer help is derived from
   the active bindings, so a key is never advertised that does not work.

8. **A declared `Confirm` action requires confirmation.** Any action whose
   descriptor sets `Confirm: true` presents a confirmation before dispatch,
   through the same descriptor-driven action path. Destructive acts are not
   single-keystroke.

9. **The chat renderer is forward-compatible with reasoning.** The turn model
   carries a reasoning role that renders dimmed and collapsible, distinct from
   assistant content. Until the upstream reasoning event exists (see Open
   questions), the role renders nothing; wiring it is a one-line change to the
   event fold when the event arrives.

## Consequences

- The TUI becomes a real client: a navi's running thought is always one glance
  away, steering is the default gesture, and the queue is a tool rather than the
  entry point.
- Two direct dependencies are added to a repo whose convention is no new
  dependencies. They are confined to `portal/tui/` and already transitively
  present for `lipgloss`; `bubbles` is new to the tree.
- `tui_test.go` is ~50KB and asserts on current render strings. Much of it will
  be rewritten. The render seam should be made testable (fixed-size golden
  output) **before** the churn, not after.
- The TUI no longer assumes 80 columns, so narrow-terminal and wide-terminal
  behaviour becomes a thing to test rather than an accident.
- Unenforcing `Confirm` is a behaviour change users will notice: cancel becomes
  two keystrokes. That is the point.
- Descriptor-driven rendering is preserved, so the cost of a new object type
  stays near zero.

## Alternatives considered

- **Keep it plain-text and improve it in place.** Rejected: the missing
  terminal-size handling, rune-width correctness, viewport scrolling, and
  cursor-bearing text input are all things `bubbles`/`lipgloss` already do
  correctly, and hand-rolling them is how the current state was reached.
- **`lipgloss` only, no `bubbles`.** Rejected: styling alone leaves the
  viewport, textarea, and generated-help problems unsolved, which are the
  interaction defects rather than the cosmetic ones.
- **Make the queue the default screen with a running-thought banner.** Rejected:
  it preserves the current inversion, where the queue is the subject and the
  running thought is an annotation on it.
- **A web UI instead.** Out of scope, and does not remove the need for a
  terminal client. ADR 0017's surface is what makes either one cheap.
- **Block this work on the reasoning event.** Rejected: reasoning display is one
  element of a larger presentation revamp and should not gate it. The turn model
  is made ready for it instead.
- **Enforce `Confirm` as a separate fix.** Rejected: it is a correctness defect
  in the same action-dispatch path this work rewrites, and splitting it invites
  the two halves drifting.

## Open questions

1. **Where does reasoning content come from?** No reasoning-delta event exists
   today in the provider stream, the agent event set, or the portal observation
   surface. Surfacing it is an upstream change (provider → agent → portal) that
   this ADR anticipates but does not decide. Until it lands, the reasoning role
   renders nothing. Whether the eventual event is a distinct kind or a flag on
   `content_delta` is left open.
2. **What is the queue filter's full vocabulary?** Status, priority, type, and
   prompt text are the obvious axes; whether it grows a query language (a `:`
   command bar) or stays a set of toggles is undecided.
3. **Is mouse support in scope?** The decision is silent; the stack supports it.
4. **Does the layout gain tabs, or a persistent sidebar?** Home/queue/detail is
   three screens with a clear order; whether they are tabs, a sidebar, or a
   command palette is a presentation choice left to implementation.
5. **What is the narrow-terminal floor?** The decision requires responsive
   collapse but names no minimum size; a floor (e.g. 60 columns) and its
   degraded layout are undecided.
