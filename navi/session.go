package navi

import (
	"fmt"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the session policy of ADR 0022 decision 4 and ADR 0026: the mode
// chosen when a thought is enqueued narrows what that thought's calls may do.
// It can never widen, because the engine requires every layer — the ceiling,
// the principal's roles and groups, and the session — to permit (policy.go).
//
// A session is a NARROWING layer, not a grant, so it is expressed as wildcard
// allows plus a deny-list: CapabilitySet.Permits is allow-required, so a set of
// bare denials would deny everything. It is never compared with the compiled
// ceiling, because it grants nothing; the ceiling and the roles are the layers
// that grant.

// modeSession derives the session policy a mode imposes on a thought's calls.
// It denies every declared tool the mode's access cap, deny-list, or tool
// subset excludes, and — when the mode is capped at read-only — every object
// mutation (`act:*`), because read-only means no mutation either. `enqueue`
// stays permitted: scheduling a follow-up is how a planning thought continues,
// not a mutation.
func (n *Navi) modeSession(m Mode) *CapabilitySet {
	session := &CapabilitySet{Allow: []Grant{
		{Capability: Any(VerbCall)},
		{Capability: Any(VerbRead)},
		{Capability: Any(VerbAct)},
		{Capability: Connect()},
		{Capability: Enqueue()},
	}}
	if m.Permissions.effectiveMax() <= tool.AccessReadOnly {
		session.Deny = append(session.Deny, Grant{Capability: Any(VerbAct)})
	}
	requested := setOf(m.Tools)
	for _, declared := range n.Tools.All() {
		name, access := declared.Def.Name, declared.Def.Access
		_, inSubset := requested[name]
		excluded := len(requested) > 0 && !inSubset
		if !excluded {
			excluded = !m.Permissions.PermitsTool(name) || !m.Permissions.Permits(access)
		}
		if excluded {
			session.Deny = append(session.Deny, Grant{Capability: Call(name)})
		}
	}
	return session
}

// sessionFor returns the session policy for a thought's mode, or nil when the
// thought names no mode — the navi default, which narrows nothing. An unknown
// mode yields a deny-all session rather than nil: the thought fails at
// resolveForMode before a call is made, and until then the failure direction is
// closed.
func (n *Navi) sessionFor(t *thought.Thought) *CapabilitySet {
	if t == nil || t.Mode == "" {
		return nil
	}
	m, ok := n.Modes.Get(t.Mode)
	if !ok {
		return &CapabilitySet{Deny: []Grant{
			{Capability: Any(VerbCall)},
			{Capability: Any(VerbRead)},
			{Capability: Any(VerbAct)},
			{Capability: Connect()},
			{Capability: Enqueue()},
		}}
	}
	return n.modeSession(m)
}

// validateModeSelection enforces ADR 0026 decision 3 at the door: a principal
// may request a mode whose permission policy does not exceed its own. Every tool
// the mode permits must be a tool the actor may already call, decided against
// the actor's roles WITHOUT the session — so a more restrictive mode is always
// selectable, and a mode broader than the actor's own policy is refused loudly
// when the thought is enqueued rather than silently narrowed to nothing when it
// runs.
//
// It runs from the queue's assembler, so a refusal means nothing was scheduled.
// Before assembly there is no compiled policy to decide against; the mode's
// shape is still checked by validateModes at Run, and an unknown mode still
// fails the thought at resolveForMode.
func (n *Navi) validateModeSelection(t *thought.Thought) error {
	if n.policy == nil {
		return nil
	}
	m, ok := n.Modes.Get(t.Mode)
	if !ok {
		return nil
	}
	actor := n.originActor(t)
	requested := setOf(m.Tools)
	for _, declared := range n.Tools.All() {
		name, access := declared.Def.Name, declared.Def.Access
		if len(requested) > 0 {
			if _, ok := requested[name]; !ok {
				continue
			}
		}
		if !m.Permissions.PermitsTool(name) || !m.Permissions.Permits(access) {
			continue
		}
		if !n.permitsCapability(actor, Call(name)) {
			return fmt.Errorf("%w: thought %s: mode %q permits %s, which %s may not do",
				portal.ErrUnauthorized, t.ID, t.Mode, Call(name), actor.Who)
		}
	}
	return nil
}
