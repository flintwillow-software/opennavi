package skill

import "testing"

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
