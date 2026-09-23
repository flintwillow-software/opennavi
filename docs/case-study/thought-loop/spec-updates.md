# Spec Updates

> Proposed new or changed specs required to execute the `thought-loop` case study. Reviewed and approved by a human before any spec tree is edited.
>
> Framing (operator, 2026-09-23): the **runtime is the house** — whether it runs as a daemon or as the plain binary, the house is the same: the thought queue is its main loop. The **doors** are how work enters and leaves: the TUI (human portal), connectors (gateway, cron, bot adapters, CLI enqueue), and tools (how the navi itself reaches out). The doors feed the same queue; no door bypasses the loop.

| Spec Code | Repo | File | Type | Why |
|-----------|------|------|------|-----|
| S010003 | repos/opennavi | docs/specs/01-agent-runtime/00-thought-queue.md | modified (append + revise §1.1) | The spec currently states two complementary execution models (queue = daemon-only; one-off = simple processing). The case study's central demand — one house, one queue, many doors — contradicts this; the execution-model statement must be rewritten and the in-process queue semantics (binary/interactive runs) spec'd |
| S010104 | repos/opennavi | docs/specs/01-agent-runtime/01-workers-navi-comm.md | modified (append + revise §6.1 table) | The interface→execution-model mapping table routes `[cli] only` navis to "simple processing, process pending and exit" — a binary-run house still runs the queue with a foreground slot; the navi-run sketch needs the portal consumer added |
| S000406 | repos/opennavi | docs/specs/00-navi-core/04-interface-set.md | added (subfeature of S000400) | The TUI is currently spec'd as a chat REPL ("user types prompts, agent responds"). The portal model — queue view as home, per-thought actions, hop into the running thought to monitor + steer — has no spec |
| S000407 | repos/opennavi | docs/specs/00-navi-core/04-interface-set.md | added (subfeature of S000400) | Remote portal attachment: the gateway is spec'd as a webhook receiver only; the event-stream + control-channel surface that lets the same TUI manage a navi running elsewhere does not exist in any spec |
| S010312 | repos/opennavi | docs/specs/01-agent-runtime/03-agent-runtime.md | modified (extend §05) | Agent-level steering exists (S010310); the case study adds queue-level message routing (attach-to-queued → Context append, resume-with-message for paused, delivery-ack events) — the queue-side half is not covered anywhere |

## Per-Update Details

### S010003 (append + revise) — The house and its main loop
- **Repo/file:** repos/opennavi/docs/specs/01-agent-runtime/00-thought-queue.md
- **What:** revise the §1.1 "Two Execution Models" statement (and the SHALL at §00) from daemon-vs-one-off to the **house model**: the navi runtime is the house — whether it runs as an always-on daemon or as the plain binary, the thought queue is the same main loop inside. Doors (TUI, connectors, tools) differ per run mode; the queue does not. New subfeature: **binary-run semantics** — a navi run without connectors drives the same queue in-process with a foreground slot (the interactive session's work is durable thoughts with identical lifecycle, park-with-checkpoint, and recovery), exiting when the session ends and the queue is drained.
- **Why:** the case study's "no daemon-only privilege" constraint and the operator's stated intent; today the spec blesses the bypass path the design removes.

### S010104 (append + revise §6.1) — Doors, not forks
- **Repo/file:** repos/opennavi/docs/specs/01-agent-runtime/01-workers-navi-comm.md
- **What:** revise the §6.1 interface→execution-model mapping: no interface maps to a separate execution model — every door feeds the same queue (TUI: portal + steering; connectors: enqueue; tools: the navi acting out). The `[cli] only` row becomes "binary run: in-process queue, foreground slot". Append the navi-run sketch showing every door consuming the same queue surface.
- **Why:** keeps the two runtime specs consistent; the mapping table is the statement of record for how each door enters the house.

### S000406 (added) — TUI as thought-queue portal
- **Repo/file:** repos/opennavi/docs/specs/00-navi-core/04-interface-set.md
- **What:** replace/extend the CLI (Interactive TUI) section's UX model: queue view as home (statuses, priorities, checkpoints), per-thought actions (attach message, bump priority, pause/resume/cancel/retry), live view (hop into the running thought: stream content deltas + tool calls, read-only) and steering (write path). Message-granularity decision recorded: connector messages are thought-per-message; TUI messages attach/steer/schedule.
- **Why:** the portal is the case study's primary human-facing deliverable; today's spec describes a chat REPL.

### S000407 (added) — Remote portal attachment
- **Repo/file:** repos/opennavi/docs/specs/00-navi-core/04-interface-set.md
- **What:** the daemon's gateway gains a portal-attachment surface: an event stream (queue + thought events — the same events a locally-run TUI consumes in-process) and a control channel (attach/steer/pause/resume/cancel/enqueue), so the identical TUI manages a navi running elsewhere. Trust/authorization alignment with S040200 noted as a requirement scenario; wire-protocol details deferred to 05-messaging.
- **Why:** remote management of a running navi is a core case-study demand with no current spec coverage. **Corollary (operator, approved with this proposal):** because doors are just consumers of the same queue events + control surface, future surfaces — a WebUI, a chat bot's web view, whatever — are additional doors, not new execution paths. "Implementing a WebUI" becomes "slap a connector on the door surface".

### S010312 (extend) — Queue-level message routing
- **Repo/file:** repos/opennavi/docs/specs/01-agent-runtime/03-agent-runtime.md
- **What:** extend §05 (Steering and Follow-up Queues) with the queue-side half: messages routed by thought ID; queued-thought messages append to thought `Context`; paused-thought messages resume with the message attached; each delivery emits a queue-level ack event consumable by the TUI. Agent-level mechanics (S010310) unchanged.
- **Why:** "add a message to a thought" must be observable at the queue layer the TUI talks to, not just inside the agent.

## Explicitly Not Changing

- **ChatSession / council mode** (S010002-family, 04-multi-agent) — unaffected; the portal model does not touch delegation.
- **Thought lifecycle, slots, preemption, checkpoints** (S010001/010002) — already built and spec'd; the foreground door reuses them.
- **Session management** (03-session-context) — per-thought session logs already cover interactive thoughts' history needs.
- **Messaging wire protocol** (05-messaging) — the portal rides the gateway; the protocol feature is authored when the messaging epic activates, not now.