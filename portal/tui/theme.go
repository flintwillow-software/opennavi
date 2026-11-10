package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// The theme is the single place the TUI chooses colours and text styles
// (ADR 0038 decision 1, NAVI-588). No other file in this package emits an
// escape code or names a colour, so the whole look is changed here, and a
// render test can force it off in one place.
//
// Colours are AdaptiveColor: a light/dark pair, so the TUI is legible on
// either terminal background without a setting. lipgloss resolves the pair
// against the detected background, and degrades to no colour when the
// terminal has no profile — so a monochrome terminal gets plain text rather
// than broken escapes.
//
// Colour is decoration, never information. Every styled element here is also
// distinguishable without it (a marker glyph, a label, a position), because
// NO_COLOR is honoured and a user who sets it must lose nothing but the
// colour (ADR 0038 decision 6's spirit: never advertise what does not work).

// theme bundles the styles the renderer uses. It is a value, so a test can
// construct an uncoloured one and compare output without escapes.
type theme struct {
	// colored is false when colour is disabled (NO_COLOR, a dumb terminal,
	// or a test asking for plain output). Every style below is a no-op when
	// it is false, so plain output is the same code path, not a branch at
	// each call site.
	colored bool

	// accent is the chrome colour: panel borders, the marker, the selected
	// row. It is a lipgloss terminal colour so it can colour a border edge.
	accent lipgloss.TerminalColor

	title    lipgloss.Style
	subtle   lipgloss.Style
	marker   lipgloss.Style
	selected lipgloss.Style
	user     lipgloss.Style
	assist   lipgloss.Style
	tool     lipgloss.Style
	system   lipgloss.Style
	good     lipgloss.Style
	bad      lipgloss.Style
	warn     lipgloss.Style
	confirm  lipgloss.Style
}

// newTheme builds the theme for the current environment. Colour is disabled
// when NO_COLOR is set (https://no-color.org — any value, including empty),
// when TERM is "dumb", or when the terminal reports no colour profile.
func newTheme() theme { return newThemeFor(shouldColor()) }

// shouldColor reports whether colour should be used, per NO_COLOR, TERM, and
// the detected profile.
func shouldColor() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	// A profile of Ascii means the terminal told us it cannot show colour.
	return lipgloss.ColorProfile() != termenv.Ascii
}

// newThemeFor builds a theme with colour on or off. Tests use the false case
// so golden files are escape-free and diffable.
func newThemeFor(colored bool) theme {
	if !colored {
		// A zero theme: every style renders its input unchanged.
		return theme{}
	}
	dark := lipgloss.Color("#D0D0D0")
	darkSubtle := lipgloss.Color("#6C6C6C")
	accent := lipgloss.Color("#7D56F4")
	green := lipgloss.Color("#04B575")
	red := lipgloss.Color("#EE6D66")
	amber := lipgloss.Color("#EEBD5E")

	adapt := func(light, dark lipgloss.Color) lipgloss.AdaptiveColor {
		return lipgloss.AdaptiveColor{Light: string(light), Dark: string(dark)}
	}

	return theme{
		colored:  true,
		accent:   adapt(lipgloss.Color("#5A3FC0"), accent),
		title:    lipgloss.NewStyle().Bold(true).Foreground(adapt(lipgloss.Color("#5A3FC0"), accent)),
		subtle:   lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#8A8A8A"), darkSubtle)),
		marker:   lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#5A3FC0"), accent)).Bold(true),
		selected: lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#5A3FC0"), accent)).Bold(true),
		user:     lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#0A6EBD"), lipgloss.Color("#6CB6FF"))),
		assist:   lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#333333"), dark)),
		tool:     lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#8A6D00"), amber)),
		system:   lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#8A8A8A"), darkSubtle)),
		good:     lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#0A7D4F"), green)),
		bad:      lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#C0392B"), red)),
		warn:     lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#8A6D00"), amber)),
		confirm:  lipgloss.NewStyle().Foreground(adapt(lipgloss.Color("#C0392B"), red)).Bold(true),
	}
}

// apply renders s through a style, or returns it unchanged when the theme is
// uncoloured. A single helper keeps the "colour off means identical text"
// invariant in one place.
func (t theme) apply(s lipgloss.Style, text string) string {
	if !t.colored {
		return text
	}
	return s.Render(text)
}

// The named accessors below are what the renderer calls. They read better at
// the call site than t.apply(t.subtle, …) and keep the style choice here.

func (t theme) Title(s string) string     { return t.apply(t.title, s) }
func (t theme) Subtle(s string) string    { return t.apply(t.subtle, s) }
func (t theme) Marker(s string) string    { return t.apply(t.marker, s) }
func (t theme) Selected(s string) string  { return t.apply(t.selected, s) }
func (t theme) User(s string) string      { return t.apply(t.user, s) }
func (t theme) Assistant(s string) string { return t.apply(t.assist, s) }
func (t theme) Tool(s string) string      { return t.apply(t.tool, s) }
func (t theme) System(s string) string    { return t.apply(t.system, s) }
func (t theme) Good(s string) string      { return t.apply(t.good, s) }
func (t theme) Bad(s string) string       { return t.apply(t.bad, s) }
func (t theme) Warn(s string) string      { return t.apply(t.warn, s) }
func (t theme) Confirm(s string) string   { return t.apply(t.confirm, s) }

// ── panel chrome ───────────────────────────────────────────────────────

// The chrome is lipgloss's own: `RoundedBorder()` drawn by a `Style`, sized
// with `Width`/`Height`, and composed with `JoinHorizontal` where a label sits
// in the top edge. Nothing here draws a border rune by hand.
//
// This replaced a hand-rolled clipped-corner frame (an augmented-ui pastiche)
// so the TUI uses the library's presets and patterns rather than a private
// vocabulary of glyphs — see the ADR 0038 amendment. The border is one line to
// change if a different preset reads better.

// panelBorder is the border every panel draws. It is one function so the whole
// app's chrome changes in one place.
func panelBorder() lipgloss.Border { return lipgloss.RoundedBorder() }

// panelStyle is the style a panel's box is drawn with. Width and Height are
// the CONTENT box (lipgloss adds the border outside them, and padding inside),
// so a caller passing a terminal size subtracts the border — see frame.render,
// which is the only place that arithmetic lives.
func (t theme) panelStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Border(panelBorder())
	if t.colored {
		s = s.BorderForeground(t.accent)
	}
	return s
}

// border styles a run of border runes in the chrome colour, so a hand-built
// edge (a titled top line, which lipgloss has no primitive for) matches the
// border lipgloss draws around the rest of the panel.
func (t theme) border(s string) string {
	if !t.colored {
		return s
	}
	return lipgloss.NewStyle().Foreground(t.accent).Render(s)
}

// panelTitle is a panel's label, styled. It is a plain string with colour, not
// a rune-composed band: the caller lays it out with Width/Align, so the
// placement is lipgloss's arithmetic rather than this function's.
func (t theme) panelTitle(label string) string {
	if label == "" {
		return ""
	}
	if !t.colored {
		return label
	}
	return t.title.Render(label)
}

// panelStatus is a panel's state suffix, styled by the caller's chosen style
// (a lifecycle colour) so a status reads the same here as in a table cell.
func (t theme) panelStatus(s string, style func(string) string) string {
	if s == "" {
		return ""
	}
	if style == nil {
		style = t.Subtle
	}
	return style(s)
}

// statusStyle picks the style for a lifecycle status. It is the ONE place the
// status-to-colour mapping lives, so a status means the same colour in a table
// cell, in the panel title, and anywhere else it is shown — two mappings would
// drift and the same state would read as two different things.
//
// The vocabulary covers both the queue's event kinds and the thought statuses
// they map to (see displayStatus), so a caller can pass either.
func (t theme) statusStyle(status string) lipgloss.Style {
	switch status {
	case "running", "started", "active", "live":
		return t.good
	case "failed", "error", "cancelled":
		return t.bad
	case "paused", "preempted", "blocked":
		return t.warn
	}
	// complete/completed/done and anything unknown: quiet. A finished thought
	// is not an alarm, and an unrecognised status should not shout.
	return t.subtle
}

// Status colours an enum cell (a thought's state, a memory scope) so the eye
// finds a row's state without reading every word. It takes the already-aligned
// cell and the raw value, so the colour is chosen from the value while the
// layout stays exactly what the grid computed — colouring must not change the
// text's width or position.
func (t theme) Status(value, aligned string, width int, a align) string {
	if !t.colored {
		return aligned
	}
	// Render only the trimmed value, then lay it out with lipgloss's own
	// Align/Width/MaxWidth, so the escape codes never count toward the column
	// width and the cell lands exactly where the grid put it.
	trimmed := strings.TrimSpace(aligned)
	if width <= 0 {
		return t.statusStyle(value).Render(trimmed)
	}
	// Truncate before aligning: Width wraps overflow, and a wrapped cell would
	// break the row (see fit).
	trimmed = truncate(trimmed, width, "…")
	return t.statusStyle(value).
		Width(width).
		Align(a.position()).
		Render(trimmed)
}

// ── rune-correct text helpers ───────────────────────────────────────────

// displayWidth is the terminal cell width of s, in columns rather than bytes,
// so a CJK or emoji cell is measured by what it occupies (NAVI-588's table
// bug: byte padding shifted every column after a wide rune). lipgloss.Width is
// the library's own measurement and is what every layout call uses.
func displayWidth(s string) int { return lipgloss.Width(s) }

// fit lays s out to EXACTLY width display columns, truncating with an ellipsis
// when it is too long and padding with spaces when it is short.
//
// It replaces fmt.Sprintf("%-*s", …), which padded by byte length and so
// misaligned every column after a non-ASCII value (NAVI-588).
//
// The truncation is explicit rather than left to lipgloss's MaxWidth, because
// Width WRAPS content that overflows it — which is right for a paragraph and
// wrong for a line that must occupy exactly one row (a footer, a table cell, a
// border edge). Truncating first and then padding with Width gives one row of
// exactly width columns, which is the guarantee every caller relies on.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncate(s, width, "…")
	return lipgloss.NewStyle().Width(width).Render(s)
}

// truncate cuts s to at most width display columns, appending tail when it had
// to cut. lipgloss has no tail-marker truncation primitive, so this is the
// library's own ansi.Truncate (which is what lipgloss calls internally); it is
// rune- and width-aware and never splits a multi-byte rune or a wide glyph.
func truncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, tail)
}

// truncateLeft cuts from the LEFT, keeping the tail, and marks the cut at the
// front. It is the identity-column rule (NAVI-600): a generated id's
// distinguishing characters are at the end, so the tail is what the reader
// needs. lipgloss has no left-truncate primitive, so this composes
// ansi.TruncateLeft with the same width measurement everything else uses.
func truncateLeft(s string, width int, marker string) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}
	mw := displayWidth(marker)
	if width <= mw {
		return truncate(s, width, "")
	}
	return marker + ansi.TruncateLeft(s, displayWidth(s)-(width-mw), "")
}
