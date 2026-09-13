// Package sdk is the public authoring surface of the opennavi SDK
// (S000800). A navi author imports this package, describes the navi in
// a NaviDef, and calls Build from the project entry point
// (cmd/<navi>/main.go, S000100).
//
// Navi definitions are Go-only (S000207): there is no YAML definition
// format. Everything the navi developer owns — name, version,
// personality, skills, tools, interfaces, extensions, permissions — is
// expressed in the NaviDef and compiled into the binary at build time.
// Deployment-time settings (provider endpoints, key paths, trusted
// keys, discovery sources) are runtime config under
// `.config/navi/<name>/` and are deliberately NOT part of NaviDef
// (S000200 §04).
//
// Tools are registered through the public tool registry
// (github.com/flintwillow-software/opennavi/tools/registry, S130400);
// registration IS the declaration. NaviDef.Tools carries the tool
// metadata for builder cross-checking only.
package sdk

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/flintwillow-software/opennavi/tools/registry"
)

// InterfaceConfig configures a single delivery mode compiled into the
// binary (S000400).
type InterfaceConfig struct {
	// Enabled compiles the interface into the binary.
	Enabled bool
	// HTTPPort is the listen port for HTTP-bearing interfaces (gateway).
	HTTPPort int
}

// InterfaceSet selects which delivery modes get compiled in (S000400).
// Only selected interfaces are included in the final executable.
type InterfaceSet struct {
	// CLI enables the interactive terminal interface (full TUI mode).
	CLI bool
	// CLIConfig optionally tunes the CLI interface.
	CLIConfig InterfaceConfig
	// WebUI enables the browser UI.
	WebUI bool
	// WebUIConfig optionally tunes the WebUI.
	WebUIConfig InterfaceConfig
	// Gateway enables the HTTP gateway (programmatic access, webhooks).
	Gateway InterfaceConfig
	// Cron enables scheduled invocation (cron-style triggers).
	Cron InterfaceConfig
}

// Skill is a capability baked into the system prompt at build time
// (compiled-in; no runtime skill loading, S000207).
type Skill struct {
	// Name is the skill identifier referenced in prompts.
	Name string
	// Path is the markdown skill file, relative to the project root.
	Path string
	// Content is an inline alternative to Path for small skills.
	Content string
}

// Extension declares an optional runtime plugin (NOT compiled in;
// S000600).
type Extension struct {
	// Name is the extension identifier.
	Name string
	// Source is the extension location (path or package reference).
	Source string
	// AllowedSigners lists the signing identities trusted for this
	// extension's artifacts.
	AllowedSigners []string
}

// PermissionPolicy is the IAM-style statement of what the navi is
// allowed to do (S000200 §02, compiled in and covered by the binary
// signature).
type PermissionPolicy struct {
	// Allow lists namespaced tool names or wildcards explicitly allowed.
	Allow []string
	// Deny lists namespaced tool names or wildcards always denied.
	// Deny takes precedence over Allow.
	Deny []string
}

// NaviDef is the complete static navi definition — the single source
// of truth compiled into the binary (S000200 §01).
type NaviDef struct {
	// Name identifies the navi: binary filename, system-prompt identity,
	// and plugin/extension compatibility. Must match ^[a-z][a-z0-9-]*$.
	Name string
	// Version is the navi's semantic version.
	Version string

	// Personality is how the navi communicates and thinks; it seeds the
	// system prompt.
	Personality string

	// Skills are compiled into the system prompt at build time.
	Skills []Skill
	// Tools carries tool metadata for builder cross-checking. Tool
	// behavior is registered through the tool registry (S130400).
	Tools []registry.ToolDefinition
	// Interfaces selects which delivery modes are compiled in.
	Interfaces InterfaceSet
	// Extensions declares optional runtime plugins.
	Extensions []Extension
	// Permissions states what the navi is allowed to do.
	Permissions PermissionPolicy
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Validate checks the definition against the S000200 §05 rules.
func (d *NaviDef) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("sdk: navi definition: name is required")
	}
	if !nameRE.MatchString(d.Name) {
		return fmt.Errorf("sdk: navi definition: name %q must match %s", d.Name, nameRE)
	}
	if d.Version == "" {
		return fmt.Errorf("sdk: navi definition %s: version is required", d.Name)
	}
	if d.Personality == "" {
		return fmt.Errorf("sdk: navi definition %s: personality is required", d.Name)
	}
	seen := map[string]bool{}
	for _, s := range d.Skills {
		if s.Name == "" {
			return fmt.Errorf("sdk: navi definition %s: skill with empty name", d.Name)
		}
		if s.Path == "" && s.Content == "" {
			return fmt.Errorf("sdk: navi definition %s: skill %s needs Path or Content", d.Name, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("sdk: navi definition %s: duplicate skill %s", d.Name, s.Name)
		}
		seen[s.Name] = true
	}
	for _, t := range d.Tools {
		if t.Name == "" {
			return fmt.Errorf("sdk: navi definition %s: tool with empty name", d.Name)
		}
	}
	return nil
}

// ErrBuilderUnavailable reports that the definition is valid but the
// builder/compiler pipeline (S000300) is not available in this SDK
// version. Callers can use errors.Is to distinguish it from
// validation failures.
var ErrBuilderUnavailable = errors.New("sdk: builder pipeline is not available in this SDK version (S000300)")

// Build validates the navi definition and compiles it into a single
// self-contained binary (S000300). The build process exits after
// producing the binary.
//
// In this SDK version the builder/compiler pipeline is still landing
// (NAVI-48): Build performs full definition validation and then fails
// with ErrBuilderUnavailable instead of writing a binary. The public
// authoring surface — this signature and NaviDef — is final and is
// guarded by the starter-template builds in CI (S000805).
func Build(def NaviDef) error {
	if err := def.Validate(); err != nil {
		return err
	}
	return fmt.Errorf("%w: definition %s v%s validated", ErrBuilderUnavailable, def.Name, def.Version)
}
