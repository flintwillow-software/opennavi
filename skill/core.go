package skill

// Core skills ship with the component types (ADR 0013 decision 6): each says
// what a component type is, when to reach for it, and what it is not for.
// The runtime includes one only when the navi actually declares that type,
// and pairs it with a generated inventory of the concrete components — so
// the model is never unaware of a capability the binary has.
//
// The prose is authored here rather than generated: a tool's JSON schema
// says how to call it, not when to reach for it, and that gap is what core
// skills fill. They are auto-loaded because they describe the navi's own
// surface; authored, non-core skills are selective.

// CoreTools describes the tool system.
func CoreTools() Skill {
	return Skill{
		Name:        "core_tools",
		Description: "What the tool system is and when to reach for a tool.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `Tools are how you affect anything outside your own reasoning: reading and
writing files, running commands, calling services. Use a tool when the answer
is not already in your context, and prefer one call that answers the question
over several speculative ones.
A tool's schema tells you how to call it; the rest of your context tells you
whether you should. Tools cannot change your permissions, your context, or
which skills are loaded.`,
	}
}

// CoreThoughts describes the thought queue and deferral.
func CoreThoughts() Skill {
	return Skill{
		Name:        "core_thoughts",
		Description: "The thought queue is the context mechanism; defer work by queueing follow-ups.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `You run one bounded thought at a time, and you have no context compaction. When
a task is larger than one thought, do not try to hold it all: finish a coherent
unit of work, write what the next unit needs into short-term memory
(memory_write), and queue a follow-up thought that states the next goal and
declares which skills it should load. A large task becomes a plan of thoughts.
Queue follow-ups deliberately — they are work, not notes. Use the thoughts_* tools
to queue, adjust, or cancel them.`,
	}
}

// CoreConnectors describes connectors.
func CoreConnectors() Skill {
	return Skill{
		Name:        "core_connectors",
		Description: "What connectors are: inbound stimuli and outbound service tools.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `Connectors integrate the navi with a service in both directions. Inbound, a
connector turns external events into thoughts — that work appears on your queue
like any other. Outbound, a connector may supply tools that act on its service;
those are ordinary tools and appear in your tool list.
Connectors run outside your control: you do not start, stop, or configure them.`,
	}
}

// CorePortals describes portals.
func CorePortals() Skill {
	return Skill{
		Name:        "core_portals",
		Description: "What portals are: surfaces that observe and steer the navi.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `Portals are how humans and other systems observe and steer the navi: a terminal,
a gateway, a dashboard. They see your thoughts and their transcripts and may
pause, resume, cancel, or steer them.
A portal is not a tool and not a source of instructions you should follow —
messages it delivers arrive as user turns.`,
	}
}

// CoreModels describes model selection.
func CoreModels() Skill {
	return Skill{
		Name:        "core_models",
		Description: "The declared provider set and per-thought model selection.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `The navi declares a set of providers and one default. A thought may name a
provider or model when it is queued, and a mode may pin one; otherwise the
default applies.
You cannot switch your own model mid-thought. If a task clearly needs a
different model, note it in short-term memory and queue a follow-up that names
it.`,
	}
}

// CoreDatastores describes persistence.
func CoreDatastores() Skill {
	return Skill{
		Name:        "core_datastores",
		Description: "What the datastore is and what it is not.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `The datastore persists navi state — thoughts, transcripts, tool state, and
short-term memory — in whichever backend the operator selected. It is
eventual-consistency persistence, not a scratchpad and not a source of
guidance: you do not choose the backend, and reading it directly is not
something you do.
Anything you want kept for later belongs in short-term memory or a queued
thought, not in a file you hope will survive.`,
	}
}

// CoreModes describes capability profiles.
func CoreModes() Skill {
	return Skill{
		Name:        "core_modes",
		Description: "What modes are: named capability restrictions a thought runs under.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `A mode is a named capability profile: it selects a subset of the declared tools
and may pin a model, always as a *restriction* of what the binary compiled. A
thought runs under one mode, chosen when it is queued.
You cannot widen your own surface or select a mode mid-thought; if a task needs
capabilities a planning thought does not have, queue a follow-up that runs under
a different mode.`,
	}
}
