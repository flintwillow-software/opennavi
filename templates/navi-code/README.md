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
- **Connector** — GitHub, whose **egress** half supplies `github_issue_comment`
  (ADR 0014, ADR 0020). This is the worked example of connector-owned egress:
  the tool is an ordinary `tool.Tool` handed over by `gh.SendTools()` and added
  explicitly, so this file is the whole story of what navi-code can say
  outward. The connector's inbound half is not implemented yet (NAVI-394).
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

The credential arrives through a `TokenSource` (ADR 0020), not a bare string, so a
credential that has to be **minted and refreshed** is expressible: a GitHub App
installation signs a JWT with its private key and exchanges it for an
installation access token that expires in an hour. That implementation is not
here yet (NAVI-395) — this template uses the static reference, which is the
right source for a personal access token and for the `GITHUB_TOKEN` a GitHub
Actions workflow mints per run. A GitHub App is the production-shaped option for
a long-lived integration, because its token is short-lived and scoped to one
installation rather than to a person.

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