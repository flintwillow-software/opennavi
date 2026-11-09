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
const (
	frameHeaderRows = 1
	frameRuleRows   = 2
	frameFooterRows = 1
	frameChromeRows = frameHeaderRows + frameRuleRows + frameFooterRows
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

// bodyHeight is the rows left for the body once the chrome is subtracted.
func (f frame) bodyHeight() int {
	h := f.height - frameChromeRows
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
func (f frame) tooShort() bool { return f.height < frameChromeRows+minBodyRows }

// render assembles header + rule + body + rule + footer, each line fitted to
// the frame width and the whole thing fitted to the frame height. body is
// clipped to bodyHeight rows and every one of its lines is truncated to the
// width, so a screen cannot overflow the frame even if it tries.
func (f frame) render(header string, body []string, footer string) string {
	rule := f.theme.Subtle(strings.Repeat("─", f.width))

	lines := make([]string, 0, f.height)
	lines = append(lines, pad(header, f.width))
	lines = append(lines, rule)

	bh := f.bodyHeight()
	for i := 0; i < bh; i++ {
		if i < len(body) {
			lines = append(lines, pad(body[i], f.width))
		} else {
			lines = append(lines, strings.Repeat(" ", f.width))
		}
	}

	lines = append(lines, rule)
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
