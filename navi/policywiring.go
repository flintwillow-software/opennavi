package navi

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/object"
)

// This file wires the compiled policy into the navi (ADR 0021 decisions 3 and
// 11): the build-time declarations (n.Roles, n.Groups) and the assembly step
// that derives the ceiling and compiles them against it.

// compilePolicy derives the capability ceiling from what the navi declares and
// compiles the declared roles and groups against it. It runs at assembly, after
// every declaration and before the navi freezes, so a policy naming something
// undeclared fails the run rather than being discovered per request.
func (n *Navi) compilePolicy() error {
	policy, err := NewPolicy(n.capabilityCeiling(), n.Roles.All(), n.Groups.All())
	if err != nil {
		return fmt.Errorf("navi: policy: %w", err)
	}
	n.policy = policy
	return nil
}

// capabilityCeiling is the navi's derived capability ceiling: every declared
// tool, and every exposed type with its fields and actions. Deriving it is what
// makes the referential rule true by construction — a capability naming
// anything else is outside the ceiling, so no policy can grant it.
func (n *Navi) capabilityCeiling() CapabilitySet {
	tools := n.Tools.All()
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Def.Name)
	}
	regs := n.Types.All()
	types := make([]object.Type, 0, len(regs))
	for _, reg := range regs {
		types = append(types, reg.Type)
	}
	return CeilingFromDeclarations(types, toolNames)
}

// Policy returns the compiled policy, or nil before assembly. It is core navi:
// a portal never sees it, and asks the navi instead (ADR 0021, "Consequences";
// ADR 0022 decision 8).
func (n *Navi) Policy() *Policy { return n.policy }

// validateRole checks a declared role.
func validateRole(r Role) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: role has an empty name", ErrInvalidRole)
	}
	if err := r.Holds.Validate(); err != nil {
		return err
	}
	return r.Grants.Validate()
}

// validateGroup checks a declared group.
func validateGroup(g *Group) error {
	if g == nil {
		return fmt.Errorf("%w: nil group", ErrInvalid)
	}
	if err := g.Ref().Validate(); err != nil {
		return err
	}
	return g.Grants().Validate()
}
