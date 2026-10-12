# navi-code

Starter template for a **CLI coding agent**: an interactive terminal navi with
tools for working in a repository.

This is a real out-of-repo-style navi module — it imports the public
`github.com/flintwillow-software/opennavi` module and composes a navi through
`navi.New`, declaring its skill, tools, model, and portal in Go. There is no
YAML definition file and no separate build step: `go build` produces the whole
binary.

## Layout

```
navi-code/
├── cmd/navi-code/
│   ├── main.go                  # the navi definition and entry point
│   └── coding-conventions.md    # compiled-in skill (go:embed)
├── go.mod                       # nested module, pinned to the published SDK
└── README.md
```

## What the navi declares

Everything in `cmd/navi-code/main.go` is the compiled-in definition:

- **Identity** — name, version, and personality (`navi.WithPersonality`).
- **Skill** — `coding_conventions`, embedded from `coding-conventions.md`.
- **Tools** — the builtins (`file_read`, `shell_exec`) bound to the working
  directory, plus one author tool (`repo_stats`). `shell_exec` is compiled in
  but denied by the permission ceiling (`navi.WithPermissions`), so it is never
  bound into a thought's surface.
- **Connector** — GitHub, **both halves**. *Egress* supplies
  `github_issue_comment` (ADR 0014, ADR 0020): an ordinary `tool.Tool` handed
  over by `gh.SendTools()` and added explicitly, so this file is the whole story
  of what navi-code can say outward. *Ingress* (ADR 0023) is an opt-in poll
  subscription that turns a comment mentioning you into a thought; set
  `NAVI_GITHUB_USERNAME` to enable it (see [GitHub ingress](#github-ingress)).
- **Model** — one Ollama provider marked default (ADR 0009).
- **Portal** — the TUI.

## GitHub egress

`github_issue_comment` lets the agent reply on the issue or pull request it was
working on. Two settings, neither compiled into the binary:

| Setting | Where | Default |
|---|---|---|
| The token | the environment variable `GITHUB_TOKEN` — a *reference*, resolved at the moment of each call and never stored (ADR 0008) — else the developer's own **`gh auth login`** | unset, then `gh` |
| The repository | `NAVI_CODE_GITHUB_REPO`, else the working directory's `origin` remote | derived from git |

That order is deliberate: an exported token is the more explicit configuration,
and `gh` is the convenience so a local run needs no export. It also matches
`gh`'s own precedence, where `GH_TOKEN`/`GITHUB_TOKEN` win over stored
credentials.

**The `gh` fallback shells out to `gh auth token` rather than reading
`~/.config/gh/hosts.yml`.** Since gh 2.26 the token is kept in the system
keyring by default (Keychain, wincred, Secret Service) and `hosts.yml` retains
only host information, so parsing that file would silently find nothing for
most users. The source caches what it reads for five minutes, so a re-login or
a revoked token recovers without restarting navi-code.

Treat the `gh` fallback as a development convenience: it acts as **your**
GitHub identity rather than a scoped service one. Prefer an explicit reference
in a deployment.

The credential arrives through a `TokenSource` (ADR 0020), not a bare string, so
a credential that has to be **minted and refreshed** is expressible. There are
two shapes, and which to choose is a deployment decision:

| Shape | Source | Choose it when |
|---|---|---|
| A static token | `StaticToken{Ref: "GITHUB_TOKEN"}`, with `GHCLIToken` as the local fallback — this template's default | CI mints a token per run, or a person runs it locally. The token is long-lived and broad, and acts as whoever minted it. |
| A GitHub App installation | `&AppInstallation{...}` | The navi is a **long-lived service**. The token is minted on demand, expires in an hour, and is scoped to one installation rather than to a person. |

This template ships the static shape, because a starter should run with the
credential a developer already has. For a deployment, the App is the
production-shaped option — GitHub's own guidance is to use an App rather than a
personal access token for "long-lived integrations". Swapping it in is one
field:

```go
gh := github.New(github.Config{
    Token: &github.AppInstallation{
        AppID:          123456,                     // the app's "App ID"
        InstallationID: 987654,                     // the installation to act as
        KeyRef:         "/etc/navi/github-app.pem", // the PEM, or an env var name
    },
    Repository: githubRepository(),
})
```

The app's **private key is the real secret** and outlives every token it mints,
so it is a reference (ADR 0008) — an environment variable holding the PEM, else
a file path — and never a value in `main.go`. The source mints on first use,
re-mints before the hour is up and again if GitHub answers 401 (an installation
token can be revoked early), and single-flights the mint so concurrent calls do
not each exchange. The installation ID is **declared rather than discovered**:
an app may be installed on several accounts, so `GET /app/installations` would
only move the choice into a config rule and make "the first one" a silent
wrong-target bug.

The connector is declared **at build time** regardless of configuration, so the
capability's presence is a property of `main.go` rather than of the operator's
environment (ADR 0014 decision 4). What an unset token disables is the
**capability, not the navi**: `Start` logs one warning and each egress call
refuses with an error naming `GITHUB_TOKEN`, so navi-code still reads files and
runs tests. Delete the connector block if you want it out of the binary
entirely — that is the point of adding it explicitly.

To try it, run navi-code in a repository with an `origin` remote:

```
just build-navi-code && ../../var/bin/navi-code
```

No credential setup is needed if you have run `gh auth login` — that is the
fallback. Export `GITHUB_TOKEN` instead (with `repo` scope, or fine-grained
`issues:write`) to use an explicit token, which takes precedence:

```
export GITHUB_TOKEN=<a token with repo scope>
```

then ask it to comment on an issue. **It cannot read or search issues** — only
post comments — so give it the issue number and repository explicitly. A call
before the connector is serving (or after it stops) fails with `connector: not
started` rather than panicking: the gate is built into the tool, not left to
the handler.

## GitHub ingress

Set `NAVI_GITHUB_USERNAME` to your GitHub login (without the `@`) and navi-code
polls the repository this navi is checked out in for comments that mention you,
turning each into a thought on the queue. It is **off by default** — unset the
variable and no poll loop runs, so the starter does nothing surprising.

| Setting | Where | Default |
|---|---|---|
| The login to watch for | `NAVI_GITHUB_USERNAME` | unset — ingress disabled |
| The repository to poll | `NAVI_CODE_GITHUB_REPO`, else the working directory's `origin` remote | derived from git |
| The poll interval | compiled in | 30s |

```
export NAVI_GITHUB_USERNAME=<your-github-login>
just build-navi-code && ../../var/bin/navi-code
```

Comment `@<your-github-login> take a look` on an issue in that repository.
Within the poll interval a thought appears on the queue with metadata
`connector=github`, `event=issue_mention`, `event_id=comment-<id>`, and
`principal=navi:navi-code`. Ingress uses the same client and credential as
egress, so a `gh auth login` or an exported `GITHUB_TOKEN` covers both.

Two properties worth knowing (ADR 0023): the subscription is a **build-time
declaration** — runtime config cannot add one, and the login is the only runtime
input — and a **redelivered event does not spawn a second thought**, deduped on
the comment id. This template keeps the dedupe set in memory, so a restart may
replay a redelivery; a deployment wires `github.WithStateStore(db)` for a
durable one.

Ingress is **model-independent**: the thought is enqueued whether or not a
provider is reachable, though the worker needs one to run it. `principal` is the
service principal the thought acts as (ADR 0021 decision 10); it is a recorded
string until the principal model lands, at which point it becomes a real
principal.

Deployment-time settings (provider endpoints, credential references) come from
the runtime config file named by `NAVI_CONFIG`, if set — never from the
compiled definition.

## Build & run

From the SDK checkout, one command builds this template into `var/bin/navi-code`:

```
just build-navi-code
```

It creates the workspace described below if it is missing, so it works from a
clean tree. `just templates` builds, vets, and tests all three starter
templates.

### Building it yourself

```
go build ./cmd/navi-code
```

That works once `github.com/flintwillow-software/opennavi` is published. It is
not yet — **the repository has no version tags**, so `v0.1.0` cannot be fetched
and a bare build fails with `missing go.sum entry`. Building against a checkout
needs a workspace, and the workspace needs **both** halves: `use` alone still
tries to fetch `v0.1.0` and fails with `unknown revision v0.1.0`.

From the SDK checkout (this is what CI does):

```
cd /path/to/opennavi
cat > go.work <<'EOF'
go 1.26

use (
	.
	./templates/navi-code
)

replace github.com/flintwillow-software/opennavi v0.1.0 => .
EOF

cd templates/navi-code
go build ./cmd/navi-code && ./navi-code
```

Never add a `replace` directive to this template's `go.mod`: a template that
ships one does not build for anyone else. The workspace is the place for it, and
`go.work` is gitignored in the SDK repo so it is never committed from there.

If you have the template checked out on its own, put the workspace beside it
instead — `use (. ../..)` with `replace ... => ../..`. One caveat: `go work init`
refuses when a `go.work` already exists in a parent directory, so a workspace at
the SDK root and one in the template are alternatives, not both.