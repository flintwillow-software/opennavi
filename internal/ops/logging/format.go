package logging

import "strings"

// Format selects the line rendering for a Logger (NAVI-328
// log-format knob): FormatJSON is the structured default (unchanged
// behavior for existing callers); FormatHuman is the console-friendly
// rendering (`time LEVEL msg key=value...`).
type Format int

const (
	// FormatJSON renders one JSON object per line (the pre-knob
	// behavior; the default for --log-file/pipes).
	FormatJSON Format = iota
	// FormatHuman renders the console-friendly aligned line.
	FormatHuman
)

// String returns the format's knob spelling.
func (f Format) String() string {
	switch f {
	case FormatHuman:
		return "human"
	default:
		return "json"
	}
}

// Valid reports whether f is a defined format.
func (f Format) Valid() bool {
	return f == FormatJSON || f == FormatHuman
}

// ParseFormat converts a knob string ("json" | "human") to a Format.
// Unknown → FormatJSON (structured default; safe fallback for logs).
func ParseFormat(s string) Format {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "human":
		return FormatHuman
	default:
		return FormatJSON
	}
}
