package tui

import "strings"

// The frame composes a screen from REGIONS rather than drawing one panel
// (NAVI-603). The shape is crush's (charmbracelet/crush
// internal/ui/model/ui.go, generateLayout/uiLayout):
//
//	header        what you are looking at, plus the secondary facts
//	-----------------------------------------------
//	main          the variable region: a conversation, a table, a status page
//	-----------------------------------------------
//	editor        its own region, only on screens that take typing
//	-----------------------------------------------
//	status        the generated help plus the transient status line
//
// Regions are lipgloss styles composed with JoinVertical (the NAVI-602
// pattern), not hand-drawn runes, and the editor is a REGION rather than a row
// inside the content — which is what lets a screen put a real input box at the
// bottom without it scrolling away with the content.
//
// The frame's contract is unchanged and is what every golden asserts: the
// output is exactly `height` lines, each exactly `width` display columns.
// Regions are sized from one place (regionRows) so the sum cannot drift.

// The chrome the frame spends on non-main regions. `main` is whatever is left,
// which is why it is the one region with no constant.
const (
	// frameHeaderRows is the header region: one row on a roomy terminal, and
	// it is dropped entirely on a short one (see headerRows).
	frameHeaderRows = 1
	// frameTabsRows is the tab bar: one row naming the pages.
	frameTabsRows = 1
	// frameStatusRows is the status/help bar. It is always present: a screen
	// with no help and no status still gets a blank bar, so the layout does
	// not jump by a row as messages come and go.
	frameStatusRows = 1
	// frameEditorMinRows is the smallest a usable input box can be: a border
	// top, one content row, a border bottom.
	frameEditorMinRows = 3
	// frameEditorRows is the editor's preferred height. It is a box with
	// padding, not a one-line field, so it needs a few rows to read as one.
	frameEditorRows = 5
	// frameGutter is the blank row between regions, so a border does not sit
	// flush against the region above it.
	frameGutter = 1
)

// minBodyRows is the floor the body is given even on a terminal too short to
// hold the chrome. Below this the frame clips rather than producing a
// negative height; the narrow/short notice is the renderer's job, not the
// frame's.
const minBodyRows = 3

// The form factors (ADR 0038 decision 6). Widths are terminal columns:
//
//	wide     >= 100  — tab bar plus the 66/33 home split
//	standard  60-99  — tab bar plus a single pane
//	narrow    < 60   — compressed chrome, three-line chat floor, a notice
const (
	// wideFloor is the width at or above which the home screen splits into
	// two panes.
	wideFloor = 100
	// narrowFloor is the hard floor: below it the TUI renders a best-effort
	// single pane and says the terminal is too narrow, rather than corrupting
	// the display.
	narrowFloor = 60
	// splitLeftFrac is the left pane's share of a wide home screen.
	splitLeftFrac = 0.66
)

// formFactor names the current layout regime for a width.
type formFactor int

const (
	formNarrow formFactor = iota
	formStandard
	formWide
)

func (f formFactor) String() string {
	switch f {
	case formWide:
		return "wide"
	case formStandard:
		return "standard"
	}
	return "narrow"
}

// formFactorFor classifies a terminal width.
func formFactorFor(width int) formFactor {
	switch {
	case width >= wideFloor:
		return formWide
	case width >= narrowFloor:
		return formStandard
	}
	return formNarrow
}

// frame is the sized render context a screen draws into.
type frame struct {
	width  int
	height int
	theme  theme

	// tabs is the TAB BAR's content: the page inventory (`1 home · 2 queue ·
	// 3 status`), with the active page marked by the screen. It is its own
	// region above the header, so where you can GO is always visible (ADR
	// 0038 decision 4).
	tabs string
	// title is the HEADER region's left label: what this screen is showing
	// (`home · <id>`, `queue`, `status`). It is a real region now, not a label
	// on a border.
	title string
	// subtitle is the header's secondary text, right-aligned: the facts that
	// are worth a glance but are not the identity (`live · following`, a
	// count, a model). Coloured by subtitleStyle.
	subtitle string
	// subtitleStyle colours the subtitle. A conversation colours it by the
	// thought's lifecycle, so the state is legible at a glance.
	subtitleStyle func(string) string
	// hasHeader says whether the header region is drawn. It is present on
	// every screen that has something to say; the notice path drops it.
	hasHeader bool

	// input is the EDITOR region's content (the steer/enqueue text). Empty
	// with inputPrompt empty means the screen takes no typing and the editor
	// region is absent entirely.
	input string
	// inputPrompt LABELS the editor box (`steer`, `new thought`, `priority`).
	// It is the action the input performs, set into the box's top edge in the
	// same vocabulary a conversation turn uses.
	inputPrompt string
	// inputFocused says whether the editor owns the keyboard, so it draws the
	// cursor and the box is styled as active.
	inputFocused bool

	// notice is a full-width attention line drawn above the status bar (a
	// dropped stream, an unreachable portal). It is a region of its own so a
	// warning is not buried in the help text.
	notice string
	// noticeStyle colours the notice.
	noticeStyle func(string) string

	// status is the STATUS region's left text: the transient message line.
	status string
	// help is the STATUS region's right text: the generated help (NAVI-593).
	help string
}

// panelPad is the gutter between the panel's rail and its content. One column
// is enough to read as a margin at terminal sizes; two would cost real width
// on a narrow terminal.
const panelPad = 1

// panelPadY is the blank rows between the panel's edges and its content. One
// row top and bottom keeps the content off the border without costing much
// height; on a short terminal the frame's minimum body still wins.
const panelPadY = 1

// maxInt is a local helper: the standard library's max is generic in newer
// Go, but this keeps the layout readable at the call sites.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// newFrame builds a frame for a terminal size, clamping to sane minimums so a
// zero or absurd size (some terminals report 0 before the first resize, and
// tests construct models directly) cannot produce a negative body height.
func newFrame(width, height int, t theme) frame {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return frame{width: width, height: height, theme: t}
}

// innerWidth is the columns available to a screen's content: the frame width
// less the two side rails and the space after the left one. Screens lay out
// against this rather than the raw frame width, so the right rail is never
// pushed off the edge by content sized to the full width.
func (f frame) innerWidth() int {
	w := f.width - 2*panelPad - 2
	if w < 1 {
		w = 1
	}
	return w
}

// chromeRows is the non-main rows this frame spends. It reads regionRows, so
// the height a screen is handed and the height render produces are the same
// number by construction rather than by two matching sums.
func (f frame) chromeRows() int { return f.height - f.regionRows().main }

// bodyHeight is the rows the MAIN region gets: everything the chrome does not
// take, floored so a screen always has something to draw into.
func (f frame) bodyHeight() int {
	h := f.regionRows().main
	if h < minBodyRows {
		return minBodyRows
	}
	return h
}

// form is the current layout regime.
func (f frame) form() formFactor { return formFactorFor(f.width) }

// tooNarrow reports whether the terminal is below the form-factor floor
// (ADR 0038 decision 6, 60 columns). The renderer uses this to show a notice
// instead of a corrupted row.
func (f frame) tooNarrow() bool { return f.width < narrowFloor }

// tooShort reports whether there is no room for the chrome plus a usable
// body.
func (f frame) tooShort() bool { return f.height < f.chromeRows()+minBodyRows }

// render composes the screen from its regions, top to bottom:
//
//	header | main | (editor) | (notice) | status
//
// The regions are sized by regionRows and joined with JoinVertical, so the
// total is exactly the frame height by construction rather than by two
// arithmetic paths agreeing. Every line is fitted to the frame width, so a
// screen cannot overflow however long its content is.
func (f frame) render(header string, body []string, footer string) string {
	r := f.regionRows()
	blank := strings.Repeat(" ", f.width)

	// Regions are assembled in order with a gutter between each pair, and the
	// gutter is emitted HERE rather than counted separately — the two used to
	// be computed in different places and drifted, which left the status bar
	// floating above the bottom or clipped off it.
	var regions [][]string
	if r.tabs > 0 {
		regions = append(regions, []string{fit(f.tabs, f.width)})
	}
	if r.header > 0 {
		regions = append(regions, []string{f.headerRegion()})
	}
	main := make([]string, 0, r.main)
	for i := 0; i < r.main; i++ {
		var content string
		if i < len(body) {
			content = body[i]
		}
		main = append(main, fit(content, f.width))
	}
	regions = append(regions, main)
	if r.editor > 0 {
		regions = append(regions, f.editorRegion(r.editor))
	}
	if r.notice > 0 {
		st := f.noticeStyle
		if st == nil {
			st = f.theme.Warn
		}
		regions = append(regions, []string{fit(" "+st(f.notice), f.width)})
	}
	if r.status > 0 {
		regions = append(regions, []string{fit(f.statusRegion(), f.width)})
	}

	lines := make([]string, 0, f.height)
	for i, reg := range regions {
		if i > 0 {
			for g := 0; g < frameGutter; g++ {
				lines = append(lines, blank)
			}
		}
		lines = append(lines, reg...)
	}

	// The contract: exactly height lines of exactly width columns.
	if len(lines) > f.height {
		lines = lines[:f.height]
	}
	for len(lines) < f.height {
		lines = append(lines, blank)
	}
	for i, l := range lines {
		lines[i] = fit(l, f.width)
	}
	return strings.Join(lines, "\n")
}

// regionRows is the height of each region for this frame. It is the ONE place
// the regions are sized, so render's output cannot drift from what bodyHeight
// promises a screen: bodyHeight returns regionRows().main.
type regionHeights struct {
	tabs   int
	header int
	main   int
	editor int
	notice int
	status int
}

func (f frame) regionRows() regionHeights {
	var h regionHeights
	h.status = frameStatusRows
	if f.tabs != "" {
		h.tabs = frameTabsRows
	}
	if f.notice != "" {
		h.notice = 1
	}
	// The editor is present only when the screen takes typing, and it is a
	// bordered box, so it needs the minimum to render as one. On a short
	// terminal the editor gives way before main does — you can read a
	// conversation you cannot type into, but not the reverse.
	if f.inputPrompt != "" {
		h.editor = frameEditorRows
	}
	if f.hasHeader {
		h.header = frameHeaderRows
	}
	// A gutter sits between every pair of ADJACENT present regions, which is
	// what render emits. Counting them any other way leaves the frame short
	// (the status bar floats above the bottom) or long (it is clipped off).
	// main is always present, so it contributes a gutter after itself for each
	// region below it.
	// main is always present, so the region count is one more than the number
	// of optional regions that are present, and there is one gutter between
	// every adjacent pair.
	optional := 0
	for _, n := range []int{h.header, h.editor, h.notice, h.status} {
		if n > 0 {
			optional++
		}
	}
	regions := optional + 1 // + main
	gaps := 0
	if regions > 1 {
		gaps = (regions - 1) * frameGutter
	}
	used := h.tabs + h.header + h.editor + h.notice + h.status + gaps
	h.main = f.height - used
	// Too short: shed chrome in the order that loses the least. The editor is
	// reduced to its minimum box, then dropped; the header is dropped; the
	// notice is dropped last because it is the only thing that may be telling
	// the reader their view is incomplete.
	for h.main < minBodyRows && h.editor > 0 {
		drop := h.editor - frameEditorMinRows
		if drop <= 0 {
			drop = h.editor
			h.editor = 0
		} else {
			h.editor = frameEditorMinRows
		}
		h.main += drop
	}
	if h.main < minBodyRows && h.header > 0 {
		h.main += h.header + frameGutter
		h.header = 0
	}
	if h.main < minBodyRows && h.notice > 0 {
		h.main += h.notice + frameGutter
		h.notice = 0
	}
	if h.main < 0 {
		h.main = 0
	}
	return h
}

// headerRegion is the header: the screen's identity on the left and its
// secondary facts on the right, on one row, separated by the full width.
func (f frame) headerRegion() string {
	left := f.theme.panelTitle(f.title)
	right := ""
	if f.subtitle != "" {
		st := f.subtitleStyle
		if st == nil {
			st = f.theme.Subtle
		}
		right = st(f.subtitle)
	}
	gap := f.width - displayWidth(left) - displayWidth(right) - 2
	if gap < 1 {
		// No room for both: drop the subtitle before the title, because the
		// title says where you are.
		right = ""
		gap = f.width - displayWidth(left) - 2
	}
	if gap < 0 {
		gap = 0
	}
	return fit(" "+left+strings.Repeat(" ", gap)+right, f.width)
}

// editorRegion draws the input box: its own bordered region at the bottom of
// the screen, with the prompt on the first content row and the cursor after
// the text. It is a region rather than a row of the content, so it does not
// scroll away with the conversation and it reads as a place to type.
func (f frame) editorRegion(rows int) []string {
	if rows < frameEditorMinRows {
		rows = frameEditorMinRows
	}
	// The editor is the same kind of object as a conversation turn: a closed
	// box whose top edge names it. The label is the ACTION the input will
	// perform (`steer`, `new thought`, `priority`), so the box says what
	// submitting will do rather than showing a bare prompt glyph.
	bodyRows := rows - 2 // the box's own top and bottom edges
	if bodyRows < 1 {
		bodyRows = 1
	}
	body := make([]string, 0, bodyRows)
	if f.input != "" || f.inputFocused {
		body = append(body, f.input)
	}
	for len(body) < bodyRows {
		body = append(body, "")
	}
	if len(body) > bodyRows {
		body = body[:bodyRows]
	}
	return box(f.inputPrompt, f.theme.Marker, f.theme.border, body, f.width)
}

// statusRegion is the status bar: the transient message on the left and the
// generated help on the right, so the bar always says both what just happened
// and what you can do.
func (f frame) statusRegion() string {
	left := ""
	if f.status != "" {
		left = " " + sanitizeLine(f.status)
	}
	right := ""
	if f.help != "" {
		right = f.help
	}
	// The help is dropped before the status: a transient message is about what
	// just happened, the help is always recoverable with `?`.
	room := f.width - displayWidth(left) - displayWidth(right)
	if room < 1 && right != "" {
		right = ""
		room = f.width - displayWidth(left)
	}
	if room < 0 {
		room = 0
	}
	return fit(left+strings.Repeat(" ", room)+right, f.width)
}

// ── labelled boxes ──────────────────────────────────────────────────────

// box renders a CLOSED box of the given width, with a label set into its top
// edge and the given body lines inside:
//
//	╭─ steer ──────────────────╮
//	│ content                  │
//	╰──────────────────────────╯
//
// It is the shared vocabulary for a conversation turn and for the editor: a
// turn and the thing you type into are the same kind of object, so they are
// drawn by the same function. lipgloss has no titled-border primitive, so the
// label is composited onto the top edge from styled blocks — the same
// technique the panel title used — rather than from a private set of glyphs.
//
// labelStyle styles the label; borderStyle styles the frame runes. Either may
// be nil. body lines are fitted to the box's inner width, so a long line is
// clipped rather than wrapping the box open (Style.Width wraps; see fit).
func box(label string, labelStyle func(string) string, borderStyle func(string) string,
	body []string, width int) []string {

	if width < 4 {
		// Too narrow for a box: fall back to the plain content so a tiny
		// terminal degrades rather than drawing a one-column frame.
		out := make([]string, 0, len(body))
		for _, l := range body {
			out = append(out, fit(l, maxInt(0, width)))
		}
		return out
	}
	if labelStyle == nil {
		labelStyle = func(s string) string { return s }
	}
	if borderStyle == nil {
		borderStyle = func(s string) string { return s }
	}

	// A box with NO BODY is just a label: it renders as a one-line band
	// (`├─ turn_end ──────`) rather than a two-row empty frame. A lifecycle
	// marker carries no content, so drawing it a top edge, nothing, and a
	// bottom edge spends two rows saying what one says. The band is the same
	// vocabulary — a labelled rule — so the two forms read as one family.
	if len(body) == 0 || allBlank(body) {
		return []string{band(label, labelStyle, borderStyle, width)}
	}

	// The body sits between the rails with a gutter each side, so text is not
	// pressed against the frame — the same gutter the panel used.
	inner := boxInner(width)
	pad := strings.Repeat(" ", boxGutter)
	out := make([]string, 0, len(body)+2)
	out = append(out, boxEdge(label, labelStyle, borderStyle, width))
	for _, l := range body {
		out = append(out, borderStyle("│")+pad+fit(l, inner)+pad+borderStyle("│"))
	}
	out = append(out, borderStyle("╰"+strings.Repeat("─", width-2)+"╯"))
	return out
}

// boxInner is the width a box's body content may occupy: the box width less
// the two rails and a one-column gutter each side. It is a function so a
// caller laying content out for box() cannot disagree with box() about how
// much room there is.
func boxInner(width int) int {
	inner := width - 2 - 2*boxGutter
	if inner < 1 {
		return 1
	}
	return inner
}

// boxGutter is the space between a box's rail and its content.
const boxGutter = 1

// allBlank reports whether every line is empty, so a body of blank lines is
// treated as no body at all.
func allBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// band renders a labelled rule with no box: `├─ label ─────────`. It is the
// bodyless form of box, used for a turn that carries only a name (a lifecycle
// marker such as `turn_end`).
func band(label string, labelStyle, borderStyle func(string) string, width int) string {
	if label == "" {
		return borderStyle(strings.Repeat("─", maxInt(0, width)))
	}
	head := borderStyle("├─ ") + labelStyle(label) + borderStyle(" ─")
	fill := width - displayWidth(head)
	if fill < 0 {
		return fit(head, width)
	}
	return head + borderStyle(strings.Repeat("─", fill))
}

// boxEdge draws a box's top edge with its label set into it:
// `╭─ label ─────────╮`. The label is dropped when there is no room for it,
// because a box with a truncated label reads worse than a plain edge.
func boxEdge(label string, labelStyle, borderStyle func(string) string, width int) string {
	corner := borderStyle("╭")
	closeCorner := borderStyle("╮")
	if label == "" {
		return corner + borderStyle(strings.Repeat("─", width-2)) + closeCorner
	}
	// `─ label ─` between the corners, with the rule filling the rest.
	head := borderStyle("─ ") + labelStyle(label) + borderStyle(" ─")
	fill := width - 2 - displayWidth(head)
	if fill < 0 {
		return corner + borderStyle(strings.Repeat("─", width-2)) + closeCorner
	}
	return corner + head + borderStyle(strings.Repeat("─", fill)) + closeCorner
}

// splitColumns divides available width into a left pane of leftFrac and a
// right pane, with a one-column gap, for the wide form factor's 66/33 home
// split (ADR 0038 decision 3). It returns the two pane widths; a caller that
// finds either too small should fall back to a single pane rather than render
// a squeezed one.
func splitColumns(available int, leftFrac float64) (left, right int) {
	const gap = 1
	if available <= gap+2 {
		return available, 0
	}
	inner := available - gap
	left = int(float64(inner) * leftFrac)
	if left < 1 {
		left = 1
	}
	right = inner - left
	return left, right
}

// joinColumns lays out two panes side by side, each padded to its column
// width, for at most height rows. It is the horizontal composition the home
// screen uses; a pane with no rows contributes blank lines so the columns
// stay aligned.
func (f frame) joinColumns(left []string, leftW int, right []string, rightW int, rows int) []string {
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		line := fit(l, leftW)
		if rightW > 0 {
			line += " " + fit(r, rightW)
		}
		out = append(out, line)
	}
	return out
}
