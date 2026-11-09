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

## Amendment — the five open questions, resolved

**Recorded on review, before implementation (NAVI-586).** The decisions above
were reviewed against the code and folded in the review's changes to decisions
3 and 6; the two factual corrections and the resolution of the open questions
are recorded here.

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

The five open questions resolve as follows:

1. **Reasoning content comes from a distinct event kind.** The provider gains a
   reasoning-delta event, the agent event set gains a matching kind, and the
   portal maps it onto the existing `CategoryAgent` with its own `Kind` —
   `agent_reasoning_delta` — rather than a flag on `content_delta`. A distinct
   kind is the smaller change to every consumer: the TUI's event fold already
   switches on `Kind`, so the reasoning role is one added case, whereas a flag
   would require every reader of `content_delta` to learn about it. The
   upstream work is NAVI-590; until it lands the reasoning role renders
   nothing, exactly as decision 9 provides.
2. **The queue filter's vocabulary is toggles, not a command language.** The
   surface implements exactly two operators — equality and `$in`
   (`navi/types.go`) — so the filter UI expresses exactly those and nothing
   more, which is what keeps decision 4's "no new backend capability" honest.
   A `:` command bar is **sugar over the same predicates**, added later as
   NAVI-597 and required to produce an identical filter state rather than a
   second code path. A query language implying operators the surface lacks
   (`>=`, substring, negation) is an object-surface change under ADR 0017, not
   a TUI change.
3. **Mouse support is in scope.** Bubble Tea supports it and it is enabled for
   the primary hit-targets: tab bar, list rows, the chat viewport, and detail
   action rows. Because the same support is what would make **touch** viable,
   hit-targets are kept **coarse** — a whole tab, row, or pane, never a
   single cell — so touch is not foreclosed by a keyboard-only target grid.
   Full gesture handling is deliberately not committed; NAVI-596 carries both
   the mouse work and the touch constraint.
4. **Navigation is tabs, not a sidebar or a palette.** A k9s-style persistent
   tab bar: `1 home`, `2 queue`, `3 memory`, `4 status`. A sidebar is rejected
   because it takes width from the conversation the home screen exists to show;
   a palette is rejected as the *primary* navigation because it hides the
   screen inventory behind a keystroke. **Detail is not a tab** — a thought's
   detail or transcript is a pushed screen within whichever tab opened it, so
   the tab bar always answers "where am I". **Settings is an overlay, not a
   tab**: it holds appearance only (theme, colour profile, default sort,
   narrow-floor behaviour), because a tab is state you are looking at and
   settings is state you are changing about the UI. A network page becomes a
   fifth tab once a `peer` object type exists (NAVI-594, NAVI-595); it is
   **deliberately not built before then**, because a bespoke screen reading
   peer state directly would be the first violation of decision 2 and would
   falsify this ADR's safety claim on the first tab built after it.
   The memory and status tabs are new work recorded as NAVI-599 and NAVI-598.
   Status is the **one tab with a bespoke element** — queue depth, concurrency,
   and stream health are model state rather than descriptors — and that is a
   conscious exception, not an accident; if it grows large, the correct answer
   is a read-only status type on the object surface rather than a private TUI
   data path.
5. **The narrow-terminal floor is 60 columns**, with the degraded layouts named
   in the form factors above. This is a decision, not a default: below the
   floor the TUI says it is too narrow rather than corrupting the display.
