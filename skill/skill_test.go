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
