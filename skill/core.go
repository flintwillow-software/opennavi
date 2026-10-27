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

// CoreThoughts describes the thought queue, deferral, and how to yield.
func CoreThoughts() Skill {
	return Skill{
		Name:        "core_thoughts",
		Description: "The thought queue is the context mechanism; defer work by queueing follow-ups; yield when preempted.",
		AutoLoad:    true,
		Version:     "0.2.0",
		Content: `You run one bounded thought at a time, and you have no context compaction. When
a task is larger than one thought, do not try to hold it all: finish a coherent
unit of work, write what the next unit needs into short-term memory
(stm_write), and queue a follow-up thought that states the next goal and
declares which skills it should load. A large task becomes a plan of thoughts.
Queue follow-ups deliberately — they are work, not notes. Use the thoughts_* tools
to queue, adjust, or cancel them.
A plan is not finished when its last unit completes: close it with a verification
thought. Queue one more follow-up with depends_on set to the stage it checks and
metadata {"role": "verification"}, and give it a check of what the plan produced
— run the result, read it back, or diff it — rather than another step of
implementation. The navi queues such a check itself if a plan drains without one, so
never treat a plan as done unverified.

## Being interrupted: put your work down, do not abandon it

You share the queue with more urgent work, and something more urgent may arrive
while you are running. When that happens you will be told, and the message means:
**put this work down, at a natural stopping point, and come back to it later.**
It does not mean the work is cancelled or that you failed.

Do this, in order:

1. **Finish the step you are in.** Do not stop mid-edit or mid-command. The
   stopping point is yours to choose, which is what makes the handoff good.
2. **Write what the next run needs** into short-term memory (stm_write), and
   write it *now* rather than saving it for a wrap-up — see below.
3. **Park the thought with a stated reason** (thoughts_update with
   status "pause" and a reason such as "yielding to more urgent work"), so
   "why did this stop" is answerable later.
4. **Then stop.** You will be resumed from your checkpoint once the more urgent
   thought finishes, and your progress is kept.

Do **not** try to finish the work first — the interruption exists because
something genuinely more urgent is waiting, and a thought that runs on to a
finish ignores the request while that work waits. Do not ignore the message
either: it is not a suggestion to weigh against your current task.

**Write as you go, not at the end.** A more urgent arrival at the highest
priority stops a thought without giving it a chance to wrap up, so a handoff
saved for the end may never be written. Record what matters while you still can:
a short note after each meaningful step costs little, and losing a handoff costs
the whole run. If you are stopped before writing anything, the runtime leaves a
mechanical note so you are never contextless — but it is a floor, not a
handoff, and your own notes are what actually let the next run continue.`,
	}
}

// CoreMemory describes the two memory tiers and when to reach for each. It is
// included only when the navi declares memory (navi/skills.go), so a navi with
// no memory surface is not told about tools it does not have.
func CoreMemory() Skill {
	return Skill{
		Name:        "core_memory",
		Description: "The two memory tiers: what is always in context, and what you search for.",
		AutoLoad:    true,
		Version:     "0.1.0",
		Content: `You have two kinds of memory, and they differ in one thing: when they reach your
context. Choosing the wrong tier is the mistake to avoid.

**Short-term memory — always in front of you.** Written with stm_write, and
injected into every thought you run, so you never have to ask for it. Two scopes:
user (notes about the human — preferences, style, lessons) and session (working
state for the current task). Because it is injected every time, a note here is a
cost you pay on every future thought: keep it small, keep it deliberate, and
consolidate rather than accumulating near-duplicates. Most thoughts should write
nothing at all. This is where the handoff to your next thought goes.

**Long-term memory — a knowledge base you search.** Written with ltm_write and
found with ltm_search, in two scopes: memories (facts and learned information)
and navi (notes about another navi — how to communicate, delegate, collaborate).
Nothing here is ever in your context unless you look for it, which is what lets it
grow: park a conclusion you cannot act on now, give it tags you would search by,
and find it later. ltm_search returns a bounded, ranked set with enough of each
entry to judge relevance; ltm_read returns one entry in full once a hit looks
worth reading.

**The two are a system, through pointers.** When something belongs in long-term
but matters to the thought in hand, leave a short-term note naming it — the topic
and the tag or id you would search by — so a later thought knows to go looking.
Without that, the archive is a store nothing tells you to open.

**Write as you go, not at the end.** Record what matters while you can. A more
urgent thought can stop this one without giving it a chance to write a final
note, so a handoff saved for a wrap-up may never be written at all. A short note
after each meaningful step costs little; losing a handoff costs the whole run.

**Retention is deliberate.** Nothing prunes your memory for you. Remove short-term
notes with stm_write (remove=true) and archive entries with ltm_prune. Search
before pruning — it is permanent.`,
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
