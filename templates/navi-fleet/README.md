# navi-fleet

The **full-fat testing platform**: every connector the workspace runs on,
composed over one SQLite datastore, so the connector implementation *and* the
navi-onboarding path can be exercised end to end.

Where [`navi-code`](../navi-code) declares a single forge connector and
[`navi-sqlite`](../navi-sqlite) declares a datastore, `navi-fleet` declares the
whole set — **Huly** (egress + inbound), **Forgejo** (egress), and **GitHub**
(egress + opt-in inbound) — so it is the worked example of a navi that acts
across services *and* hears from them. It is the reference for the connector
implementation and for onboarding a new navi.

**Production navis live in their own repositories** (one per navi), copied from
this template and pinning the SDK as a dependency — not deployed from the SDK
repo. That is deliberate: it is what makes the onboarding path testable (see
[Onboarding a new navi](#onboarding-a-new-navi-into-its-own-repo)).

It is a real out-of-repo-style navi module: it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`. There is no YAML definition file and no separate build step:
`go build` produces the whole binary.

## Layout

```
navi-fleet/
├── cmd/navi-fleet/
│   ├── main.go      # the navi definition and entry point
│   └── main_test.go # composition + ingress guards
├── go.mod           # nested module, pinned to the published SDK
└── README.md
```

## What the navi declares

- **Identity** — name (`NAVI_NAME`, default `navi-fleet`), version, personality.
- **Datastore** — the `sqlite` backend, marked default for this binary. One file
  (`NAVI_DB`, default `var/navi-fleet.db`) backs the queue, transcripts, memory,
  and tool state — **and the connectors' persisted inbound seen-sets**, which
  share the same `*sql.DB` so a redelivery is suppressed across a restart
  (ADR 0023 decision 5).
- **Tools** — the builtins (`file_read`, `shell_exec`) bound to the working
  directory, the stateful `todo` tool, and every connector's egress tools added
  **explicitly** via `connector.SendTool` (ADR 0020) — never auto-registered, so
  `main.go` is the whole story of what the navi can reach and say.
- **Connectors**
  - **Huly** — egress (issues, comments, labels, relations, descriptions) plus
    the inbound subscriptions that make *"assign an issue to this navi and it
    picks it up"* work (NAVI-561): `issue_assigned`, `issue_mentioned`,
    `issue_status_changed`, and `comment_on_my_issue`, polled and deduped, with
    the resulting thought acting as the navi's own principal (`navi:<name>`).
  - **Forgejo** — egress (issues, pull requests, comments, labels, repo reads)
    over the Gitea-compatible REST API. Its inbound binding is the shared
    webhook seam (ADR 0029) and lands later.
  - **GitHub** — egress, plus the opt-in mention ingress the `navi-code`
    template established.
- **Model** — Ollama (`NAVI_MODEL`, default `qwen3.8:27b`), marked default.
  Runtime config may retarget the provider/model.
- **Portal** — the TUI (ADR 0002).

## Environment

| Variable | Purpose | Default |
|---|---|---|
| `NAVI_NAME` | navi name (also the `navi:<name>` principal) | `navi-fleet` |
| `NAVI_CONFIG` | runtime config file | unset |
| `NAVI_DB` | SQLite path (queue, memory, seen-sets) | `var/navi-fleet.db` |
| `NAVI_MODEL` | Ollama model | `qwen3.8:27b` |
| `HULY_URL` | Huly instance root | `https://huly.flintwillow.software` |
| `HULY_WORKSPACE` | Huly workspace slug (required for Huly) | unset |
| `HULY_TOKEN` | Huly credential **reference** (ADR 0008) | `HULY_TOKEN` |
| `NAVI_HULY_PROJECT` | Huly project the inbound half watches | `NAVI` |
| `FORGEJO_URL` | Forgejo instance root | `https://git.flintwillow.software` |
| `FORGEJO_TOKEN` | Forgejo credential **reference** | `FORGEJO_TOKEN` |
| `NAVI_FORGEJO_REPO` | default `owner/name` for Forgejo tools | git remote |
| `GITHUB_TOKEN` | GitHub credential reference (falls back to `gh auth`) | `GITHUB_TOKEN` |
| `NAVI_GITHUB_REPO` | default `owner/name` for GitHub tools | git remote |
| `NAVI_GITHUB_USERNAME` | enables GitHub mention ingress when set | unset |

Every credential is a **reference**, never a value: the connector resolves it at
the call site and never stores it (ADR 0008). An unset credential disables that
connector's capability — it does not stop the navi (ADR 0020 decision 7).

## Run

```bash
go build -o navi-fleet ./cmd/navi-fleet
HULY_WORKSPACE=flintwillowsoftware HULY_TOKEN=... ./navi-fleet
```

In-repo (from the repository root), the templates share a workspace:

```bash
just workspace
just build-navi-fleet   # or: (cd templates/navi-fleet && go build ./...)
```

## Onboarding a new navi into its own repo

Each navi on navi-dock lives in its own repository, so its connector set, its
credentials, and its job are reviewable in one place and the SDK is a pinned
dependency. To start one from this template:

1. Create the repo — e.g. `flintwillow-software/navi-<name>`.
2. Copy `cmd/navi-fleet/` to `cmd/navi-<name>/`, rename the module in `go.mod`
   (`module example.com/navi-<name>`), and rename the command.
3. Keep only the connectors that navi needs. The capability surface *is* the
   binary (ADR 0006/0014): removing a connector removes its tools, and no
   prompt can talk the navi into using a tool it does not have.
4. Set `NAVI_NAME` and the credential references. The navi's service principal
   is `navi:<name>`, which is what its inbound work acts as.
5. During development, point at the SDK checkout with a `replace` directive (or
   a `go.work`); drop it once the SDK is tagged and `go.mod` pins a release.
6. Build, run, and verify the onboarding: assign a Huly issue to the navi's
   Huly user and watch it land as a thought.

`navi-fleet` itself is the test bed for that path: it declares every connector,
so a break in the authoring surface (a missing export, a changed option, a
connector that will not compose) fails here before it reaches a navi repo.

## Design notes

- Which services exist, which event kinds are polled, and which tools are on the
  model's surface are **build-time declarations** (ADR 0006/0013/0023). Runtime
  config may *narrow* what a declared connector watches (endpoints, workspace,
  project, repository); it can never add a connector or an event kind.
- A navi is a **user in every service**: its own Huly user, its own Forgejo and
  GitHub credentials. Each connector is per-navi with its own credential.
- The inbound half reuses the connector's egress client and credential
  (ADR 0023 decision 6); there is no second connection.
