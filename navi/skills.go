package navi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

// Compiled skills (ADR 0013 decisions 5 and 6). The navi's skill surface is
// the authored skills plus the generated core skills: authored prose per
// declared component type, and a mechanical inventory derived from component
// metadata. All three loading paths resolve against this one set — it is a
// menu, never a fetch.
//
// The set is built at assembly, after the component collections are known, so
// the inventory cannot drift from main.go.

// assembleSkills builds the compiled skill set.
func (n *Navi) assembleSkills() {
	index := map[string]skill.Skill{}
	var order []string
	add := func(s skill.Skill, override bool) {
		if _, exists := index[s.Name]; exists && !override {
			return
		}
		if _, exists := index[s.Name]; !exists {
			order = append(order, s.Name)
		}
		index[s.Name] = s
	}

	for _, s := range n.Skills.All() {
		add(s, true)
	}
	for _, s := range n.coreSkills() {
		add(s, false)
	}

	n.skillIndex = index
	n.skillOrder = order
}

// coreSkills generates the SDK skills for the component types this navi
// actually declares, plus the inventory.
func (n *Navi) coreSkills() []skill.Skill {
	var out []skill.Skill
	if n.Tools.Len() > 0 {
		out = append(out, skill.CoreTools())
	}
	// The thought system is core, not added (ADR 0003).
	out = append(out, skill.CoreThoughts())
	// The memory skill names the tier tools, so it ships only when the navi
	// actually declares memory — a navi with no memory surface must not be
	// told about tools it does not have.
	if n.shortMemory != nil {
		out = append(out, skill.CoreMemory())
	}
	if n.Connectors.Len() > 0 {
		out = append(out, skill.CoreConnectors())
	}
	if n.Portals.Len() > 0 {
		out = append(out, skill.CorePortals())
	}
	if n.Models.Len() > 0 {
		out = append(out, skill.CoreModels())
	}
	if n.Datastores.Len() > 0 {
		out = append(out, skill.CoreDatastores())
	}
	if n.Modes.Len() > 0 {
		out = append(out, skill.CoreModes())
	}
	out = append(out, n.inventorySkill())
	return out
}

// inventorySkill is the mechanical half of decision 6: a generated list of
// the components the binary actually has, so the model is never unaware of a
// capability. It stays a list of names — the per-tool schemas are already in
// the tool surface — because it costs context on every thought.
func (n *Navi) inventorySkill() skill.Skill {
	var b strings.Builder
	b.WriteString("The components this navi is composed of, generated from its compiled declarations:\n\n")

	var toolNames []string
	for _, t := range n.Tools.All() {
		toolNames = append(toolNames, t.Def.Name)
	}
	if len(toolNames) > 0 {
		fmt.Fprintf(&b, "- Tools (%d): %s\n", len(toolNames), strings.Join(toolNames, ", "))
	}
	if conns := n.Connectors.All(); len(conns) > 0 {
		names := make([]string, 0, len(conns))
		for _, c := range conns {
			names = append(names, describe(c))
		}
		fmt.Fprintf(&b, "- Connectors (%d): %s\n", len(conns), strings.Join(names, ", "))
	}
	if portals := n.Portals.All(); len(portals) > 0 {
		names := make([]string, 0, len(portals))
		for _, p := range portals {
			names = append(names, describe(p))
		}
		fmt.Fprintf(&b, "- Portals (%d): %s\n", len(portals), strings.Join(names, ", "))
	}
	if modes := n.Modes.All(); len(modes) > 0 {
		names := make([]string, 0, len(modes))
		for _, m := range modes {
			names = append(names, m.Name)
		}
		fmt.Fprintf(&b, "- Modes: %s\n", strings.Join(names, ", "))
	}
	if models := n.Models.All(); len(models) > 0 {
		names := make([]string, 0, len(models))
		for _, m := range models {
			label := m.Name + " (" + m.Model + ")"
			if m.Default {
				label += ", default"
			}
			names = append(names, label)
		}
		fmt.Fprintf(&b, "- Providers: %s\n", strings.Join(names, ", "))
	}
	if ds := n.Datastores.All(); len(ds) > 0 {
		names := make([]string, 0, len(ds))
		for _, d := range ds {
			label := d.Name
			if d.Default {
				label += " (default)"
			}
			names = append(names, label)
		}
		fmt.Fprintf(&b, "- Datastores: %s\n", strings.Join(names, ", "))
	}

	return skill.Skill{
		Name:        "core_inventory",
		Description: "The components this navi is composed of, generated from its declarations.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content:     strings.TrimRight(b.String(), "\n"),
	}
}

// Resolve maps skill names to their content. An unknown name is an error
// naming the compiled-in set — a skill request is a menu selection, never a
// fetch.
func (n *Navi) Resolve(names []string) ([]skill.Skill, error) {
	var out []skill.Skill
	for _, name := range names {
		s, ok := n.skillIndex[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown skill %q (compiled in: %s)",
				ErrInvalid, name, strings.Join(n.skillOrder, ", "))
		}
		out = append(out, s)
	}
	return out, nil
}

// loadedSkills returns the skills a thought loads: every auto-loaded skill
// (the core set plus author-declared ones) followed by the thought's own
// declared requests.
func (n *Navi) loadedSkills(t *thought.Thought) ([]skill.Skill, error) {
	var names []string
	seen := map[string]bool{}
	for _, name := range n.skillOrder {
		if n.skillIndex[name].AutoLoad {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, name := range t.Skills {
		if seen[name] {
			continue
		}
		names = append(names, name)
	}
	return n.Resolve(names)
}

// skillsBlock renders the thought's loaded skills as one context block. It
// returns "" when nothing is loaded.
func (n *Navi) skillsBlock(t *thought.Thought) (string, error) {
	skills, err := n.loadedSkills(t)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, s := range skills {
		b.WriteString("## Skill: ")
		b.WriteString(s.Name)
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(s.Content))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// SkillTool returns the agent-chosen skill-loading tool. The author adds it
// explicitly (`n.Tools.Add(n.SkillTool())`) like any other tool, and it can
// only select from the compiled-in set — the third loading path of ADR 0013
// decision 5.
func (n *Navi) SkillTool() (tool.Tool, error) {
	schema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The name of a compiled-in skill, as listed in the component inventory."
			}
		},
		"required": ["name"],
		"additionalProperties": false
	}`))
	if err != nil {
		return tool.Tool{}, err
	}

	return tool.Tool{
		Def: tool.Def{
			Name:          "skill_load",
			Description:   "Load one compiled-in skill's guidance into the conversation. Use it when a task needs guidance you do not already have in context; an unknown name fails rather than fetching anything.",
			InputSchema:   schema,
			OutputFormat:  "markdown",
			Access:        tool.AccessReadOnly,
			Timeout:       5 * time.Second,
			Recovery:      "none",
			Mode:          tool.ToolModeParallel,
			Tags:          []string{"skill", "context"},
			Version:       "0.1.0",
			PromptSnippet: "skill_load — load a compiled-in skill's guidance by name",
			PromptGuidelines: []string{
				"Load a skill when you need it; do not load every skill you can see.",
			},
			SourceInfo: tool.SourceInfo{Origin: tool.OriginSDK},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			name, _ := args["name"].(string)
			if strings.TrimSpace(name) == "" {
				return "", fmt.Errorf("skill_load: name is required")
			}
			skills, err := n.Resolve([]string{name})
			if err != nil {
				return "", err
			}
			return skills[0].Content, nil
		},
	}, nil
}
