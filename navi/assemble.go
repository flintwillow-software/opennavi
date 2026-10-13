package navi

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flintwillow-software/opennavi/thought"
)

// DefaultThoughtBudget is the assembled-context budget in tokens when
// WithThoughtBudget is unset. It is a soft ceiling: assembly truncates past
// it with a reported marker, and the agent is told as it approaches it.
const DefaultThoughtBudget = 16000

// assembleThought is the creation-time assembler the navi installs on its own
// thought queue (ADR 0013 decision 2). It is the single place a thought's
// context is built: personality, the mode fragment, the skills it loads, and
// the always-loaded short-term memory. Because it runs at Enqueue, a thought
// carries its context to the runner and a thought that cannot be assembled is
// rejected at the door.
//
// Every input is a build-time declaration or in-RAM state — no runtime config
// path can add a source, which is the injection boundary of ADR 0013 decision
// 4. Assembly is reproducible: the same declarations and the same memory
// yield the same block.
func (n *Navi) assembleThought(t *thought.Thought) error {
	// A follow-up inherits its originator from the thought it continues (ADR
	// 0022 decision 3): a thought with no origin that declares a dependency
	// takes that dependency's principal, request facts, and lineage. This is
	// the queue-driven inheritance rule — `thoughts_create` chains a plan by
	// naming the ids it continues, and the runtime's own closing check
	// depends on the plan's sinks — so "on whose behalf" is carried without
	// the creating tool having to know it. A thought with neither an origin
	// nor a dependency is system-originated and acts as navi:<name>
	// (decision 3, ADR 0021 decision 10).
	if t.Origin.IsZero() {
		if parent := n.parentThought(t); parent != nil {
			t.InheritFrom(parent)
		}
	}

	prompt := n.personality

	if t.Mode != "" {
		if m, ok := n.Modes.Get(t.Mode); ok && m.Prompt != "" {
			prompt = joinBlocks(prompt, m.Prompt)
		}
	}

	skills, err := n.skillsBlock(t)
	if err != nil {
		return err
	}
	prompt = joinBlocks(prompt, skills)

	if n.shortMemory != nil {
		prompt = joinBlocks(prompt, n.shortMemory.Inject())
	}

	t.Assembled = n.truncateContext(prompt)
	return nil
}

// parentThought returns the first of a thought's declared dependencies that
// the queue can resolve, or nil. It runs from the assembler, outside the
// queue lock, so reading the parent is safe.
func (n *Navi) parentThought(t *thought.Thought) *thought.Thought {
	if n.Thoughts == nil {
		return nil
	}
	for _, id := range t.DependsOn {
		if p, err := n.Thoughts.GetThought(id); err == nil {
			return p
		}
	}
	return nil
}

// truncateContext enforces the thought budget, cutting at a rune boundary
// with a visible marker. Truncation is never silent.
func (n *Navi) truncateContext(block string) string {
	budget := n.thoughtBudget
	if budget <= 0 || estimateTokens(block) <= budget {
		return block
	}

	keep := budget * 4
	if keep > len(block) {
		keep = len(block)
	}
	for keep > 0 && !utf8.RuneStart(block[keep]) {
		keep--
	}
	marker := fmt.Sprintf("\n\n[context truncated: over the %d-token budget]", budget)
	return strings.TrimRight(block[:keep], " \n") + marker
}

// estimateTokens is the ~4-characters-per-token heuristic the agent's budget
// notice also uses, so assembly and the notice agree on the rough size.
func estimateTokens(s string) int { return (len(s) + 3) / 4 }
