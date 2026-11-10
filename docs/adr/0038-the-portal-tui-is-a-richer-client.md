---
status: accepted
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

3. **Home is the default screen, and its left pane is chosen by how many
   thoughts are running.** The TUI opens on a home screen whose left pane
   varies with the running count, with the queue as a right pane at a **66/33
   split** when the terminal is wide enough:

   - **One running** — the steer page for that thought: the conversation with a
     **persistent steer input** focused on arrival, so steering the running
     thought is the default action and costs no keystroke to reach.
   - **Two or more running** — an overview list of the running thoughts in the
     queue's own table shape. It is **selectable**: choosing one opens that
     thought's steer layout in the left pane, so the overview launches rather
     than dead-ends. This state is reachable in any session, since
     `MaxConcurrent` defaults to 2 (`navi/navi.go`), so the pane re-derives on
     lifecycle events rather than at start-up.
   - **None running** — a new-thought page: an input box to enqueue, in the
     shape of an editor's landing state when no session is open.

   The input is **contextual** — a steer box when a thought is running, an
   enqueue box when none is — so there is never a steer field with no target.
   The queue is a **separate page** as well as a right pane, not the default;
   when the right pane collapses on a narrow terminal nothing is lost, because
   the queue remains reachable as its own tab.

4. **The queue is a page with filters.** A dedicated queue view lists thoughts
   as a table with a filter bar and sort control. Filtering uses the existing
   `object.Query.Filter` — the surface already implements equality and `$in`
   (`navi/types.go`), and the TUI currently never passes it. No new backend
   query capability is introduced.

5. **Opening a thought reuses the home chat component.** The conversation
   renderer is one component with two contexts (home's running thought, a
   thought opened from the queue); an opened thought adds a declared-detail
   pane beside it. There is no second chat implementation.

6. **The TUI is terminal-aware, with three discrete form factors.** The model
   handles `tea.WindowSizeMsg`, sizes every component from it, and measures
   text with `lipgloss.Width` so truncation and column layout are rune-correct.
   Responsiveness is expressed as **named form factors** rather than a
   continuum, so each is testable as a fixed-size render:

   - **Wide (≥100 columns)** — tab bar plus the 66/33 home split.
   - **Standard (60–99 columns)** — tab bar plus a single pane; the queue pane
     collapses and the queue is reached by its tab.
   - **Narrow (<60 columns)** — the tab bar compresses, the chat viewport clamps
     to a three-line minimum, and a "terminal too narrow" notice is shown rather
     than a corrupted row. 60 columns is the **hard floor**: below it the TUI
     renders its best-effort single pane and says so, and never emits a row
     wider than the terminal.

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
   assistant content. Until the upstream reasoning event exists (see "Questions
   resolved on review"), the role renders nothing; wiring it is a one-line
   change to the event fold when the event arrives.

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

## Questions resolved on review

These were recorded open when this ADR was first written. Each is answered
below; the answers are the authoritative reading, and the amendment above
records the two factual corrections the review also made.

1. **Where does reasoning content come from?** No reasoning-delta event exists
   today in the provider stream, the agent event set, or the portal observation
   surface, so surfacing it is an upstream change (provider → agent → portal)
   that this ADR anticipates but does not deliver. The event is a **distinct
   kind** — `agent_reasoning_delta`, mapped onto the existing `CategoryAgent` —
   rather than a flag on `content_delta`, because the TUI's event fold already
   switches on `Kind`, so the reasoning role is one added case, whereas a flag
   would require every reader of `content_delta` to learn about it. Until the
   event lands the reasoning role renders nothing, exactly as decision 9
   provides. Upstream work: NAVI-590.
2. **What is the queue filter's full vocabulary?** **Toggles.** The surface
   implements exactly two operators — equality and `$in` (`navi/types.go`) — so
   the filter UI expresses exactly those and nothing more, which is what keeps
   decision 4's "no new backend capability" honest. A `:` command bar is
   **sugar over the same predicates**, added later (NAVI-597) and required to
   produce an identical filter state rather than a second code path. A query
   language implying operators the surface lacks (`>=`, substring, negation) is
   an object-surface change under ADR 0017, not a TUI change.
3. **Is mouse support in scope?** **Yes.** It is enabled for the primary
   hit-targets: tab bar, list rows, the chat viewport, and detail action rows.
   Because the same support is what would make **touch** viable, hit-targets
   are kept **coarse** — a whole tab, row, or pane, never a single cell — so
   touch is not foreclosed by a keyboard-only target grid. Full gesture
   handling is deliberately not committed. NAVI-596.
4. **Does the layout gain tabs, or a persistent sidebar?** **Tabs**, in a
   k9s-style persistent bar: `1 home`, `2 queue`, `3 memory`, `4 status`. A
   sidebar is rejected because it takes width from the conversation the home
   screen exists to show; a palette is rejected as the *primary* navigation
   because it hides the screen inventory behind a keystroke. **Detail is not a
   tab** — a thought's detail or transcript is a pushed screen within whichever
   tab opened it, so the tab bar always answers "where am I". **Settings is an
   overlay, not a tab**: appearance only (theme, colour profile, default sort,
   narrow-floor behaviour), because a tab is state you are looking at and
   settings is state you are changing about the UI. A network page becomes a
   fifth tab once a `peer` object type exists (NAVI-594, NAVI-595); it is
   **deliberately not built before then**, because a bespoke screen reading
   peer state directly would be the first violation of decision 2 and would
   falsify this ADR's safety claim on the first tab built after it. The memory
   and status tabs are NAVI-599 and NAVI-598; status is the **one tab with a
   bespoke element** — queue depth, concurrency, and stream health are model
   state rather than descriptors — which is a conscious exception, and if it
   grows large the correct answer is a read-only status type on the object
   surface rather than a private TUI data path.
5. **What is the narrow-terminal floor?** **60 columns**, with the degraded
   layouts named in decision 6's form factors. This is a decision, not a
   default: below the floor the TUI says it is too narrow rather than
   corrupting the display.

## Amendment — two corrections to the Context

**Recorded on review, before implementation (NAVI-586).** The decisions above
were reviewed against the code and folded in the review's changes to decisions
3 and 6, and the open questions are resolved in the section above. What remains
here is the review's two corrections to the Context, which are not changes to a
decision and so are recorded rather than applied.

Two details in the Context were **understated**, and implementation depends on
them:

- **The byte-length padding is not confined to table cells.** The Context
  names `repeat("-", 60)` and `fmt.Sprintf("%-*s", …)`. The same byte
  assumption also lives in `m.clip` and `sanitizeLine`, which slice with
  `len(s)` and `s[:n]` — so a multi-byte rune is corrupted in *any* clipped or
  sanitized string, not only in a column value. Rune-correct truncation is
  therefore a property of the text helpers, not a table-rendering fix, and the
  foundation slice must correct it there.
- **`Confirm` is declared on three actions, not one.** The Context names the
  `thought` descriptor's `cancel`. `Confirm: true` is also set on the `remove`
  action of both tool-state descriptors in `navi/toolstatetypes.go`. This
  strengthens decision 8 rather than changing it: the flag already flows
  through `view.Action.Confirm` for every descriptor, so enforcement is a
  single generic path in the TUI and covers all three for free. Enforcement is
  **a TUI-side change, not a descriptor or surface change** — no backend work.

Decision 2 was verified directly, because it is the claim the whole revamp's
safety rests on: the ADR 0017 object surface, the ADR 0019 stream, and the
`portal/view` toolkit are untouched by anything this work requires. It holds.

## Amendment — the keymap binding on a screen with a persistent input

**Recorded on implementation (NAVI-587).** Decision 7 binds actions to the
focused object rather than to a mode. On home and on a conversation that
screen also carries a **persistent text input focused on arrival** (decision
3), and a terminal cannot tell "pause this thought" (`s`) from "type the
letter s" without being told. Decision 7 did not settle that collision; this
amendment does.

The binding is **an explicit input-focus state plus a leader key**, and the
reason for two mechanisms is that each covers the other's gap:

- **Plain action letters** (`s`/`r`/`c`/`R`/`p`/`P`) bind to the focused
  object **in every mode**, and are the fast path. They fire wherever a text
  input does **not** have focus: the queue, the detail view, and home while
  the steer input is **blurred**. While the input is **focused**, a printable
  rune is text and never an action — the mistake in the other direction.
- **`esc` on an empty buffer blurs the input** rather than leaving the screen
  (on a non-empty buffer it still clears it, unchanged). `tab` toggles focus,
  and any printable rune typed while blurred re-focuses the input and inserts,
  so typing is never a mode that must be re-entered. Plain action letters are
  therefore one key away on home.
- **`ctrl+a` is the leader.** It opens a small menu of the focused object's
  **declared** actions, and a letter dispatches one. It is not a printable
  rune, so it works with the input focused — the escape hatch for acting
  without leaving the steer field. Because the menu is built from the
  descriptor, a new object type gets a correct menu with no new binding
  (decision 2), and the menu **omits** an action that does not apply to the
  object's status rather than advertising a key whose only outcome is an
  error (decision 7). `steer` is deliberately absent from the menu: the
  persistent input *is* its UI.

Rejected: plain letters acting while the input is focused (it would make `s`
untypeable); a modifier per action (`ctrl+r`/`ctrl+c`/`ctrl+s`/`ctrl+p` are
terminal- and readline-significant, and the chat already owns the ctrl
combinations it needs for scrolling and folding).

Two consequences of decision 3 that this amendment fixes as well:

- **Home must be reachable from everywhere.** A default screen you can only
  visit once is not a default. Until the tab bar of decision 4 exists
  (NAVI-593), `1` returns home — the tab bar's own home key, so the binding
  survives that change.
- **A thought that is still on the board stays tracked.** Pausing the thought
  home is showing must not make it vanish: you paused it in order to resume
  it, and home is where you do that. Home advances to the next running thought
  when the tracked one reaches a terminal state (complete/failed/cancelled) —
  but when **nothing** is running, **the finished conversation stays on
  screen**. Watching a thought finish and then reading its result is the whole
  point of home; blanking the transcript the instant it completes destroys the
  thing the reader was waiting for. The empty state is reached by starting
  something new, or by the thought leaving the page. A buffered steer is
  **kept** across a tracked-thought change and re-targets on send, with the
  panel title naming the current target so the re-target is visible.
- **The panel title's status suffix names the thought's lifecycle.** It leads
  with `live` / `queued` / `paused` / `complete` / `failed` / `cancelled` and
  carries the view's own state (`· following`, `· following paused (ctrl+f)`)
  after it. The colour comes from the same status-to-colour mapping a table
  cell uses, so a state means one colour everywhere in the TUI; a screen that
  named the thought and showed its content but never said whether it was still
  working was missing the one fact the reader came for.

Decision 3's **two-pane 66/33 split** is not delivered by this amendment or by
NAVI-587. The single-pane home is; the split is tracked separately (NAVI-601),
because it is a layout over the queue page (NAVI-592) and is the **wide-only**
form factor of decision 6.

## Amendment — the chrome is lipgloss's, not a private vocabulary

**Recorded on implementation (NAVI-602).** The Context above describes the
panel chrome as "opencode's spatial structure with augmented-ui's visual
language", and decision 1 adopted the Charm stack so the TUI would stop
hand-rolling presentation. The chrome was hand-rolled anyway: a private border
vocabulary (`borderClipped`, the `╱ ┐ └` clipped-corner pastiche), a
`frame.render` that drew every rail and rule by character arithmetic, and
`pad`/`truncate`/`alignCell`/`displayWidth` helpers reimplementing
`lipgloss.Width`, `Style.Width`, `Style.MaxWidth`, and `Style.Align`.

**The chrome is now lipgloss's.** The panel is a `lipgloss.Style` with
`RoundedBorder()` and `Padding`, sized with `Width`/`Height`, and the titled
top edge is composited from styled blocks with `lipgloss.Width` measurement
rather than from a private vocabulary of glyphs. The text helpers are the
library's primitives. This **supersedes the augmented-ui chrome vocabulary**
named in the Context; the *structure* decision 6 describes — a framed panel,
three form factors, a 60-column floor — is unchanged.

Two findings worth recording, because both are traps a later change would
re-enter:

- **`Style.Width` wraps overflow; it does not clip it.** Every single-row
  caller (a table cell, the footer, a border edge) must truncate *before*
  padding with `Width`, or the line silently becomes two rows and pushes the
  frame's bottom border and footer out of the terminal. This is why `fit`
  exists and why body lines are fitted before the panel style sees them.
- **`Style.Width`/`Height` are the content box.** lipgloss draws the border
  outside them and padding inside, so a caller with a terminal size subtracts
  the border. `frame.render` is the one place that arithmetic lives.

The frame's **exact-size contract** is unchanged and is the check on the whole
migration: the output is still exactly `height` lines of exactly `width`
display columns, asserted by `TestFrameIsExactlyTerminalSized` and the goldens
at every form factor.

**Not in scope, deliberately:** the list's column layout (`columnLayout`,
`grid`) stays custom. `bubbles/table` takes static column widths, while these
are resolved from the object surface at runtime with per-kind alignment,
per-column floors, left-truncation for identity columns (NAVI-600), and
low-value columns dropped on a narrow terminal — adopting it would regress
decision 2, that a new object type needs no new screen.

**Follow-up:** the keymap and the persistent input are still hand-rolled
(`chatKeys`, `helpLine`, `menuLetter`, and a `string` with manual backspace).
Moving them to `bubbles/key` + `bubbles/help` + `bubbles/textarea` is NAVI-593.

## Amendment — the keymap is declared, and the help is generated from it

**Recorded on implementation (NAVI-593).** Decision 7 says actions are bound to
the focused object and that "the footer help is derived from the active
bindings, so a key is never advertised that does not work". The first half was
delivered by NAVI-587; the second half is this amendment.

Before it, the help was a hand-kept string per context (`helpLine`) and the
dispatch was a `switch k.String()` — two lists that had to be kept in agreement
by hand, and did not always agree (the whole revamp started from a help line
advertising keys that were inert). Now every key is a `key.Binding` declared
once in `keys.go`, and **both** the dispatch and the footer read the same
declarations. Each context supplies a small `help.KeyMap` over the bindings
that are live in it, so a context cannot advertise a key it does not handle.

Two consequences worth recording:

- **The help truncates rather than overflows.** `bubbles/help` renders from the
  bindings and clips to the terminal width; the hand-kept string overflowed any
  narrow terminal. (`help` clips *items* and can still emit a line wider than
  its `Width`, so the footer enforces the final width — that is a property of
  the library, not of the binding.)
- **`?` opens a full-screen help overlay, and is typable while a text input has
  focus.** The rule "a printable rune is text while a text input owns the
  keyboard" now lives in one predicate (`inputOwnsKeyboard`) that the help key,
  the action letters, and the leader all consult. `?` is a character people
  write ("does this work?"), so claiming it while typing would make the steer
  input unable to express a question — the same defect as an advertised key
  that does nothing, in the other direction. Blurred, `?` opens help.

The overlay reports the keys of the screen **underneath** it, not its own: a
reader who asked for help wants to know what they can do where they were.

**Not in this amendment:** the persistent input is still a `string` with manual
backspace handling rather than `bubbles/textarea`. That is a behaviour change
(it brings its own key handling and can be multi-line, which changes the
frame's input-row accounting) and is tracked separately.

## Amendment — the screen is regions, not one panel

**Recorded on implementation (NAVI-603).** Decision 6 describes the form
factors and the frame; the implementation put *everything* inside one bordered
panel — the conversation, the input, and the status line, with the help
underneath it. That shape cannot express a home screen with a header and a
sidebar, and it is why home did not read as a screen you are *in*.

The frame now composes **regions**, in the shape crush uses
(`charmbracelet/crush`, `internal/ui/model/ui.go` `generateLayout`/`uiLayout`):

```
header        what you are looking at, plus the secondary facts
-----------------------------------------------
main          the variable region: conversation, table, status
-----------------------------------------------
editor        its own bordered region, only on screens that take typing
-----------------------------------------------
status        the transient message and the generated help
```

Three consequences worth recording:

- **The editor is a region, not a row of the content.** It has its own border
  and its own focus, and it does not scroll away with the conversation — which
  is what makes it read as a place to type rather than as a line in the
  transcript.
- **The header is a region, not a label on a border.** The screen's identity is
  on the left and the secondary facts (a thought's lifecycle, the view's
  scroll state, the running activity) on the right. Decision 6's form factors
  and the 60-column floor are unchanged; what changed is that the header is now
  somewhere to put things.
- **`render` and `regionRows` compute the layout once.** The row arithmetic had
  drifted between the two — the status bar floated above the bottom or was
  clipped off it — so the heights are derived in one place and the gutters are
  emitted where they are counted. The exact-size contract (exactly `height`
  lines of exactly `width` columns) is unchanged and is the check on all of it.

**Not yet done, and why this is an amendment rather than the whole story:**
the home sidebar (NAVI-601) and the queue and status pages (NAVI-592,
NAVI-598) are built on these regions in follow-up work. Decision 4's answer to
open question 4 — **tabs, not a sidebar** — is not changed here; the sidebar
decision 4 rejected was a *persistent navigation* sidebar, and NAVI-601 adds a
*content* pane on home only.

## Amendment — home's sidebar is a content pane

**Recorded on implementation (NAVI-601).** Decision 3 describes the left pane
and the queue as a right pane at a 66/33 split, and decision 6 makes that split
the wide-only form factor. Both are now built: on a wide terminal home shows
the running thought's transcript on the left and the queue on the right; below
the wide floor the pane collapses to the single-pane home, and nothing is lost
because the queue is reachable as its own page.

This does **not** reverse decision 4's answer to open question 4 (tabs, not a
sidebar). The sidebar decision 4 rejected was a *persistent navigation*
sidebar — one that would be present on every screen and take width from the
conversation everywhere. This is a **content pane on home only**: it shows what
is queued while you watch what is running, and it is absent everywhere else.
The distinction is the point; if a sidebar later appears on other screens, that
is a different decision and needs its own amendment.

The sidebar is deliberately **not** the full queue table: it shows the prompt
and the lifecycle state, because a 33% column cannot show an id *and* a prompt
*and* a date, and the prompt is what a reader scans for. Filtering and sorting
are the queue page's job (NAVI-592).
