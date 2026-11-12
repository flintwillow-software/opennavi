package release

import (
	"strings"
	"testing"
)

// fixture is a small changelog exercising the shapes the real one has: a
// heading, entries with bold lead-ins, continuation lines, semver markers, and
// a previous release section below.
const fixture = `# Changelog

## [Unreleased]

### Added
- **Thing one** (NAVI-1). A description that continues
  onto a second line. — ` + "`Semver: minor`" + `
- **Thing two** (NAVI-2). Another. — ` + "`Semver: patch`" + `

### Fixed
- **A bug** (NAVI-3). Fixed it. — ` + "`Semver: patch`" + `

## [1.0.0] - 2020-01-01

### Added
- Initial release

[Unreleased]:
[1.0.0]:
`

func TestUnreleasedParsesEntries(t *testing.T) {
	entries, err := Unreleased(fixture)
	if err != nil {
		t.Fatalf("Unreleased: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if got, want := entries[0].Heading, "Added"; got != want {
		t.Errorf("entry 0 heading = %q, want %q", got, want)
	}
	if got, want := entries[2].Heading, "Fixed"; got != want {
		t.Errorf("entry 2 heading = %q, want %q", got, want)
	}
	// Continuation lines are joined onto their entry.
	if !strings.Contains(entries[0].Text, "continues onto a second line") {
		t.Errorf("continuation not joined: %q", entries[0].Text)
	}
	// The semver marker is stripped from the text (it is a hint, not prose)...
	if strings.Contains(entries[0].Text, "Semver:") {
		t.Errorf("semver marker not stripped: %q", entries[0].Text)
	}
	// ...but it is still visible to the version computation, which reads it
	// from the same Text. Verify via NextVersion below.
}

func TestNextVersionHighestHintWins(t *testing.T) {
	entries, err := Unreleased(fixture)
	if err != nil {
		t.Fatal(err)
	}
	version, bump, err := NextVersion("v0.5.1", entries)
	if err != nil {
		t.Fatalf("NextVersion: %v", err)
	}
	if bump != "minor" {
		t.Errorf("bump = %q, want minor (highest hint wins)", bump)
	}
	if version != "0.6.0" {
		t.Errorf("version = %q, want 0.6.0", version)
	}
}

func TestNextVersionBumpKinds(t *testing.T) {
	cases := []struct {
		name     string
		lastTag  string
		hints    []string
		want     string
		wantBump string
	}{
		{"patch only", "v1.2.3", []string{"patch"}, "1.2.4", "patch"},
		{"minor wins over patch", "v1.2.3", []string{"patch", "minor"}, "1.3.0", "minor"},
		{"major wins over all", "v1.2.3", []string{"patch", "minor", "major"}, "2.0.0", "major"},
		{"from zero", "v0.1.0", []string{"patch"}, "0.1.1", "patch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			for _, h := range tc.hints {
				b.WriteString("- **X**. y. — `Semver: " + h + "`\n")
			}
			entries, err := parseSection(b.String())
			if err != nil {
				t.Fatal(err)
			}
			got, bump, err := NextVersion(tc.lastTag, entries)
			if err != nil {
				t.Fatalf("NextVersion: %v", err)
			}
			if got != tc.want || bump != tc.wantBump {
				t.Errorf("= (%q, %q), want (%q, %q)", got, bump, tc.want, tc.wantBump)
			}
		})
	}
}

func TestNextVersionNoHints(t *testing.T) {
	entries, _ := parseSection("- **X**. y.\n")
	if _, _, err := NextVersion("v1.0.0", entries); err == nil {
		t.Fatal("expected an error with no semver hints")
	}
}

func TestTitleUsesBoldLeadIn(t *testing.T) {
	cases := map[string]string{
		"**Thing one** (NAVI-1). A description.": "Thing one",
		"**A trailing dot.** rest":               "A trailing dot",
		"No bold here. Second sentence.":         "No bold here",
	}
	for text, want := range cases {
		if got := (Entry{Text: text}).Title(); got != want {
			t.Errorf("Title(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestRefs(t *testing.T) {
	e := Entry{Text: "**X** (NAVI-10, NAVI-2). Also NAVI-10 again."}
	got := e.Refs()
	want := []int{10, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refs = %v, want %v", got, want)
		}
	}
}

func TestDraftShape(t *testing.T) {
	entries, err := Unreleased(fixture)
	if err != nil {
		t.Fatal(err)
	}
	d := Draft("0.6.0", "v0.5.1", entries, 8, 30)

	for _, want := range []string{
		"v0.6.0 — <one-line summary of this release>",
		"3 changes since v0.5.1.",
		"Added:",
		"Fixed:",
		"  - Thing one",
		"Refs NAVI-1, NAVI-2, NAVI-3",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("draft missing %q:\n%s", want, d)
		}
	}
}

func TestDraftSingularAndBounding(t *testing.T) {
	one, _ := parseSection("- **Solo**. x. — `Semver: patch`\n")
	if d := Draft("0.0.1", "v0.0.0", one, 8, 30); !strings.Contains(d, "1 change since") {
		t.Errorf("singular not used:\n%s", d)
	}

	// More entries than the per-heading bound: the remainder is reported, not
	// silently dropped.
	var b strings.Builder
	for i := 0; i < 12; i++ {
		b.WriteString("- **E" + string(rune('A'+i)) + "**. x. — `Semver: patch`\n")
	}
	many, _ := parseSection(b.String())
	d := Draft("0.0.1", "v0.0.0", many, 8, 30)
	if !strings.Contains(d, "… and 4 more") {
		t.Errorf("bound not reported:\n%s", d)
	}
}

func TestCutMovesEntriesAndEmptiesUnreleased(t *testing.T) {
	out, err := Cut(fixture, "0.6.0", "2026-11-12")
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}

	// Unreleased is present but empty.
	entries, err := Unreleased(out)
	if err != nil {
		t.Fatalf("Unreleased after cut: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Unreleased not emptied: %d entries remain", len(entries))
	}

	for _, want := range []string{
		"## [0.6.0] - 2026-11-12",
		"## [Unreleased]",
		"## [1.0.0] - 2020-01-01",
		"Thing one",
		"A bug",
		"[Unreleased]:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cut output missing %q", want)
		}
	}

	// The new section sits BELOW Unreleased and ABOVE the previous release.
	iUnrel := strings.Index(out, "## [Unreleased]")
	iNew := strings.Index(out, "## [0.6.0]")
	iOld := strings.Index(out, "## [1.0.0]")
	if !(iUnrel < iNew && iNew < iOld) {
		t.Errorf("section order wrong: Unreleased=%d, new=%d, old=%d", iUnrel, iNew, iOld)
	}
}

func TestCutRefusesBadInput(t *testing.T) {
	if _, err := Cut(fixture, "1.0.0", "2026-11-12"); err == nil {
		t.Error("expected refusal: version already exists")
	}
	if _, err := Cut("# Changelog\n\n## [1.0.0]\n", "0.1.0", "2026-11-12"); err == nil {
		t.Error("expected refusal: no Unreleased section")
	}
	empty := "# Changelog\n\n## [Unreleased]\n\n## [1.0.0]\n"
	if _, err := Cut(empty, "0.1.0", "2026-11-12"); err == nil {
		t.Error("expected refusal: empty Unreleased")
	}
}

func TestCutIsIdempotentInEffect(t *testing.T) {
	// Cutting, then cutting again with a different version, must refuse: there
	// is nothing left in Unreleased.
	out, err := Cut(fixture, "0.6.0", "2026-11-12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Cut(out, "0.7.0", "2026-11-13"); err == nil {
		t.Error("expected refusal on a second cut of an emptied Unreleased")
	}
}

// parseSection is a test helper: it wraps a section body so parseEntries can be
// exercised directly.
func parseSection(body string) ([]Entry, error) {
	return Unreleased("## [Unreleased]\n\n" + body + "\n## [x]\n")
}
