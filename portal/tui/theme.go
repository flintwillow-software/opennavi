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

// The panel vocabulary is opencode's spatial structure with augmented-ui's
// visual language: the screen is divided into named panels that are visibly
// delineated, and the delineation is a clipped-corner frame rather than a
// plain rectangle.
//
// augmented-ui's signature is a CLIPPED corner — one or more corners cut away
// with the edge running at an angle. In terminal runes the translation is a
// diagonal glyph against straight edges, which is what borderClipped is. The
// heavy top edge (borderBand) is the second augmented-ui idiom: a header band
// that reads as a labelled module rather than a box.

// borderClipped cuts the top-left and bottom-right corners, so the frame reads
// as a notched module. Opposite-diagonal cuts are the augmented-ui signature.
func borderClipped() lipgloss.Border {
	return lipgloss.Border{
		Top: "─", Bottom: "─", Left: "│", Right: "│",
		TopLeft: "╱", TopRight: "┐", BottomLeft: "└", BottomRight: "╱",
	}
}

// borderBand is a heavy top edge with a light body: a header band.
func borderBand() lipgloss.Border {
	return lipgloss.Border{
		Top: "━", Bottom: "─", Left: "│", Right: "│",
		TopLeft: "┏", TopRight: "┓", BottomLeft: "└", BottomRight: "┘",
	}
}

// borderRail is opencode's rail: a single heavy vertical on the left, with a
// notch at each end, and no other edges.
func borderRail() lipgloss.Border {
	return lipgloss.Border{Left: "┃", TopLeft: "╻", BottomLeft: "╹"}
}

// panelStyle is the border style panels use. It is one function so the whole
// app's chrome changes in one place.
func (t theme) panelStyle() lipgloss.Style {
	if !t.colored {
		// An uncoloured theme still draws the structure — the border is
		// layout, not decoration — but without the accent.
		return lipgloss.NewStyle().Border(borderClipped())
	}
	return lipgloss.NewStyle().
		Border(borderClipped()).
		BorderForeground(t.accent)
}

// panelTitle renders a title for a panel's top edge: a short label in the
// accent colour, prefixed by a rule stub, so a panel announces what it holds
// the way augmented-ui's labelled modules do.
func (t theme) panelTitle(label string) string {
	if label == "" {
		return ""
	}
	stub := "─┤ "
	close_ := " ├"
	if !t.colored {
		return stub + label + close_
	}
	return t.subtle.Render(stub) + t.title.Render(label) + t.subtle.Render(close_)
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
	var style lipgloss.Style
	switch value {
	case "running", "started", "active":
		style = t.good
	case "failed", "error", "cancelled":
		style = t.bad
	case "paused", "preempted", "blocked":
		style = t.warn
	case "complete", "completed", "done":
		style = t.subtle
	default:
		style = t.subtle
	}
	// Render only the trimmed value, then re-pad, so the escape codes never
	// count toward the column width.
	trimmed := strings.TrimSpace(aligned)
	rendered := style.Render(trimmed)
	space := width - displayWidth(trimmed)
	if space <= 0 {
		return rendered
	}
	switch a {
	case alignRight:
		return strings.Repeat(" ", space) + rendered
	case alignCenter:
		left := space / 2
		return strings.Repeat(" ", left) + rendered + strings.Repeat(" ", space-left)
	}
	return rendered + strings.Repeat(" ", space)
}

// ── rune-correct text helpers ───────────────────────────────────────────

// displayWidth is the terminal cell width of s, in columns rather than bytes,
// so a CJK or emoji cell is measured by what it occupies (NAVI-588's table
// bug: byte padding shifted every column after a wide rune).
func displayWidth(s string) int { return ansi.StringWidth(s) }

// truncate cuts s to at most width display columns, appending tail when it
// had to cut. It is rune- and width-aware, so it never splits a multi-byte
// rune or a wide glyph in half, and the result is never wider than width.
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
// front. It is the identity-column rule: a generated id's distinguishing
// characters are at the end, so the tail is what the reader needs.
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

// pad right-pads s to exactly width display columns, truncating first when it
// is too long. This replaces fmt.Sprintf("%-*s", …), which padded by byte
// length and so misaligned every column after a non-ASCII value.
func pad(s string, width int) string {
	s = truncate(s, width, "…")
	if w := displayWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}
