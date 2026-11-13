// Package release implements the release-train's text surgery: computing the
// next version from the CHANGELOG's Semver hints, drafting a tag message from
// the Unreleased entries, and cutting that section into a versioned one.
//
// It is a Go package rather than a shell/awk script because the operations are
// structured-text manipulations with edge cases worth testing, and because the
// repository's CI already runs `go test ./...` — so this gets real coverage for
// free instead of needing a second toolchain in CI. The thin CLI over it lives
// in cmd/navi-release; scripts/release.sh orchestrates (git, $EDITOR, prompts)
// and calls the CLI for the parts that touch the CHANGELOG.
package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// unreleasedHeading matches the `## [Unreleased]` line.
var unreleasedHeading = regexp.MustCompile(`^## \[Unreleased\]`)

// anyH2Heading matches any `## [...]` or `## ` heading, which ends the
// Unreleased section.
var anyH2Heading = regexp.MustCompile(`^## `)

// sectionBody returns the text between the `## [Unreleased]` heading and the
// next `## ` heading (or the end of the document), plus the byte offsets of the
// whole Unreleased section (heading included) so a caller can splice it.
//
// It is a line scan rather than one regexp because Go's regexp is RE2, which
// has no lookahead — the natural `(?=^## )` boundary is not expressible.
func sectionBody(changelog string) (body string, start, end int, ok bool) {
	return namedSectionBody(changelog, "[Unreleased]")
}

// namedSectionBody returns the body of the `## <heading>` section whose
// heading contains headingMark, plus the byte offsets of the whole section
// (heading included).
func namedSectionBody(changelog, headingMark string) (body string, start, end int, ok bool) {
	lines := strings.SplitAfter(changelog, "\n")

	offset := 0
	headingEnd := -1
	for _, line := range lines {
		if headingEnd < 0 {
			if strings.HasPrefix(line, "## ") && strings.Contains(line, headingMark) {
				headingEnd = offset + len(line)
				start = offset
			}
		} else if anyH2Heading.MatchString(line) {
			return changelog[headingEnd:offset], start, offset, true
		}
		offset += len(line)
	}
	if headingEnd < 0 {
		return "", 0, 0, false
	}
	// The section runs to the end of the document.
	return changelog[headingEnd:], start, len(changelog), true
}

// semverHint matches the release hint every changelog entry ends with.
var semverHint = regexp.MustCompile("Semver: ([a-z]+)")

// naviRef matches a Huly issue reference.
var naviRef = regexp.MustCompile(`NAVI-(\d+)`)

// boldTitle matches an entry's bold lead-in, which is its human title.
var boldTitle = regexp.MustCompile(`\*\*(.+?)\*\*`)

// Entry is one changelog bullet: the section it sits under, its text, and the
// release hint it carries.
type Entry struct {
	// Heading is the `### Added` / `### Changed` / ... it sits under.
	Heading string
	// Text is the entry body with the trailing `— Semver: x` marker removed.
	Text string
	// Semver is the release hint the entry carried (`patch`, `minor`, `major`),
	// or "" when it had none. It is kept separate from Text because Text is
	// the changelog PROSE (the marker is not part of it) while the hint is
	// release METADATA that NextVersion needs.
	Semver string
}

// Unreleased returns the entries in the CHANGELOG's Unreleased section, in
// document order, with continuation lines joined onto their entry.
func Unreleased(changelog string) ([]Entry, error) {
	body, _, _, ok := sectionBody(changelog)
	if !ok {
		return nil, fmt.Errorf("no '## [Unreleased]' section found")
	}
	return parseEntries(body), nil
}

// Versioned returns the entries in the CHANGELOG's `## [<version>]` section.
//
// It is what the tagging step drafts from: after the cut, the release's own
// section is the committed record of what the tag names, so a message built
// from it cannot drift from the changelog.
func Versioned(changelog, version string) ([]Entry, error) {
	body, _, _, ok := namedSectionBody(changelog, "["+version+"]")
	if !ok {
		return nil, fmt.Errorf("no '## [%s]' section found", version)
	}
	return parseEntries(body), nil
}

// parseEntries walks a section body and returns its entries.
func parseEntries(body string) []Entry {
	var (
		entries []Entry
		heading = "Changed"
	)
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "### "):
			heading = strings.TrimSpace(strings.TrimPrefix(line, "### "))
		case strings.HasPrefix(line, "- "):
			entries = append(entries, Entry{
				Heading: heading,
				Text:    strings.TrimPrefix(line, "- "),
			})
		case strings.TrimSpace(line) == "":
			// blank: skip
		default:
			// A continuation of the previous entry.
			if n := len(entries); n > 0 {
				entries[n-1].Text += " " + strings.TrimSpace(line)
			}
		}
	}

	// The semver marker can land on a continuation line (an entry whose prose
	// wraps before it), so the marker is split out AFTER continuations are
	// joined rather than per line.
	for i := range entries {
		if m := semverHint.FindStringSubmatch(entries[i].Text); m != nil {
			entries[i].Semver = m[1]
		}
		entries[i].Text = stripSemverMarker(entries[i].Text)
	}
	return entries
}

// stripSemverMarker removes the trailing `— \`Semver: x\“ from an entry; it is
// a release hint, not part of the changelog prose.
func stripSemverMarker(s string) string {
	re := regexp.MustCompile("\\s*—\\s*`Semver:\\s*\\w+`\\s*$")
	return re.ReplaceAllString(s, "")
}

// NextVersion computes the next version from the Semver hints in the Unreleased
// section, incrementing lastTag. The highest hint present wins: one `minor`
// among fifty `patch`es is still a minor release. It returns the version
// WITHOUT a leading `v` (the changelog convention); callers add the `v` for the
// tag.
func NextVersion(lastTag string, entries []Entry) (version, bump string, err error) {
	hints := map[string]bool{}
	for _, e := range entries {
		if e.Semver != "" {
			hints[e.Semver] = true
		}
	}
	if len(hints) == 0 {
		return "", "", fmt.Errorf("no 'Semver: patch|minor|major' hints in [Unreleased]")
	}

	switch {
	case hints["major"]:
		bump = "major"
	case hints["minor"]:
		bump = "minor"
	default:
		bump = "patch"
	}

	base := strings.TrimPrefix(strings.TrimSpace(lastTag), "v")
	var maj, min, pat int
	if _, err := fmt.Sscanf(base, "%d.%d.%d", &maj, &min, &pat); err != nil {
		return "", "", fmt.Errorf("last tag %q is not vX.Y.Z: %w", lastTag, err)
	}

	switch bump {
	case "major":
		maj, min, pat = maj+1, 0, 0
	case "minor":
		min, pat = min+1, 0
	case "patch":
		pat++
	}
	return fmt.Sprintf("%d.%d.%d", maj, min, pat), bump, nil
}

// Title returns an entry's short human title: its bold lead-in when it has one,
// else its first sentence, truncated.
func (e Entry) Title() string {
	if m := boldTitle.FindStringSubmatch(e.Text); m != nil {
		return strings.TrimSuffix(m[1], ".")
	}
	first := e.Text
	if i := strings.IndexAny(first, ".!?"); i >= 0 {
		first = first[:i]
	}
	return truncate(first, 110)
}

// Refs returns the issue numbers an entry cites, in order of appearance.
func (e Entry) Refs() []int {
	var out []int
	for _, m := range naviRef.FindAllStringSubmatch(e.Text, -1) {
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Draft builds the tag-message draft: a subject placeholder, the entry count,
// each section's entry titles (bounded), and the issue reference list.
//
// It is a DRAFT, not generated prose. Every tag in this repository before this
// tool was a hand-written 15-25 line summary; a message that dumps two hundred
// entries is not a release note. The operator fills the subject and writes the
// prose in $EDITOR, with the inventory already laid out.
func Draft(version, lastTag string, entries []Entry, maxPerHeading, maxRefs int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "v%s — <one-line summary of this release>\n\n", version)
	fmt.Fprintf(&b, "%d change%s since %s.\n\n", len(entries), plural(len(entries)), lastTag)

	// Group by heading, preserving document order.
	var order []string
	byHeading := map[string][]Entry{}
	for _, e := range entries {
		if _, seen := byHeading[e.Heading]; !seen {
			order = append(order, e.Heading)
		}
		byHeading[e.Heading] = append(byHeading[e.Heading], e)
	}

	for _, h := range order {
		es := byHeading[h]
		fmt.Fprintf(&b, "%s:\n", h)
		for i, e := range es {
			if i == maxPerHeading {
				fmt.Fprintf(&b, "  - … and %d more\n", len(es)-maxPerHeading)
				break
			}
			fmt.Fprintf(&b, "  - %s\n", e.Title())
		}
		b.WriteString("\n")
	}

	if refs := allRefs(entries); len(refs) > 0 {
		shown := refs
		suffix := ""
		if len(refs) > maxRefs {
			shown = refs[:maxRefs]
			suffix = fmt.Sprintf(", … (+%d more)", len(refs)-maxRefs)
		}
		parts := make([]string, len(shown))
		for i, n := range shown {
			parts[i] = fmt.Sprintf("NAVI-%d", n)
		}
		fmt.Fprintf(&b, "Refs %s%s\n", strings.Join(parts, ", "), suffix)
	}

	return b.String()
}

// allRefs returns every issue number cited by the entries, deduplicated and
// ascending.
func allRefs(entries []Entry) []int {
	seen := map[int]bool{}
	for _, e := range entries {
		for _, n := range e.Refs() {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Cut moves the Unreleased section's entries into a new `## [version] - date`
// section placed directly below it, leaving `[Unreleased]` present but empty so
// the next release has a home.
//
// This is what makes repeated releases tractable. Without it the Unreleased
// section grows without bound and every release re-lists everything since the
// beginning — which is the state this repository was in when the tool was
// written (200 entries, spanning v0.1.0 to the present).
func Cut(changelog, version, date string) (string, error) {
	body, start, end, ok := sectionBody(changelog)
	if !ok {
		return "", fmt.Errorf("no '## [Unreleased]' section found")
	}

	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("[Unreleased] is empty — nothing to release")
	}
	if strings.Contains(changelog, "## ["+version+"]") {
		return "", fmt.Errorf("[%s] already exists in the changelog", version)
	}

	// Keep the Unreleased heading (so the next release has a home), drop its
	// body, and insert the versioned section where the body was.
	heading := changelog[start:end]
	heading, _, _ = strings.Cut(heading, "\n")

	section := fmt.Sprintf("\n## [%s] - %s\n\n%s\n\n", version, date, body)
	return changelog[:start] + heading + "\n" + section + changelog[end:], nil
}

// plural returns "s" for a count other than one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncate shortens s to n runes, appending an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
