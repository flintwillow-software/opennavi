package navi

import (
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/thought"
)

// TestUserPromptSurfacesTheHandoffNote: a resumed thought is never
// contextless (ADR 0035 decision 3) — the park-time handoff note reaches the
// resumed run's prompt, delimited from the enqueue Context so the two are
// distinguishable.
func TestUserPromptSurfacesTheHandoffNote(t *testing.T) {
	t.Run("no note, no handoff block", func(t *testing.T) {
		got := userPrompt(&thought.Thought{Prompt: "do the thing", Context: "an event arrived"})
		if strings.Contains(got, "handoff") {
			t.Fatalf("a thought with no note must not get a handoff block: %q", got)
		}
		if !strings.Contains(got, "do the thing") || !strings.Contains(got, "an event arrived") {
			t.Fatalf("prompt or context lost: %q", got)
		}
	})

	t.Run("note is surfaced and delimited from context", func(t *testing.T) {
		got := userPrompt(&thought.Thought{
			Prompt:      "do the thing",
			Context:     "an event arrived",
			HandoffNote: "the migration is half-applied; run 003_next",
		})
		if !strings.Contains(got, "the migration is half-applied; run 003_next") {
			t.Fatalf("handoff note not surfaced into the resumed prompt: %q", got)
		}
		// The note must come after the context, inside its own delimited
		// block, so a reader can tell what the thought was told from what the
		// previous run concluded.
		ctxIdx := strings.Index(got, "an event arrived")
		noteIdx := strings.Index(got, "the migration is half-applied")
		delimIdx := strings.Index(got, "handoff from the previous run")
		if !(ctxIdx < delimIdx && delimIdx < noteIdx) {
			t.Fatalf("handoff block not delimited after context: ctx=%d delim=%d note=%d in %q", ctxIdx, delimIdx, noteIdx, got)
		}
	})
}
