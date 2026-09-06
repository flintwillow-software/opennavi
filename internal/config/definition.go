// Package config implements agent configuration (spec
// 00-navi-core/02-navi-definition.md): the static NaviDefinition
// (build-time, validated per §05) and the runtime RuntimeConfig with
// the defaults → file → env → CLI-flags merge chain (spec "Static
// Definition vs. Runtime Config Separation" — runtime config never
// modifies the compiled definition).
package config

import (
	"fmt"
	"regexp"
	"strings"
)

// PermissionRule is one allow/deny rule (spec: IAM-style permissions).
type PermissionRule struct {
	Tool   string   `yaml:"tool,omitempty"`
	Path   string   `yaml:"path,omitempty"`
	Action []string `yaml:"action,omitempty"`
}

// SkillDefinition is one compiled-in skill.
type SkillDefinition struct {
	Name             string   `yaml:"name"`
	InstructionsFile string   `yaml:"instructions_file,omitempty"`
	Instructions     string   `yaml:"instructions,omitempty"`
	Keywords         []string `yaml:"keywords,omitempty"`
}

// ToolDefinition is one compiled-in tool.
type ToolDefinition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Parameters  string `yaml:"parameters,omitempty"` // JSON Schema
	// Handler wiring is a builder (#140) concern — not parsed here.
}

// ThoughtQueueConfig mirrors the navi definition's thought_queue block.
type ThoughtQueueConfig struct {
	MaxConcurrent  int    `yaml:"max_concurrent,omitempty"`
	PrioritySlots  int    `yaml:"priority_slots,omitempty"`
	SlotExhaustion string `yaml:"slot_exhaustion,omitempty"`
}

// InterfaceEntry is one interface block in the definition (parsed
// generically; the interfaces package owns the full InterfaceSet).
type InterfaceEntry struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port,omitempty"`
}

// ExtensionConfig declares optional runtime plugins.
type ExtensionConfig struct {
	AllowStandalonePlugins bool     `yaml:"allow_standalone_plugins,omitempty"`
	AllowedSigners         []string `yaml:"allowed_signers,omitempty"`
}

// NaviDefinition is the static, build-time navi definition (spec §01).
type NaviDefinition struct {
	Name         string                    `yaml:"name"`
	Version      string                    `yaml:"version,omitempty"`
	Personality  string                    `yaml:"personality,omitempty"`
	Tools        []ToolDefinition          `yaml:"tools,omitempty"`
	Skills       []SkillDefinition         `yaml:"skills,omitempty"`
	Interfaces   map[string]InterfaceEntry `yaml:"interfaces,omitempty"`
	ThoughtQueue *ThoughtQueueConfig       `yaml:"thought_queue,omitempty"`
	Extensions   ExtensionConfig           `yaml:"extensions,omitempty"`
	Permissions  struct {
		Allow []PermissionRule `yaml:"allow,omitempty"`
		Deny  []PermissionRule `yaml:"deny,omitempty"`
	} `yaml:"permissions"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate applies the §05 build-time validation rules:
// name format, tool uniqueness, interface compatibility, permission
// rule validity, personality format. Extension signer verification is
// deferred to the #144 extension system.
func (d *NaviDefinition) Validate() error {
	if err := validateName(d.Name); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, t := range d.Tools {
		if t.Name == "" {
			return fmt.Errorf("definition: tool with empty name")
		}
		if seen[t.Name] {
			return fmt.Errorf("definition: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
	}

	if err := validateInterfaces(d.Interfaces); err != nil {
		return err
	}

	// Permission rules may only reference defined tools.
	for _, r := range append(append([]PermissionRule{}, d.Permissions.Allow...), d.Permissions.Deny...) {
		if r.Tool != "" && !seen[r.Tool] {
			return fmt.Errorf("definition: permission rule references undefined tool %q", r.Tool)
		}
		if r.Tool == "" && r.Path == "" {
			return fmt.Errorf("definition: permission rule with neither tool nor path")
		}
	}

	if err := validatePersonality(d.Personality); err != nil {
		return err
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("definition: name is required")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("definition: name %q invalid — must be lowercase alphanumeric with hyphens", name)
	}
	if len(name) > 64 {
		return fmt.Errorf("definition: name %q too long (max 64)", name)
	}
	return nil
}

// validateInterfaces checks conflicting HTTP-listening interfaces
// (spec §05 rule 3).
func validateInterfaces(ifaces map[string]InterfaceEntry) error {
	gateway, webui := ifaces["gateway"], ifaces["webui"]
	if gateway.Enabled && webui.Enabled {
		gp, wp := gateway.Port, webui.Port
		if gp == 0 {
			gp = 443
		}
		if wp == 0 {
			wp = 8080
		}
		if gp == wp {
			return fmt.Errorf("definition: gateway and webui conflict on port %d", gp)
		}
	}
	return nil
}

// validatePersonality checks the personality format (spec §05 rule 6):
// plain string, file: path, or dir: path.
func validatePersonality(p string) error {
	if p == "" {
		return nil // optional
	}
	switch {
	case strings.HasPrefix(p, "file:"), strings.HasPrefix(p, "dir:"):
		path := p[strings.Index(p, ":")+1:]
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("definition: personality %q: missing path after prefix", p)
		}
	}
	return nil
}
