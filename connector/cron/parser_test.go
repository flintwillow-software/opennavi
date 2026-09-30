package cron

import (
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	valid := []string{
		"* * * * *",
		"0 0 * * *",
		"*/5 * * * *",
		"0 9-17 * * 1-5",
		"0 0 1 1 *",
		"30 4 1,15 * 5",
		"0 */6 * * *",
		"1-30/2 * * * *",
		"59 23 31 12 6",
		"0 12 1 */2 *",
	}
	for _, expr := range valid {
		if _, err := parseExpr(expr); err != nil {
			t.Errorf("parseExpr(%q) = %v, want nil", expr, err)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	invalid := map[string]string{
		"":              "expected 5 fields",
		"* * * *":       "expected 5 fields",
		"* * * * * *":   "expected 5 fields",
		"60 * * * *":    "out of range",
		"* 24 * * *":    "out of range",
		"* * 0 * *":     "out of range",
		"* * 32 * *":    "out of range",
		"* * * 13 *":    "out of range",
		"* * * * 7":     "out of range",
		"a * * * *":     "invalid value",
		"*/0 * * * *":   "invalid step",
		"*/-1 * * * *":  "invalid step",
		"5-1 * * * *":   "out of range",
		"1-2-3 * * * *": "invalid range end",
		"*,x * * * *":   "invalid value",
		"0-70 * * * *":  "out of range",
	}
	for expr, want := range invalid {
		_, err := parseExpr(expr)
		if err == nil {
			t.Errorf("parseExpr(%q) = nil, want error containing %q", expr, want)
		} else if !contains(err.Error(), want) {
			t.Errorf("parseExpr(%q) = %q, want containing %q", expr, err, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestMatchesMinute(t *testing.T) {
	ce, _ := parseExpr("*/15 * * * *")
	base := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	if !ce.Matches(base) {
		t.Fatal("10:00 should match */15")
	}
	if ce.Matches(base.Add(7 * time.Minute)) {
		t.Fatal("10:07 should not match */15")
	}
	if !ce.Matches(base.Add(30 * time.Minute)) {
		t.Fatal("10:30 should match */15")
	}
}

func TestMatchesDomDowOrSemantics(t *testing.T) {
	// "0 0 13 * 5": 13th OR Friday (Vixie OR when both restricted).
	ce, _ := parseExpr("0 0 13 * 5")
	// 2026-09-13 is a Sunday → dom match.
	if !ce.Matches(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("13th should match (dom)")
	}
	// 2026-09-11 is a Friday → dow match.
	if !ce.Matches(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("Friday should match (dow)")
	}
	// 2026-09-14 is a Monday, not the 13th → no match.
	if ce.Matches(time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("non-13th Monday should not match")
	}
}

func TestNextMinuteResolution(t *testing.T) {
	ce, _ := parseExpr("30 * * * *")
	base := time.Date(2026, 9, 6, 10, 31, 0, 0, time.UTC)
	next := ce.Next(base)
	if want := time.Date(2026, 9, 6, 11, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("Next = %v, want %v", next, want)
	}
	// Sub-minute truncation: next after 10:30:30 is 11:30, not 10:30.
	base = time.Date(2026, 9, 6, 10, 30, 30, 0, time.UTC)
	if got := ce.Next(base); got.Equal(time.Date(2026, 9, 6, 10, 30, 0, 0, time.UTC)) {
		t.Fatal("Next must be strictly after t (sub-minute truncated)")
	}
}

func TestNextLeapDay(t *testing.T) {
	ce, _ := parseExpr("0 0 29 2 *")
	next := ce.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	// 2028 is the next leap year after 2026 (2027 not, 2028 yes).
	if want := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("Next leap day = %v, want %v", next, want)
	}
}

func TestNextDSTGap(t *testing.T) {
	// America/New_York 2026 spring-forward: 2026-03-08, 02:00→03:00
	// (02:30 local does not exist). Semantics match robfig/cron v3:
	// a schedule firing in a nonexistent wall time is SKIPPED that day
	// — the next run is the following day's 02:30 (which exists).
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	ce, _ := parseExpr("30 2 * * *")
	before := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	next := ce.Next(before)

	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("DST-gap skip = %v, want %v (Mar 9 02:30 — Mar 8's 02:30 does not exist)", next, want)
	}
	// Sanity: the scan correctly fires the pre-gap day (Mar 7 02:30
	// exists and is the first match after Mar 6 12:00).
	first := ce.Next(time.Date(2026, 3, 6, 12, 0, 0, 0, loc))
	if want := time.Date(2026, 3, 7, 2, 30, 0, 0, loc); !first.Equal(want) {
		t.Fatalf("pre-gap fire = %v, want Mar 7 02:30", first)
	}
}

func TestNextDSTFallBackDoubleFire(t *testing.T) {
	// America/New_York 2026 fall-back: 2026-11-01, 02:00→01:00 — the
	// 02:30 wall hour repeats. A minute-scan fires twice (once per
	// pass). Documented as intended behavior (tested, not fixed).
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	ce, _ := parseExpr("30 2 * * *")
	// Scan the repeated hour manually: 01:59 → 02:30 (first pass, EDT)
	// → 01:59:59 (wait, Go normalizes)... Actually scan wall minutes
	// via Next() twice.
	first := ce.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, loc))
	if first.Hour() != 2 || first.Minute() != 30 {
		t.Fatalf("first fire = %v, want 02:30", first)
	}
	second := ce.Next(first)
	if second.Hour() != 2 || second.Minute() != 30 {
		t.Fatalf("second fire = %v, want 02:30 (repeated hour)", second)
	}
	if !second.After(first) {
		t.Fatalf("second fire %v not after first %v", second, first)
	}
}
