package navi

import (
	"context"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

func invTool(name string) tool.Tool {
	return scriptedTool(name, tool.AccessReadOnly, func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	})
}

func mustResolve(t *testing.T, n *Navi, name string) string {
	t.Helper()
	got, err := n.Resolve([]string{name})
	if err != nil {
		t.Fatalf("resolve %q: %v", name, err)
	}
	return got[0].Content
}

// TestInventoryReflectsComponents is the issue's first gate: a navi with no
// authored skills still presents a correct generated inventory, and adding a
// component changes it.
func TestInventoryReflectsComponents(t *testing.T) {
	n := New("inventory-test")
	if err := n.Tools.Add(invTool("alpha_read")); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	before := mustResolve(t, n, "core_inventory")
	if !strings.Contains(before, "alpha_read") {
		t.Fatalf("inventory omits a declared tool:\n%s", before)
	}
	if strings.Contains(before, "beta_read") {
		t.Fatalf("inventory names a tool the navi does not have:\n%s", before)
	}

	if err := n.Tools.Add(invTool("beta_read")); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()
	after := mustResolve(t, n, "core_inventory")
	if !strings.Contains(after, "alpha_read") || !strings.Contains(after, "beta_read") {
		t.Fatalf("inventory did not follow the added tool:\n%s", after)
	}
}

// TestAuthoredSkillLoadsOnlyWhenDeclared is the second gate: authored content
// reaches a thought only when loaded, never unconditionally.
func TestAuthoredSkillLoadsOnlyWhenDeclared(t *testing.T) {
	n := New("skill-load-test")
	if err := n.Skills.Add(skill.Skill{
		Name:        "deploy_runbook",
		Description: "how we deploy",
		Content:     "Deploy by tagging a release; never hot-patch production.",
	}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	// Not auto-loaded, not declared: absent.
	block, err := n.skillsBlock(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block, "never hot-patch production") {
		t.Fatalf("an unloaded skill reached the context:\n%s", block)
	}

	// Declared on the thought: present.
	declared, err := n.skillsBlock(&thought.Thought{ID: "t2", Skills: []string{"deploy_runbook"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(declared, "never hot-patch production") {
		t.Fatalf("a declared skill did not reach the context:\n%s", declared)
	}
}

// TestAutoLoadedAuthoredSkillIsAlwaysPresent covers the author-declared path.
func TestAutoLoadedAuthoredSkillIsAlwaysPresent(t *testing.T) {
	n := New("auto-skill-test")
	if err := n.Skills.Add(skill.Skill{
		Name:     "house_style",
		Content:  "Answer in short paragraphs.",
		AutoLoad: true,
	}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	block, err := n.skillsBlock(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "Answer in short paragraphs.") {
		t.Fatalf("auto-loaded skill absent:\n%s", block)
	}
}

// TestResolveRejectsUnknownSkill is the third gate: every path selects only
// from the compiled-in set.
func TestResolveRejectsUnknownSkill(t *testing.T) {
	n := New("resolve-test")
	n.assembleSkills()

	if _, err := n.Resolve([]string{"does_not_exist"}); err == nil {
		t.Fatal("an unknown skill must fail, not resolve")
	} else if !strings.Contains(err.Error(), "does_not_exist") {
		t.Fatalf("error should name the requested skill: %v", err)
	}

	if _, err := n.skillsBlock(&thought.Thought{ID: "t1", Skills: []string{"does_not_exist"}}); err == nil {
		t.Fatal("a thought declaring an unknown skill must fail")
	}
}

// TestSkillLoadTool covers the agent-chosen path, including its menu-only
// failure mode.
func TestSkillLoadTool(t *testing.T) {
	n := New("skill-tool-test")
	if err := n.Skills.Add(skill.Skill{Name: "runbook", Content: "the runbook body"}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	tl, err := n.SkillTool()
	if err != nil {
		t.Fatal(err)
	}
	if tl.Def.Name != "skill_load" || tl.Def.Access != tool.AccessReadOnly {
		t.Fatalf("skill tool def = %+v", tl.Def)
	}

	out, err := tl.Handler(context.Background(), map[string]any{"name": "runbook"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the runbook body") {
		t.Fatalf("skill_load output = %q", out)
	}

	if _, err := tl.Handler(context.Background(), map[string]any{"name": "nope"}); err == nil {
		t.Fatal("skill_load of an unknown name must fail")
	}
	if _, err := tl.Handler(context.Background(), map[string]any{}); err == nil {
		t.Fatal("skill_load without a name must fail")
	}
}

// TestCoreSkillPerComponentType is the fourth gate: one real core skill per
// shipped component type, present only when the navi declares that type.
func TestCoreSkillPerComponentType(t *testing.T) {
	n := New("core-skill-test")
	if err := n.Tools.Add(invTool("alpha_read")); err != nil {
		t.Fatal(err)
	}
	if err := n.Connectors.Add(&stubConnector{}); err != nil {
		t.Fatal(err)
	}
	if err := n.Portals.Add(portal.NewControl(portal.AllowAll())); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(stubProvider()); err != nil {
		t.Fatal(err)
	}
	if err := n.Datastores.Add(stubCandidate()); err != nil {
		t.Fatal(err)
	}
	if err := n.Modes.Add(Mode{Name: "execute"}); err != nil {
		t.Fatal(err)
	}
	n.assembleSkills()

	for _, name := range []string{
		"core_tools", "core_thoughts", "core_connectors", "core_portals",
		"core_models", "core_datastores", "core_modes", "core_inventory",
	} {
		if content := mustResolve(t, n, name); strings.TrimSpace(content) == "" {
			t.Fatalf("core skill %q has no content", name)
		}
	}

	// A navi without tools gets no tool guidance.
	bare := New("bare")
	bare.assembleSkills()
	if _, err := bare.Resolve([]string{"core_tools"}); err == nil {
		t.Fatal("a navi with no tools must not get the tool core skill")
	}
	if _, err := bare.Resolve([]string{"core_thoughts"}); err != nil {
		t.Fatalf("the thought system is core, so its guidance is always present: %v", err)
	}
}

// TestSkillNamespaceIsReserved keeps the generated core set unambiguous.
func TestSkillNamespaceIsReserved(t *testing.T) {
	n := New("namespace-test")
	if err := n.Skills.Add(skill.Skill{Name: "core_tools", Content: "shadow"}); err == nil {
		t.Fatal("the core_ namespace is reserved for SDK skills")
	}
	if err := n.Skills.Add(skill.Skill{Name: "fine_name", Content: "ok"}); err != nil {
		t.Fatalf("an ordinary name must be accepted: %v", err)
	}
}

// TestInventoryTokenCost measures the generated inventory's context cost,
// which is paid on every thought that loads it (reported in runtime.md).
func TestInventoryTokenCost(t *testing.T) {
	n := New("cost-test")
	for _, name := range []string{"file_read", "shell_exec", "todo_read", "todo_write", "note_read", "note_write", "time_read"} {
		if err := n.Tools.Add(invTool(name)); err != nil {
			t.Fatal(err)
		}
	}
	n.assembleSkills()

	counter := llm.HeuristicTokenCounter{}
	block, err := n.skillsBlock(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	cost := counter.CountText(block)
	t.Logf("auto-loaded core skills cost ~%d tokens for a 7-tool navi", cost)
	if cost <= 0 {
		t.Fatal("token cost must be measurable")
	}
	if cost > 1500 {
		t.Fatalf("core skills cost ~%d tokens, larger than the compact budget allows", cost)
	}
}

// Minimal component doubles for the inventory/core-skill tests.

type stubConnector struct{}

func (stubConnector) Start(context.Context, connector.Deps) error { return nil }
func (stubConnector) Stop() error                                 { return nil }

func stubProvider() model.Provider { return model.Ollama("test-model") }

func stubCandidate() datastore.Candidate {
	return datastore.Candidate{
		Name:    "inmem",
		Default: true,
		Open:    func(datastore.Params) (datastore.Backend, error) { return inmem.New(), nil },
	}
}
