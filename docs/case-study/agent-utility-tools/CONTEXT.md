---
status: draft
created: '2026-09-12'
updated: '2026-09-12'
tags:
  - case-study
  - tool-catalog
  - agent-utility-tools
priority: medium
---

# Context

## Problem

Navis today can read files, run shell commands, and inspect git — but they lack the four affordances every production agent relies on:

1. **Progress tracking** — long, multi-step work drifts without a durable task list the agent can write down, update, and re-read across turns and restarts. Humans watching a navi also have no way to see what it is mid-way through.
2. **Wall-clock awareness** — agents have no reliable current-time source. Cron jobs, scheduling, deadlines, and "check again tomorrow" reasoning all silently break or hallucinate timestamps.
3. **Persistent scratchpad** — agents accumulate facts worth keeping verbatim (URLs, credentials *references*, decisions, environment quirks). The semantic memory system (06-memory) is for retrieval-by-meaning, not for a faithful verbatim notebook.
4. **Outbound API access** — no way to call an external HTTP API (GitHub, Plane, weather, internal services). Author tools like `github_issue_read` are *planned* namespace examples (S130400 §04) but there is no primitive to build them on.

## Stakeholders

- **Navi authors** — compose binaries; need these tools selectable per navi definition like the rest of the catalog.
- **Agents themselves** — the primary users of the tools.
- **End users** — benefit from visible todo state (what is my navi working on?) and agent-initiated API actions.
- **Author-tool developers** — `http_request` is the substrate for domain tools (`github_*`, `plane_*`, …) before dedicated namespaces exist.

## Constraints

- **CGO_ENABLED=0** project-wide — pure Go only; persistence goes through the existing store layer (modernc SQLite), no new drivers.
- Tools are **compiled into the binary** (S000300); inclusion is decided by the navi definition; unused tools are compiled out.
- Every tool declares the complete **`ToolDefinition` metadata** (S130400): name, description, inputSchema, outputFormat, access, timeout, recovery, mode, namespace, tags, prompt metadata, provenance (`builtin`).
- Access levels (**read-only / write / execute**) are enforced by the IAM permission model (S040200) — no tool bypasses policy.
- **Secrets must never enter LLM context** — `http_request` authentication resolves server-side via the credential resolver (S100400); only references appear in tool arguments.
- **SSRF surface**: an outbound HTTP tool is a new exfiltration vector; private-range and redirect policy must be constrained by default.
- Error recovery follows S010400 — failed calls return error *and* partial output where meaningful.

## Out of Scope

- Shell variants (`shell_zsh_exec`, `shell_fish_exec`) — already slated separately in the 0.1.0 change log
- Web crawling / search-engine queries (distinct from single-URL `http_request`)
- Image generation, speech, or other provider-backed media tools — better as author tools
- The semantic/vector knowledge base (06-memory) — notes are verbatim, memory is semantic; they complement, not replace
- Marketplace sharing of todo/note state between navis (S000600 extension contract territory)
- Webhook *inbound* handling — already covered by S100500