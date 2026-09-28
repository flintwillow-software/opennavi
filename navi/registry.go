package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/internal/bridge"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// buildRegistry converts the declared Tools collection into the runtime
// tool registry. The registry owns metadata, validation,
// active-set membership, and dispatch; the runner binds a mode-selected
// subset of it into each thought's agent executor via tool/bridge.
//
// The compiled default active set is every registered tool the navi's
// permission ceiling permits. A thought's mode narrows that further at
// bind time (ADR 0007) — it can never widen it (ADR 0006).
func (n *Navi) buildRegistry() (*registry.Registry, error) {
	reg := registry.New()
	tools := n.Tools.All()
	// model_list is a core discovery tool (ADR 0009), not a catalog entry:
	// it is registered automatically whenever the navi declares models,
	// and is absent when there is no set to discover.
	if n.Models.Len() > 0 {
		ml, err := n.modelListTool()
		if err != nil {
			return nil, err
		}
		tools = append(tools, ml)
	}
	for _, t := range tools {
		def, err := toRegistryDefinition(t.Def)
		if err != nil {
			return nil, fmt.Errorf("navi: tool %q: %w", t.Def.Name, err)
		}
		if err := reg.Register(def, registry.Handler(t.Handler)); err != nil {
			return nil, fmt.Errorf("navi: register tool %q: %w", t.Def.Name, err)
		}
	}
	if err := reg.SetActive(n.defaultActiveNames(reg)); err != nil {
		return nil, fmt.Errorf("navi: set default active set: %w", err)
	}
	return reg, nil
}

// defaultActiveNames is the compiled default surface: every registered
// tool the permission ceiling permits by name and access level.
func (n *Navi) defaultActiveNames(reg *registry.Registry) []string {
	names := reg.AllNames()
	out := make([]string, 0, len(names))
	for _, name := range names {
		def, ok := reg.Get(name)
		if !ok {
			continue
		}
		if !n.Permissions.PermitsTool(name) {
			continue
		}
		if !n.Permissions.Permits(tool.ToolAccess(def.Access)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// toRegistryDefinition mirrors a public tool.Def onto the runtime
// registry's ToolDefinition. The two are field-for-field identical by
// design (see the tool package note); this is the mechanical bridge, and
// the only deliberate difference — the unexported argument-preparation
// hook — is a registration concern expressed through the registry's
// WithPrepareArguments option, not the descriptor ABI.
func toRegistryDefinition(d tool.Def) (registry.ToolDefinition, error) {
	schema, err := registry.ParseSchema(d.InputSchema.Raw())
	if err != nil {
		return registry.ToolDefinition{}, err
	}
	return registry.ToolDefinition{
		Name:             d.Name,
		Description:      d.Description,
		InputSchema:      schema,
		OutputFormat:     d.OutputFormat,
		Access:           registry.ToolAccess(d.Access),
		Timeout:          d.Timeout,
		Recovery:         d.Recovery,
		Mode:             registry.ToolMode(d.Mode),
		Tags:             append([]string(nil), d.Tags...),
		Version:          d.Version,
		PromptSnippet:    d.PromptSnippet,
		PromptGuidelines: append([]string(nil), d.PromptGuidelines...),
		SourceInfo: registry.SourceInfo{
			Origin: registry.ToolOrigin(d.SourceInfo.Origin),
			Path:   d.SourceInfo.Path,
		},
	}, nil
}

// bindOptions resolves the tool surface for one thought (ADR 0007): the
// compiled ceiling narrowed by the thought's mode, rendered as the
// bridge's bind-time policy. An unknown mode is an error — a thought
// naming a mode the navi does not declare fails loudly rather than
// silently running with the default surface.
func (n *Navi) bindOptions(t *thought.Thought) (bridge.Options, error) {
	// Start from the compiled ceiling.
	ceilingAllowed := n.Permissions.AllowTools
	blocked := append([]string(nil), n.Permissions.DenyTools...)
	maxAccess := n.Permissions.effectiveMax()

	// The mode narrows: it may replace the requested names (Tools) and
	// deny more, but the ceiling's own allow/deny and access cap still
	// apply. validateModes argues this statically for m.Permissions;
	// applying it again here also covers m.Tools, which is not part of
	// the static Exceeds check.
	requested := ceilingAllowed
	if t.Mode != "" {
		m, ok := n.Modes.Get(t.Mode)
		if !ok {
			return bridge.Options{}, fmt.Errorf("%w: thought %s: unknown mode %q",
				ErrInvalid, t.ID, t.Mode)
		}
		if len(m.Tools) > 0 {
			requested = m.Tools
		}
		blocked = append(blocked, m.Permissions.DenyTools...)
		if mm := m.Permissions.effectiveMax(); mm < maxAccess {
			maxAccess = mm
		}
	}

	allowed := intersectAllowed(requested, n.Permissions.AllowTools, n.Permissions.DenyTools)
	opts := bridge.Options{
		Policy: bridge.Policy{Allowed: allowed, Blocked: blocked},
	}
	if maxAccess != tool.AccessExecute {
		for lvl := tool.AccessReadOnly; lvl <= maxAccess; lvl++ {
			opts.Access = append(opts.Access, registry.ToolAccess(lvl))
		}
	}
	return opts, nil
}

// intersectAllowed narrows requested by the ceiling's allow-list (when
// it has one) and removes ceiling-denied names, so a mode can never
// surface a tool the compiled permission ceiling excludes.
func intersectAllowed(requested, ceilingAllow, ceilingDeny []string) []string {
	if len(requested) == 0 {
		return nil
	}
	allowSet := setOf(ceilingAllow)
	denySet := setOf(ceilingDeny)
	out := make([]string, 0, len(requested))
	for _, name := range requested {
		if _, denied := denySet[name]; denied {
			continue
		}
		if len(allowSet) > 0 {
			if _, ok := allowSet[name]; !ok {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}
