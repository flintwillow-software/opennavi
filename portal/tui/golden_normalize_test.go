package tui

import "testing"

// TestNormalizeGoldenCoversDateRollover pins that the golden normalisation
// catches BOTH the full rendered timestamp and the column-truncated form, so a
// golden cannot re-acquire a wall-clock dependency at the next date boundary.
func TestNormalizeGoldenCoversDateRollover(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-11-20 03:04:05Z", "YYYY-MM-DD 03:04:05Z"},
		{"2026-11-2…", "YYYY-MM-DD…"},
		{"2026-01-05 23:59:59Z", "YYYY-MM-DD 23:59:59Z"},
		{"2027-03-9…", "YYYY-MM-DD…"},
		{"no date here", "no date here"},
	}
	for _, c := range cases {
		if got := normalizeGolden(c.in); got != c.want {
			t.Errorf("normalizeGolden(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
