package navi

import (
	"fmt"
	"strings"

	"github.com/flintwillow-software/opennavi/tool"
)

// Mode is a named capability profile selected per thought (ADR 0007). A
// mode is a *restriction*: it selects a subset of the declared tools and
// a model, and it may narrow permissions. It can never widen anything
// the navi compiled — a mode that tries is rejected at Run (ADR 0006).
type Mode struct {
	// Name is the mode identifier a thought selects.
	Name string
	// Tools is the active tool subset by name. Empty = every declared
	// tool (subject to Permissions).
	Tools []string
	// Permissions narrows the navi's compiled ceiling for this mode.
	Permissions PermissionPolicy
	// Model optionally pins the model (ADR 0009); empty = the navi's
	// default.
	Model string
	// Prompt is an optional fragment appended to the system prompt for
	// thoughts in this mode.
	Prompt string
}

// PermissionPolicy is a tool-surface restriction. The same type serves
// both roles it has in the design: the navi's compiled *ceiling* (ADR
// 0006 — what the binary is capable of at all) and a mode's narrower
// policy. Run checks that every mode stays inside the ceiling.
//
// The zero value means "no restriction from this layer", which is what
// makes the ceiling composable: a mode that declares nothing inherits
// the ceiling rather than fighting it.
type PermissionPolicy struct {
	// MaxAccess is the highest tool access level permitted
	// (tool.AccessAny = no restriction from this layer).
	MaxAccess tool.ToolAccess
	// AllowTools, when non-empty, restricts the surface to exactly
	// these names.
	AllowTools []string
	// DenyTools removes these names. Deny wins over allow at every
	// layer — a mode can always deny more, never less.
	DenyTools []string
}

// effectiveMax resolves the policy's access ceiling: the zero value is
// not "no access", it is "no restriction", which is equivalent to
// permitting the most privileged level.
func (p PermissionPolicy) effectiveMax() tool.ToolAccess {
	if p.MaxAccess == tool.AccessAny {
		return tool.AccessExecute
	}
	return p.MaxAccess
}

// Exceeds reports whether p grants anything the ceiling does not, and
// returns an error naming the first violation. Two independent checks,
// because a tool surface can be widened by access level *or* by name:
//
//   - access: p's ceiling must not be higher than the ceiling's own.
//   - naming: a tool p explicitly allows must not be excluded or denied
//     by the ceiling.
//
// Narrowing by omission is always allowed: p may allow fewer tools than
// the ceiling and deny more of them. That asymmetry is the point — the
// ceiling is enforced globally, so anything p leaves out is simply not
// reachable.
func (p PermissionPolicy) Exceeds(ceiling PermissionPolicy) error {
	if p.effectiveMax() > ceiling.effectiveMax() {
		return fmt.Errorf(
			"permission ceiling: mode permits access %q but the ceiling allows at most %q",
			p.effectiveMax(), ceiling.effectiveMax())
	}
	if len(p.AllowTools) == 0 {
		return nil
	}
	ceilingAllows := setOf(ceiling.AllowTools)
	ceilingDenies := setOf(ceiling.DenyTools)
	for _, name := range p.AllowTools {
		if len(ceilingAllows) > 0 {
			if _, ok := ceilingAllows[name]; !ok {
				return fmt.Errorf(
					"permission ceiling: mode permits tool %q which the ceiling's allow-list excludes",
					name)
			}
		}
		if _, denied := ceilingDenies[name]; denied {
			return fmt.Errorf(
				"permission ceiling: mode permits tool %q which the ceiling denies",
				name)
		}
	}
	return nil
}

// Permits reports whether a tool call at the given access level is
// permitted by this policy alone.
func (p PermissionPolicy) Permits(access tool.ToolAccess) bool {
	return access <= p.effectiveMax()
}

// PermitsTool reports whether the named tool is permitted by this
// policy alone (deny wins, then the allow-list if one is set).
func (p PermissionPolicy) PermitsTool(name string) bool {
	if _, denied := setOf(p.DenyTools)[name]; denied {
		return false
	}
	if len(p.AllowTools) == 0 {
		return true
	}
	_, ok := setOf(p.AllowTools)[name]
	return ok
}

// String renders a policy for diagnostics.
func (p PermissionPolicy) String() string {
	parts := []string{fmt.Sprintf("max=%s", p.effectiveMax())}
	if len(p.AllowTools) > 0 {
		parts = append(parts, "allow=["+strings.Join(p.AllowTools, ",")+"]")
	}
	if len(p.DenyTools) > 0 {
		parts = append(parts, "deny=["+strings.Join(p.DenyTools, ",")+"]")
	}
	return strings.Join(parts, " ")
}

func setOf(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}
