# navi-standard

The **reference for what a complete navi looks like**, and a runnable local
playground: no external services, one SQLite file, a local Ollama, every portal.

Where [`navi-sqlite`](../navi-sqlite) declares a datastore,
[`navi-code`](../navi-code) a coding CLI, [`navi-daemon`](../navi-daemon) a
service, and [`navi-fleet`](../navi-fleet) the whole connector set,
`navi-standard` declares **everything the framework offers** in one legible
file — identity, models, an embedder, both memory tiers, modes, the compiled
policy, execution, observability, webhook ingress, the datastore, and the
portals — so it is both the worked example of the authoring surface and the
source a future onboarding CLI will scaffold a new navi repo from.

It is a real out-of-repo-style navi module: it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`. There is no YAML definition file and no separate build step:
`go build` produces the whole binary.

## Layout

```
navi-standard/
├── cmd/navi-standard/
│   ├── main.go      # the navi definition and entry point, one section per concern
│   └── main_test.go # composition, policy, and presentation guards
├── go.mod           # nested module, pinned to the published SDK
└── README.md
```

## Built for two readers

The definition is **sectioned, one concern per block**, because it has two
readers:

- **A person learning the authoring model.** Each section says what the concern
  is *for*, not merely what the call does. The policy section in particular is
  teaching material: every grant carries a comment explaining what it permits
  and why the default is what it is.
- **The onboarding CLI** (future) — a tool that scaffolds a new navi repo from
  this template. Nothing is buried in a helper the tool would have to
  understand to re-emit, and every knob is an environment variable with a
  documented default. Keep it that way.

## What the navi declares

| Concern | Declaration |
|---|---|
| **Identity** | `NAVI_NAME` (default `navi-standard`), version, personality |
| **Datastore** | `sqlite`, one file (`NAVI_DB`), backing the queue, transcripts, both memory tiers, tool state, and the connectors' inbound seen-sets |
| **Models** | Ollama (default, keyless) + OpenRouter (cloud, needs a key) |
| **Embedder** | `model/ollama` with `nomic-embed-text`, so the memory tiers are exercised, not stubbed |
| **Memory** | `WithShortTermMemory`, `WithThoughtBudget`, `WithTokenizer`, per-scope `WithShortTermMemoryBudget` |
| **Modes** | the built-in `plan` and `execute`, plus a declared `research` mode (read-only subset) |
| **Tools** | the builtins, the stateful `todo` list (exposed on the object surface), and every connector egress tool |
| **Connectors** | Huly (egress + inbound), GitHub (egress) |
| **Policy** | the permission ceiling, two roles (`operator`, `reader`), and a group (`navi-peers`) |
| **Execution** | `WithWorkers`, `WithQueueConfig` (durable reconciliation on), `WithPlanCloseCheck` |
| **Observability** | `WithHealthRecorder`, `WithLogger`, `WithDatastoreFlushInterval` |
| **Ingress** | `WithWebhooks` (one shared listener; `NAVI_WEBHOOK_ADDR`) |
| **Portals** | control + gateway (when `NAVI_PORTAL_ADDR` is set), and the in-process TUI on a terminal |

## Environment

Every knob has a sensible default, so a bare `./navi-standard` runs. The
defaults lean in the direction the operator asked for: **read-only on external
systems, read/write on the navi's own aggregates**.

| Variable | Purpose | Default |
|---|---|---|
| `NAVI_NAME` | navi name (also the `navi:<name>` principal) | `navi-standard` |
| `NAVI_VERSION` | reported version | `0.1.0` |
| `NAVI_CONFIG` | runtime config file (deployment layer) | unset |
| `NAVI_DB` | SQLite path (queue, transcripts, memory, tool state, seen-sets) | `var/navi-standard.db` |
| `NAVI_SESSION_KEY` | encrypts transcript bodies at rest | unset (plaintext) |
| `NAVI_LOG_LEVEL` | `debug` · `info` · `warn` · `error` | `info` |
| `NAVI_FLUSH_INTERVAL` | datastore flush cadence (0 = default ~250ms, negative = synchronous) | `0` |
| `NAVI_OLLAMA_MODEL` | Ollama model | `qwen3.8:27b` |
| `NAVI_OPENROUTER_MODEL` | OpenRouter model | `anthropic/claude-sonnet-4` |
| `OPENROUTER_API_KEY` | OpenRouter credential **reference** (ADR 0008) | unset → provider unusable |
| `OLLAMA_HOST` | Ollama base URL (provider and embedder) | `http://localhost:11434` |
| `NAVI_EMBED_MODEL` | embedding model | `nomic-embed-text` |
| `NAVI_EMBED_DIMENSIONS` | embedding vector length | `768` |
| `NAVI_TOKENIZER` | tiktoken encoding, or `heuristic` | `cl100k_base` |
| `NAVI_THOUGHT_BUDGET` | per-thought context budget (tokens) | `16000` |
| `NAVI_STM_SESSION_BUDGET` | short-term memory budget, `session` scope | `2000` |
| `NAVI_STM_USER_BUDGET` | short-term memory budget, `user` scope | `2000` |
| `NAVI_STM_NAVI_BUDGET` | short-term memory budget, `navi` scope | `4000` |
| `NAVI_WORKERS` | concurrent thoughts | `1` |
| `NAVI_QUEUE_CONCURRENCY` | queue scheduler concurrency | `2` |
| `NAVI_QUEUE_RECONCILE` | store-reconcile cadence | `1s` |
| `NAVI_WEBHOOK_ADDR` | shared webhook listener address | unset (no webhook ingress) |
| `NAVI_PORTAL_ADDR` | portal gateway listen address (headless front door) | unset |
| `NAVI_DAEMON` | force the headless presentation even on a TTY | unset (off) |
| `HULY_URL` | Huly instance root | `https://huly.flintwillow.software` |
| `HULY_WORKSPACE` | Huly workspace slug (inbound off when unset) | unset |
| `HULY_TOKEN` | Huly credential **reference** | `HULY_TOKEN` |
| `NAVI_HULY_PROJECT` | Huly project the inbound half watches | `NAVI` |
| `NAVI_HULY_POLL_INTERVAL` | Huly inbound poll cadence | `60s` |
| `GITHUB_TOKEN` | GitHub credential reference (falls back to `gh auth`) | `GITHUB_TOKEN` |
| `NAVI_GITHUB_REPO` | default `owner/name` for GitHub tools | unset |

Every credential is a **reference**, never a value: the connector resolves it at
the call site and never stores it (ADR 0008). An unset credential disables that
connector's capability — it does not stop the navi (ADR 0020 decision 7), which
is why the playground runs keyless.

## Providers, and the startup auth check

Ollama ships as the default because **it is standard for a developer to have a
local instance**, even a small or slow model, and it needs no key — so a keyless
local run works. The other providers the repo has built (OpenRouter) are also
declared, with credential references; they need credentials and do not break a
keyless run.

At startup the navi **checks every declared provider and reports which are
usable**:

```
level=INFO msg="provider usable"   provider=ollama     model=qwen3.8:27b default=true local=true
level=WARN msg="provider unusable" provider=openrouter model=anthropic/claude-sonnet-4 credential=OPENROUTER_API_KEY reason="provider \"openrouter\": credential OPENROUTER_API_KEY is not set (fail-closed — ADR 0008)"
level=INFO msg="provider check complete" usable=1 unusable=1
```

This is `navi.CheckProviders` — an **SDK** capability (NAVI-606), not template
code. Only the composition root can turn a declared `model.Provider` descriptor
into a live adapter: the credential resolution and adapter construction are
internal, so a template cannot reach them, and `model/` stays stdlib-only and
secret-free (ADR 0004). The check applies the same endpoint overrides assembly
applies, carries credential **references** (never values), and treats an
unreachable provider as a report rather than an error — the local run needs only
Ollama. See the SDK changelog for the addition; it landed as its own commit,
separate from this template.

## Permissions

The policy is **compiled** (ADR 0033): the ceiling is derived from the declared
tools and exposed types, and every role and group is checked against it once at
assembly — a grant naming something undeclared fails the run rather than being
discovered per request. `n.Validate()` compiles it, so `main_test.go` asserts
real decisions at composition time rather than reading the declared grant lists.

Two roles and one group ship, as worked examples of every permission the policy
layer supports:

- **`operator`** (principal `user:operator`) — the human at the console. Full
  portal control, every navi aggregate read/write, and **every declared tool by
  name**. Tool grants are built from `n.Tools.All()` rather than hand-written,
  because the ceiling lists tools by name and `call:*` would exceed it.
- **`reader`** (a principal the trust source places in `group:readers`) —
  **read-only on external systems**: it may call the read tools
  (`huly_issue_list`, `huly_issue_search`, `huly_issue_description`,
  `file_read`) and is refused the ones that leave the machine or change state
  (`huly_issue_comment`, `huly_issue_update`, `github_issue_comment`,
  `shell_exec`). This is the default the operator asked for.
- **`navi-peers`** (group) — other navis may read this navi's aggregates and
  call the discovery tools, and may not mutate anything or touch external
  systems. This is the navi-level read/write, external read-only split,
  expressed as a group.

**Group membership is the trust side** (ADR 0021 decision 4) and comes from the
trust source at runtime — a group declares only what its members may *do*,
never who they *are*. The test supplies membership with
`navi.NewResolvedPrincipal(..., groups)`, which is exactly what a trust source
does.

**The permission ceiling** (`navi.WithPermissions`) is `MaxAccess:
AccessExecute`, the outer bound; roles narrow it and can never widen it.

### A known gap: memory tools are runtime-assembled

The short-term and long-term memory tools (`stm_write`, `ltm_*`) are built by
the runtime *after* declaration (`assembleMemory`), so they are not in
`n.Tools` when a role is declared and a grant naming them fails `Validate`'s
referential check. They are held by the navi's own **service principal** by
construction (ADR 0021 decision 10), so a thought the navi runs itself — cron,
connector inbound, the plan-close check — can use them. An **operator chat
session** (which runs as the operator, ADR 0022 decision 3) cannot, and this is
a real limitation. Making the memory tools declarable is tracked as **NAVI-607**.

## Run

The playground, in the foreground with its TUI (no trust store, no daemon —
the TUI is hosted in-process):

```bash
just run-navi-standard
```

That builds the template into `var/bin/`, starts it against a **fresh**
`var/data/navi-standard.db` with a local Ollama, and drops you into the TUI.
Press `n` to queue a thought, `q` to quit. The memory tiers work against the
local embedder — `nomic-embed-text` on the same Ollama.

From inside the template directory, the equivalent is:

```bash
go build -o navi-standard ./cmd/navi-standard
OLLAMA_HOST=http://localhost:11434 ./navi-standard
```

### Headless, as a service would run it

```bash
just run-navi-standard-headless   # starts detached, prints the connect command
just stop-navi-standard           # stop it
```

This variant runs the navi with `NAVI_DAEMON=1` and a portal gateway, and
generates an Ed25519 key plus a development trust store on first run. A
**remote** front-end must authenticate (a signed request as `service:cli`
against a trust store, ADR 0021 decision 6) — which is why the local playground
prefers the in-process TUI, and why this path exists only to exercise the
headless deployment shape.

In-repo (from the repository root), the templates share a workspace:

```bash
just workspace
just build-navi-standard   # or: (cd templates/navi-standard && go build ./...)
just templates             # builds, vets, and tests every template
```

### Building it yourself

The template's `go.mod` requires the published module. The repository has no
version tags yet, so a bare `go build` fails with `missing go.sum entry`.
Never add a `replace` directive to this template's `go.mod` — a template that
ships one does not build for anyone else. Use a `go.work` (what `just workspace`
creates):

```
go work init . ../..
go work use ./templates/navi-standard
go work edit -replace=github.com/flintwillow-software/opennavi@v0.1.0=.
```

## Out of scope, deliberately

- **No peer/P2P code.** ADR 0031 is accepted but **unimplemented** — there is no
  libp2p in `go.mod`, no peer package, no `cmd/navi-relay`. Do not try to
  declare peer features here; there is nothing to declare.
- **`navi-fleet` is not this template.** The operator intends `navi-fleet` to
  become the **P2P team** template (teams of navis, ADR 0031), which is blocked
  on the peer protocol landing. `navi-standard` is the single complete navi;
  `navi-fleet` stays the connector test bed until the peer work exists. That
  intent is recorded here and on the template's Huly issue (NAVI-606).

## Design notes

- Everything in `main.go` is the **static, compiled-in half** (ADR 0006/0013):
  the tool surface, the declared modes, the policy, and the event kinds a
  connector watches. Runtime config may *narrow* what a declared connector
  watches; it can never add a connector or an event kind.
- The policy's tool grants are derived from `n.Tools.All()`, so the operator
  role cannot drift from the declared tool set — but this is also why
  `declarePolicy` must run **after** `declareTools` and `declareConnectors`.
- Connectors are declared **at build time regardless of configuration**, so the
  capability's presence is a property of this file rather than of the operator's
  environment (ADR 0014 decision 4).
