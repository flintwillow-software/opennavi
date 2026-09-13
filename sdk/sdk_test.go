package sdk

import (
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/tools/registry"
)

func validDef() NaviDef {
	return NaviDef{
		Name:        "oncall-bot",
		Version:     "1.0.0",
		Personality: "You are OnCall Bot.",
	}
}

func TestBuildValidates(t *testing.T) {
	if err := Build(validDef()); !errors.Is(err, ErrBuilderUnavailable) {
		t.Fatalf("Build(valid) = %v, want ErrBuilderUnavailable", err)
	}
}

func TestBuildRejectsInvalid(t *testing.T) {
	cases := map[string]func(*NaviDef){
		"empty name":     func(d *NaviDef) { d.Name = "" },
		"bad name":       func(d *NaviDef) { d.Name = "Oncall Bot" },
		"empty version":  func(d *NaviDef) { d.Version = "" },
		"no personality": func(d *NaviDef) { d.Personality = "" },
		"skill no body":  func(d *NaviDef) { d.Skills = []Skill{{Name: "s"}} },
		"dup skill": func(d *NaviDef) {
			d.Skills = []Skill{{Name: "s", Content: "x"}, {Name: "s", Content: "y"}}
		},
	}
	for label, mutate := range cases {
		def := validDef()
		mutate(&def)
		if err := Build(def); err == nil || errors.Is(err, ErrBuilderUnavailable) {
			t.Errorf("%s: Build = %v, want validation error", label, err)
		}
	}
}

func TestValidateAcceptsToolMetadata(t *testing.T) {
	def := validDef()
	def.Tools = []registry.ToolDefinition{{Name: "github_issue_read"}}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate with tool metadata = %v", err)
	}
}
