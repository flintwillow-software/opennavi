package skill

import (
	"strings"
	"testing"
)

func TestSkillValue(t *testing.T) {
	s := Skill{
		Name:        "code-review",
		Description: "review code changes",
		Content:     "Focus on correctness and security when reviewing.",
		Tags:        []string{"review"},
		Version:     "0.1.0",
	}
	if !s.Embedded() {
		t.Fatal("embedded skill should report content")
	}
	if s.Name != "code-review" || s.Content == "" {
		t.Fatalf("skill fields wrong: %+v", s)
	}
	empty := Skill{Name: "stub", Description: "pointer"}
	if empty.Embedded() {
		t.Error("empty content should not report embedded")
	}
}

// TestCoreThoughtsClosesAPlan pins the guidance half of ADR 0016: a model
// reading the core thought skill is told that a plan is not finished until
// a verification thought closes it, and how to mark that thought.
func TestCoreThoughtsClosesAPlan(t *testing.T) {
	content := CoreThoughts().Content
	for _, want := range []string{"verification", "depends_on", "role"} {
		if !strings.Contains(content, want) {
			t.Fatalf("core_thoughts does not mention %q:\n%s", want, content)
		}
	}
}

// TestCoreThoughtsTeachesHowToObeyASteer pins NAVI-582's guidance against
// ADR 0035 decision 3, so the prose and the decision cannot drift. Each of
// these is a distinct obligation the ADR names; dropping one leaves a steered
// thought with an instruction it cannot act on, which the ADR says is worse
// than a cancellation because the thought keeps running while the more urgent
// work waits.
func TestCoreThoughtsTeachesHowToObeyASteer(t *testing.T) {
	content := CoreThoughts().Content

	obligations := []struct {
		what string
		want []string
	}{
		{"it is a yield, not an abandonment", []string{"put this work down", "not mean the work is cancelled"}},
		{"finish the step you are in", []string{"Finish the step you are in", "mid-edit"}},
		{"write what the next run needs", []string{"memory_write", "what the next run needs"}},
		{"park with a stated reason", []string{"pause", "reason"}},
		{"you will be resumed", []string{"resumed", "checkpoint"}},
		{"do not run on to a finish", []string{"Do not", "try to finish the work first"}},
		{"do not ignore the message", []string{"Do not ignore the message"}},
		{"write as you go, because a p0 gives no chance to wrap up", []string{"Write as you go", "without giving it a chance to wrap up"}},
	}
	for _, ob := range obligations {
		for _, want := range ob.want {
			if !strings.Contains(content, want) {
				t.Errorf("core_thoughts does not cover %q (missing %q)", ob.what, want)
			}
		}
	}
}

// TestCoreThoughtsDistinguishesSteeredFromCancelled is the ADR 0035 decision 3
// distinction: a p0 cancellation gives no opportunity to wrap up, so a thought
// must not assume it will always get to park cleanly.
func TestCoreThoughtsDistinguishesSteeredFromCancelled(t *testing.T) {
	content := CoreThoughts().Content
	if !strings.Contains(content, "highest\npriority stops a thought without giving it a chance to wrap up") &&
		!strings.Contains(content, "without giving it a chance to wrap up") {
		t.Fatalf("core_thoughts does not distinguish a steered yield from a cancellation:\n%s", content)
	}
}
