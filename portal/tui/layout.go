package tui

import "strings"

// The frame is the layout primitive every screen renders into (NAVI-588): a
// header line, a body of a computed height, and a footer. Screens stop
// guessing their own height and stop emitting a fixed 60-column rule; the
// frame knows the terminal size and hands the body exactly the rows it has.
//
// The frame's contract is that its output is exactly `height` lines, each
// exactly `width` display columns — no more, no less. That is what makes the
// TUI fill the terminal rather than leaving it half-drawn, and it is what a
// golden render can assert at a fixed size.

// chrome is the number of rows the frame spends on non-body content: the
// header, the footer, and the two separator rules between them.
// The panel chrome: a top border with the title, an optional header line
// inside it, a bottom border, and the status/help footer beneath the panel.
// The header row is optional because it only ever carries a warning; a
// healthy TUI spends that row on content.
const (
	frameHeaderRows = 1 // the optional header line inside the panel
	frameBorderRows = 2 // the top and bottom panel edges
	frameFooterRows = 1 // the status/help line below the panel
	frameInputRows  = 1 // the optional input line below the panel
	// frameBaseChrome includes the vertical padding rows, which are part of
	// the panel's chrome rather than its content.
	frameBaseChrome = frameBorderRows + frameFooterRows + 2*panelPadY
	frameChromeRows = frameBaseChrome + frameHeaderRows
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
	// title labels the panel's top edge (the augmented-ui "labelled module"
	// idiom). Empty draws a plain edge.
	title string
	// hasHeader says whether the optional header row inside the panel is
	// drawn. It carries only a warning, so it is usually absent.
	hasHeader bool
	// status is a short state suffix shown at the right of the title's rule,
	// so transient state (a paused scrollback, a trimmed window) does not cost
	// a body row the way a header line would.
	status string
	// statusStyle colours the status suffix. It is chosen by the screen (a
	// conversation colours it by the thought's lifecycle), so the panel can
	// show at a glance whether what you are reading is running, paused, or
	// finished without spending a body row on it. Zero value renders plain.
	statusStyle func(string) string
	// input is the persistent input line drawn below the panel, with the
	// cursor at the end. Empty means no input row, which is the case on
	// screens that take no typing.
	input string
	// inputPrompt labels the input line (`steer ›`), so the reader knows what
	// submitting will do.
	inputPrompt string
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

// chromeRows is the chrome this frame actually spends: the borders and footer
// always, the header row only when there is something to say in it.
func (f frame) chromeRows() int {
	n := frameBaseChrome
	if f.hasHeader {
		n += frameHeaderRows
	}
	if f.inputPrompt != "" {
		n += frameInputRows
	}
	return n
}

// bodyHeight is the rows left for the body once the chrome is subtracted.
func (f frame) bodyHeight() int {
	h := f.height - f.chromeRows()
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

// render assembles header + rule + body + rule + footer, each line fitted to
// the frame width and the whole thing fitted to the frame height. body is
// clipped to bodyHeight rows and every one of its lines is truncated to the
// width, so a screen cannot overflow the frame even if it tries.
func (f frame) render(header string, body []string, footer string) string {
	// The screen is ONE panel with a clipped-corner frame: a titled top edge,
	// side rails, and a bottom edge. This is the augmented-ui treatment — a
	// notched module rather than a plain box — applied to opencode's
	// structure, where the chrome exists to divide the view rather than to
	// decorate it.
	b := borderClipped()
	rail := f.theme.Subtle

	// The title is left-aligned on the top edge and the status is
	// right-aligned, with the rule filling between them. The status is
	// dropped before the title is, because the title says where you are and
	// the status only says how the view is behaving.
	title := f.theme.panelTitle(f.title)
	status := ""
	if f.status != "" {
		// The suffix's colour is the screen's choice (see statusStyle), so a
		// lifecycle state reads the same here as it does in a table cell.
		st := f.statusStyle
		if st == nil {
			st = f.theme.Subtle
		}
		status = st("┤ " + f.status + " ├")
	}
	fill := f.width - 2 - 2*panelPad - displayWidth(title) - displayWidth(status)
	if fill < 0 {
		// Not enough room for both: drop the status, then let the title clip.
		status = ""
		fill = f.width - 2 - 2*panelPad - displayWidth(title)
	}
	// The title sits inside the corner with a gutter, matching the body, so
	// the label lines up with the content beneath it rather than with the
	// frame's own edge.
	top := b.TopLeft + strings.Repeat(b.Top, panelPad) + title +
		strings.Repeat(b.Top, maxInt(0, fill)) + status +
		strings.Repeat(b.Top, panelPad) + b.TopRight

	// Every interior line is railed on BOTH sides, with a gutter between the
	// rail and the content. The gutter is what stops the panel from reading as
	// a cramped box: content pressed against the rail has no margin, and the
	// eye has nothing to separate the frame from what it frames.
	//
	// The gutter is symmetric — the same on the left and the right — so a
	// centred or right-aligned cell lines up with the same margin as a
	// left-aligned one.
	const gutter = panelPad
	inner := maxInt(1, f.width-2*gutter-2)
	line := func(content string) string {
		return pad(rail(b.Left)+strings.Repeat(" ", gutter)+
			pad(content, inner)+strings.Repeat(" ", gutter)+rail(b.Right), f.width)
	}

	lines := make([]string, 0, f.height)
	lines = append(lines, pad(top, f.width))
	// Blank rows inside the panel, top and bottom, so the content is not
	// pressed against the frame's own edges. Without them the first body line
	// reads as part of the border and the last as part of the footer.
	for i := 0; i < panelPadY; i++ {
		lines = append(lines, line(""))
	}
	if f.hasHeader {
		lines = append(lines, line(header))
	}

	bh := f.bodyHeight()
	for i := 0; i < bh; i++ {
		var content string
		if i < len(body) {
			content = body[i]
		}
		lines = append(lines, line(content))
	}

	// The input line sits inside the panel above the bottom edge, where a
	// chat's composer belongs: it reads as part of the conversation rather
	// than as a separate bar, and the panel's own frame contains it.
	if f.inputPrompt != "" {
		lines = append(lines, line(""))
		lines = append(lines, line(f.inputPrompt+" "+f.input))
	}

	for i := 0; i < panelPadY; i++ {
		lines = append(lines, line(""))
	}
	bottom := b.BottomLeft + strings.Repeat(b.Bottom, maxInt(0, f.width-2)) + b.BottomRight
	lines = append(lines, pad(bottom, f.width))
	lines = append(lines, pad(footer, f.width))

	// The frame promises exactly `height` lines; clip if a caller's chrome
	// arithmetic ever disagrees, and pad if the terminal is taller than the
	// chrome plus the floor.
	if len(lines) > f.height {
		lines = lines[:f.height]
	}
	for len(lines) < f.height {
		lines = append(lines, strings.Repeat(" ", f.width))
	}
	return strings.Join(lines, "\n")
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
		line := pad(l, leftW)
		if rightW > 0 {
			line += " " + pad(r, rightW)
		}
		out = append(out, line)
	}
	return out
}
