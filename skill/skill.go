// Package skill is the public skill component (ADR 0004): a skill is a
// self-contained capability bundle whose content is embedded in the
// definition — instructions, examples, and constraints the navi loads
// as context when the skill is active. Stdlib-only; no imports beyond
// the Go standard library.
package skill

// Skill is the value carried by a skill definition: a short identity,
// a human description, and the EMBEDDED content that ships with the
// definition. Content is verbatim (no fetching, no references) —
// embedding is the point: a skill works offline and version-couples
// with the definition that carried it.
type Skill struct {
	// Name is the skill identifier (namespace_action convention).
	Name string
	// Description explains what the skill does in one line.
	Description string
	// Content is the embedded skill body (instructions, templates,
	// constraints) — shipped with the definition, not fetched.
	Content string
	// Tags are discovery keywords beyond the name.
	Tags []string
	// Version is the skill content version for pinned, reproducible
	// definition sets.
	Version string
	// AutoLoad marks a skill the author declares always-present (ADR 0013
	// decision 5, the author-declared path). A skill without it reaches a
	// thought only when the thought declares it or the agent loads it.
	AutoLoad bool
}

// Embedded reports whether the skill carries content (a definition
// with an empty body is a stub/pointer and should not be treated as
// executable).
func (s Skill) Embedded() bool {
	return s.Content != ""
}
