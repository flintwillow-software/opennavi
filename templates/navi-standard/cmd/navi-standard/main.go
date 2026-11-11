// Command navi-standard is the reference for what a COMPLETE navi looks like:
// every option the framework offers, declared in one legible file, over one
// SQLite file and a local Ollama. It runs as a local playground with no
// external services, and it is the source a future onboarding CLI will
// scaffold a new navi repo from.
//
// Two readers are served by the shape of this file. The first is a person
// learning the authoring model: each section below is one concern, and the
// comments say what the concern is FOR, not merely what the call does. The
// second is the onboarding tool: the definition is deliberately flat and
// sectioned — one concern per block, every knob an env var with a documented
// default, nothing buried in a helper the tool would have to understand to
// re-emit. Keep it that way.
//
// Everything here is the STATIC, compiled-in half (ADR 0006/0013): the tool
// surface, the declared modes, the policy, and the event kinds a connector
// watches. Deployment settings — endpoints, credential references, database
// path — come from the environment and the config file named by NAVI_CONFIG.
//
// This template deliberately does NOT declare peer/P2P features: ADR 0031 is
// accepted but unimplemented (no libp2p, no peer package, no navi-relay), so
// there is nothing to declare. The navi-fleet template is the connector test
// bed today; it becomes the P2P TEAM template (teams of navis, ADR 0031) once
// the peer protocol lands, and navi-standard stays the single complete navi.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/connector/github"
	"github.com/flintwillow-software/opennavi/connector/huly"
	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/model/ollama"
	"github.com/flintwillow-software/opennavi/model/tokenizer"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "navi-standard:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	n, store, err := compose()
	if err != nil {
		return err
	}
	defer store.Close()

	// Provider authentication check (NAVI-606): report which declared
	// providers this navi can actually reach and authenticate BEFORE it
	// accepts work. A navi that silently fails a provider at the first
	// thought is worse than one that says at startup which ones it can use.
	// An unreachable provider is not fatal — the local run needs only Ollama,
	// and the cloud providers are expected to be unusable without a key.
	reportProviders(ctx, n)

	return n.Run(ctx)
}

// ── composition ───────────────────────────────────────────────────────────
//
// compose builds the navi and opens its datastore. It is separate from run so
// a test can validate the whole composition without serving it.

func compose() (*navi.Navi, *sqlite.Store, error) {
	// The datastore is opened here rather than in build because the store's
	// *sql.DB is shared: the connectors' inbound seen-sets live in the same
	// file as the queue, transcripts, memory, and tool state (ADR 0023
	// decision 5), so a redelivery is suppressed across a restart.
	store, dbPath, err := openDatastore()
	if err != nil {
		return nil, nil, err
	}

	n, err := build(dbPath)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return n, store, nil
}

// build declares every section of the navi. It takes the datastore path so the
// datastore candidate can be declared here, where the rest of the definition
// lives.
func build(dbPath string) (*navi.Navi, error) {
	n, err := declareIdentity()
	if err != nil {
		return nil, err
	}
	if err := declareDatastore(n, dbPath); err != nil {
		return nil, err
	}
	if err := declareModels(n); err != nil {
		return nil, err
	}
	if err := declareTools(n); err != nil {
		return nil, err
	}
	if err := declareConnectors(n); err != nil {
		return nil, err
	}
	if err := declareModes(n); err != nil {
		return nil, err
	}
	if err := declarePolicy(n); err != nil {
		return nil, err
	}
	if err := declarePortals(n); err != nil {
		return nil, err
	}
	return n, nil
}

// ── identity ──────────────────────────────────────────────────────────────

// declareIdentity sets the navi's name, version, personality, and the
// cross-cutting options: logging, memory, the embedder, the tokenizer, and the
// execution shape. These are options on navi.New rather than declarations in a
// collection because they configure DEFAULTS rather than items.
func declareIdentity() (*navi.Navi, error) {
	opts := []navi.Option{
		navi.WithVersion(envOr("NAVI_VERSION", "0.1.0")),
		navi.WithPersonality(personality),

		// ── observability ────────────────────────────────────────────────
		// A structured logger handed to every component through Deps. The
		// default is slog.Default(); we set a text handler so a local run is
		// readable in a terminal.
		navi.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel(),
		}))),
		// The datastore is RAM-first: writes land in memory and flush on a
		// cadence. This tunes that cadence; 0 selects the default (~250ms),
		// negative is synchronous write-through.
		navi.WithDatastoreFlushInterval(envDuration("NAVI_FLUSH_INTERVAL", 0)),
		// The start-time connector-health view. A local run prints it; a
		// deployment would persist it beside the database.
		navi.WithHealthRecorder(recordHealth),

		// ── memory (ADR 0037, ADR 0039) ──────────────────────────────────
		// Short-term memory is the always-loaded tier injected into every
		// thought. Declaring it requires a datastore (assembly fails without
		// one rather than silently keeping memory volatile).
		navi.WithShortTermMemory(),
		// The per-thought context budget in tokens. Assembly truncates past
		// it and a running thought is told as it approaches it so it can
		// defer. Zero or negative disables both.
		navi.WithThoughtBudget(envInt("NAVI_THOUGHT_BUDGET", navi.DefaultThoughtBudget)),
		// One token counter, shared by the memory budgets, assembly
		// truncation, and the deferral notice, so the three agree on how full
		// a thought is (ADR 0039 decision 3). NAVI_TOKENIZER selects the
		// tiktoken encoding; empty selects cl100k_base; "heuristic" selects
		// the ~4-runes-per-token fallback.
		navi.WithTokenizer(declareTokenizer()),
		// Per-scope short-term memory budgets (ADR 0039 decision 2): a burst
		// of working state cannot consume the room reserved for notes about
		// the human. A scope with no declared budget uses the default.
		navi.WithShortTermMemoryBudget(datastore.ScopeSession, envInt("NAVI_STM_SESSION_BUDGET", 2000)),
		navi.WithShortTermMemoryBudget(datastore.ScopeUser, envInt("NAVI_STM_USER_BUDGET", 2000)),
		navi.WithShortTermMemoryBudget(datastore.ScopeNavi, envInt("NAVI_STM_NAVI_BUDGET", 4000)),

		// ── execution ────────────────────────────────────────────────────
		// How many thoughts run concurrently. A local playground defaults to
		// 1 so output is readable; a daemon would raise it.
		navi.WithWorkers(envInt("NAVI_WORKERS", 1)),
		// The queue's scheduling configuration: concurrency, priority slots,
		// and the reconcile/flush cadence. This navi is DURABLE (SQLite), so
		// reconciliation is enabled — the loop adopts thoughts persisted by
		// another process (a `navi enqueue`, the cron connector) so external
		// doors reach a running navi. Flush stays at the default async
		// cadence: writes must not block a lifecycle transition on the store.
		navi.WithQueueConfig(thought.Config{
			MaxConcurrent:     envInt("NAVI_QUEUE_CONCURRENCY", 2),
			ReconcileInterval: envDuration("NAVI_QUEUE_RECONCILE", time.Second),
			FlushInterval:     0, // 0 selects the ~250ms dirty-set flusher
			SlotExhaustion:    thought.PolicyQueue,
		}),
		// The closing check (ADR 0016): when a multi-stage plan drains with no
		// verification thought behind it, queue one. Defaults on; a navi that
		// must not spend thoughts on checks turns it off.
		navi.WithPlanCloseCheck(true),

		// ── ingress (ADR 0029) ───────────────────────────────────────────
		// ONE shared webhook listener that connectors register routes on. The
		// address is the navi's, declared once — a connector declares a route,
		// never a port. Empty disables webhook ingress entirely (which is the
		// default for a local playground; a connector with a webhook binding
		// fails Start if it needs one and none is declared).
		navi.WithWebhooks(envOr("NAVI_WEBHOOK_ADDR", "")),
	}

	// The embedder makes the long-term memory archive searchable by meaning
	// rather than only by word (ADR 0040 decision 1). It is local — Ollama —
	// so the playground needs no external service. Declaring none is a
	// supported configuration (keyword-only search), not a degraded one.
	opts = append(opts, navi.WithEmbedder(ollama.NewAt(
		ollamaEndpoint(),
		envOr("NAVI_EMBED_MODEL", model.DefaultEmbedModel),
		envInt("NAVI_EMBED_DIMENSIONS", model.DefaultEmbedDimensions),
	)))

	// The runtime config file, if named, is the deployment layer (ADR 0008:
	// credential REFERENCES, never values). Never compiled in.
	if cfg := strings.TrimSpace(os.Getenv("NAVI_CONFIG")); cfg != "" {
		opts = append(opts, navi.WithRuntimeConfig(cfg))
	}

	return navi.New(envOr("NAVI_NAME", "navi-standard"), opts...), nil
}

// personality is the navi's compiled identity. Its CAPABILITY is the tool and
// connector set declared below, not this prose (ADR 0014 decision 5): the
// prompt orients, the declarations constrain.
const personality = "You are navi-standard, a complete reference navi. You keep durable " +
	"notes about the work and the people you work with, act only through the tools you " +
	"have been given, and say plainly when something is outside your reach. Keep answers " +
	"terse and your reasoning visible."

// ── datastore (ADR 0011) ─────────────────────────────────────────────────

// declareDatastore adds the SQLite candidate, marked default for this binary.
// One file backs every aggregate: thoughts and transcripts, memory (both
// tiers), tool state, and the connectors' inbound seen-sets.
func declareDatastore(n *navi.Navi, dbPath string) error {
	candidate := sqlite.CandidateAt(dbPath)
	candidate.Default = true
	return n.Datastores.Add(candidate)
}

// openDatastore opens the SQLite file this navi runs on, creating its parent
// directory. NAVI_DB names the file; NAVI_SESSION_KEY, if set, encrypts
// transcript bodies at rest.
func openDatastore() (*sqlite.Store, string, error) {
	dbPath := envOr("NAVI_DB", filepath.Join("var", "navi-standard.db"))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, "", err
	}
	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return nil, "", err
	}
	store, err := sqlite.Open(dbPath, sqlite.Options{Cipher: cipher})
	if err != nil {
		return nil, "", err
	}
	return store, dbPath, nil
}

// ── models (ADR 0009) ────────────────────────────────────────────────────

// declareModels declares the provider set. Exactly one is default — Ollama,
// because it is standard for a developer to have a local instance and it needs
// no key, so a keyless local run works. The cloud providers are declared too,
// with credential REFERENCES; an unset credential disables that provider's
// capability without stopping the navi (ADR 0020 decision 7), which
// reportProviders makes visible at startup.
//
// Model names are env-overridable so a deployment retargets without a rebuild.
func declareModels(n *navi.Navi) error {
	// Primary: local Ollama, marked default. No credential.
	if err := n.Models.Add(model.Ollama(
		envOr("NAVI_OLLAMA_MODEL", "qwen3.8:27b"),
		model.Default(),
		model.WithEndpoint(ollamaEndpoint()),
	)); err != nil {
		return err
	}

	// Secondary: OpenRouter (cloud). Requires OPENROUTER_API_KEY — a
	// reference, resolved at the call site and never stored (ADR 0008). With
	// no key it is declared but unusable, and reportProviders says so.
	if err := n.Models.Add(model.OpenRouter(
		envOr("NAVI_OPENROUTER_MODEL", "anthropic/claude-sonnet-4"),
		model.WithCredential(model.DefaultOpenRouterCredential),
	)); err != nil {
		return err
	}

	return nil
}

// ── tools ────────────────────────────────────────────────────────────────

// declareTools adds the builtins, the stateful todo list, and every connector
// egress tool. Connector tools are added EXPLICITLY (ADR 0020) — never
// auto-registered — so this file is the whole story of what the navi can
// reach and say.
func declareTools(n *navi.Navi) error {
	// Builtins bound to the working directory. shell_exec is compiled in but
	// the policy below denies it by default (read-only playground); the deny
	// is what keeps it off a thought's surface, not its absence.
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	builtins, err := builtin.Tools(workDir)
	if err != nil {
		return err
	}
	if err := addTools(n, builtins); err != nil {
		return err
	}

	// The todo tool is stateful: it reads and writes the bound datastore's
	// tool-state aggregate, so todos live in the same SQLite file as
	// everything else.
	state := n.Datastores.Handle().Stateful()
	todos, err := todo.Tools("default", state)
	if err != nil {
		return err
	}
	if err := addTools(n, todos); err != nil {
		return err
	}

	// Expose the todo aggregate on the object surface (ADR 0017 decision 8),
	// so a portal renders it through the SAME view the tool writes. The types
	// are not exposed by default because the stateful view is chosen per
	// component at declaration — the author holds the view, so the author
	// registers the type with it. `read:todo` and `act:todo.*` in the policy
	// below are only valid because this runs.
	for _, reg := range navi.ToolStateTypes("default", state) {
		if err := n.Types.Add(reg); err != nil {
			return err
		}
	}
	return nil
}

// ── connectors (ADR 0014, ADR 0020, ADR 0023) ────────────────────────────

// declareConnectors wires the external services this navi acts in. Each is
// declared at BUILD time regardless of configuration, so the capability's
// presence is a property of this file rather than of the operator's
// environment (ADR 0014 decision 4). What an unset credential disables is the
// CAPABILITY, not the navi: Start logs one warning and each call refuses with
// an error naming the variable, so the playground still runs keyless.
func declareConnectors(n *navi.Navi) error {
	if err := addHuly(n); err != nil {
		return err
	}
	return addGitHub(n)
}

// addHuly wires the tracker: egress tools plus the inbound subscriptions that
// make "assign an issue to this navi and it picks it up" work (NAVI-561). The
// inbound half reuses the egress client and credential (ADR 0023 decision 6).
// Inbound is OFF unless HULY_WORKSPACE names a workspace.
func addHuly(n *navi.Navi) error {
	actor, err := principal.New(principal.KindNavi, n.Name())
	if err != nil {
		return err
	}
	cfg := huly.Config{
		URL:        envOr("HULY_URL", defaultHulyURL),
		Workspace:  strings.TrimSpace(os.Getenv("HULY_WORKSPACE")),
		Credential: hulyCredential,
		// The event KINDS are a build-time declaration (ADR 0023 decision 1);
		// the project is a runtime target, but runtime can never add a kind.
		Subscriptions: []huly.Subscription{{
			Name:    "tracker",
			Project: envOr("NAVI_HULY_PROJECT", "NAVI"),
			Events: []huly.EventKind{
				huly.EventIssueAssigned,
				huly.EventIssueMentioned,
				huly.EventIssueStatusChanged,
				huly.EventCommentOnMyIssue,
			},
			// Inbound work acts as the navi itself, not as the connector
			// (ADR 0021 decision 10).
			Principal: actor,
		}},
		PollInterval: envDuration("NAVI_HULY_POLL_INTERVAL", 60*time.Second),
	}
	h := huly.New(cfg)
	if err := n.Connectors.Add(h); err != nil {
		return err
	}
	tools, err := h.SendTools()
	if err != nil {
		return err
	}
	return addTools(n, tools)
}

// addGitHub wires the public forge: egress only (the opt-in mention ingress
// lives in the navi-code and navi-fleet templates; a playground needs no poll
// loop). Its token falls back to `gh auth` so a local run needs no export.
func addGitHub(n *navi.Navi) error {
	gh := github.New(github.Config{
		Token: github.ChainToken(
			github.StaticToken{Ref: githubCredential},
			&github.GHCLIToken{},
		),
		Repository: envOr("NAVI_GITHUB_REPO", ""),
	})
	if err := n.Connectors.Add(gh); err != nil {
		return err
	}
	tools, err := gh.SendTools()
	if err != nil {
		return err
	}
	return addTools(n, tools)
}

// ── modes (ADR 0007, ADR 0006) ───────────────────────────────────────────

// declareModes declares the named capability profiles a thought can select. A
// mode is a RESTRICTION: it selects a subset of the tools and may narrow
// permissions, and it can never widen the compiled ceiling — Run rejects a
// mode that tries (ADR 0006).
//
// Two modes ship: the built-in plan (read-only) and execute (full surface),
// plus a "research" mode that is a worked example of a NARROWED surface. The
// ceiling relationship is intact: research allows a subset of what execute
// allows, and both stay inside the navi's ceiling.
func declareModes(n *navi.Navi) error {
	// The built-in read-only planning profile: write/execute tools are never
	// bound into a planning thought's surface.
	if err := n.Modes.Add(navi.PlanMode); err != nil {
		return err
	}
	// The built-in full-capability profile: the navi's compiled surface under
	// its own ceiling.
	if err := n.Modes.Add(navi.ExecuteMode); err != nil {
		return err
	}
	// A real declared mode: research can read the world but not change it.
	// Its tool allow-list is a SUBSET of the declared set (narrowing by
	// omission is always allowed), and its access ceiling is read-only, which
	// the compiled ceiling permits.
	return n.Modes.Add(navi.Mode{
		Name: "research",
		Tools: []string{
			"file_read",
			"todo_read",
			"huly_issue_list",
			"huly_issue_search",
			"huly_issue_description",
		},
		Permissions: navi.PermissionPolicy{MaxAccess: tool.AccessReadOnly},
		Prompt:      "Research mode: gather and read. Propose changes; do not make them.",
	})
}

// ── policy (ADR 0021, ADR 0022, ADR 0033) ────────────────────────────────

// declarePolicy declares the compiled authorization policy: the permission
// CEILING (what this binary is capable of at all, ADR 0006) and the roles and
// groups that decide what a given principal may do.
//
// The defaults lean in the direction the operator asked for: read-only on
// external systems, read/write on the navi's own aggregates. The ceiling is
// the outer bound; roles narrow it. Every grant below carries a comment saying
// what it is for and why its default is what it is — this section is teaching
// material as much as configuration.
//
// The policy is COMPILED (ADR 0033): the ceiling is derived from the declared
// tools and exposed types, and every role and group is checked against it once
// at assembly. A grant naming an undeclared tool fails the run rather than
// being discovered per request. `n.Validate()` compiles it, so a test can ask
// a real authorization question at composition time.
func declarePolicy(n *navi.Navi) error {
	// ── the permission ceiling (ADR 0006) ────────────────────────────────
	// MaxAccess is the highest tool access level this binary permits at all.
	// AccessExecute admits the builtins (shell_exec is AccessExecute); the
	// DEFAULT policy below is what actually denies it to a thought.
	n.Permissions = navi.PermissionPolicy{
		MaxAccess: tool.AccessExecute,
	}

	// ── the operator role (ADR 0021 decision 5) ──────────────────────────
	// A role is a TRUST policy (who may hold it) plus a PERMISSION policy
	// (what holding it permits). The operator is the human at the console;
	// its grants are the widest, and still bounded by the ceiling.
	//
	// The tool grants are built from n.Tools.All() rather than written out by
	// hand, because the ceiling lists tools BY NAME (ADR 0021 decision 11):
	// `call:*` exceeds it and the navi refuses to start, and a hand-kept list
	// drifts the moment a connector's tool set changes. This is why
	// declarePolicy must run AFTER declareTools and declareConnectors.
	//
	// Note what is NOT here and why: the short-term and long-term memory tools
	// (stm_write, ltm_*) are assembled by the runtime AFTER declaration
	// (navi.assembleMemory), so they are not in n.Tools yet and a grant naming
	// them would fail Validate's referential check. They are held by the
	// navi's own SERVICE principal by construction (ADR 0021 decision 10) — a
	// thought the navi runs itself can use them. See the template README and
	// NAVI-607 for making them declarable so an operator session can too.
	caps := []navi.Capability{
		// Portal attachment and scheduling: the operator drives the navi.
		navi.Connect(),
		navi.Enqueue(),
		// The navi's OWN aggregates are read/write for the operator: these are
		// navi-level systems, and the operator owns them. `read` covers the
		// object surface; `act` covers the actions the type DECLARES (a type
		// with no actions is read-only through the surface — its writes go
		// through tools, which is why memory has no `act`).
		navi.Read("thought"),
		navi.Act("thought", "steer"),
		navi.Act("thought", "pause"),
		navi.Act("thought", "resume"),
		navi.Act("thought", "cancel"),
		navi.Act("thought", "retry"),
		navi.Act("thought", "bump"),
		// The transcript detail is its own type (a thought's entries).
		navi.Read("entry"),
		// Memory and the trust ledger are read-only through the object surface:
		// memory is written through the memory TOOLS, and the trust ledger is
		// the record of what the navi decided.
		navi.Read("memory"),
		navi.Read("trust_event"),
		// Todos are the navi's own aggregate with declared actions.
		navi.Read("todo"),
		navi.Act("todo", "complete"),
		navi.Act("todo", "start"),
		navi.Act("todo", "remove"),
	}
	for _, t := range n.Tools.All() {
		caps = append(caps, navi.Call(t.Def.Name))
	}
	operator, err := navi.NewRole("operator",
		navi.TrustPolicy{
			// The console principal. A deployment would add OIDC claims or a
			// trust-store principal here instead of a compiled one.
			Principals: []principal.Ref{principal.MustParse("user:operator")},
		},
		navi.CapabilitySet{Allow: navi.Allows(caps...)},
	)
	if err != nil {
		return err
	}
	if err := n.Roles.Add(operator); err != nil {
		return err
	}

	// ── the reader role: read-only on EXTERNAL systems ───────────────────
	// A principal that may look at the world but not change it. This is the
	// shape the operator asked for as the default for anything touching an
	// external service: reads are cheap and reversible, writes are not.
	reader, err := navi.NewRole("reader",
		navi.TrustPolicy{
			Groups: []principal.Ref{principal.MustParse("group:readers")},
		},
		navi.CapabilitySet{Allow: navi.Allows(
			navi.Connect(),
			// Reading the navi's own aggregates is fine — it is the navi's
			// state, and reading it changes nothing. NO `act:` grants: a reader
			// looks, and does not mutate, anywhere.
			navi.Read("thought"),
			navi.Read("entry"),
			navi.Read("memory"),
			navi.Read("todo"),
			// External systems: read tools ONLY. No huly_issue_comment, no
			// huly_issue_update, no github_issue_comment, no shell_exec —
			// those are the ones that leave the machine or change state.
			// (The memory tools are runtime-assembled; see the operator role.)
			navi.Call("file_read"),
			navi.Call("todo_read"),

			navi.Call("huly_issue_list"),
			navi.Call("huly_issue_search"),
			navi.Call("huly_issue_description"),
		)},
	)
	if err != nil {
		return err
	}
	if err := n.Roles.Add(reader); err != nil {
		return err
	}

	// ── a group: the permission side of a named collection ───────────────
	// Membership is the TRUST side and comes from the trust source (ADR 0021
	// decision 4) — a group never declares members here, because a compiled
	// membership list would be a second, conflicting answer. A group declares
	// only what its members may DO.
	//
	// This is the "navi peers" group: other navis on the mesh (once ADR 0031
	// lands) may read this navi's aggregates but not touch external systems
	// or mutate anything. It is the read-on-navi-level,
	// read-only-on-external default, expressed as a group.
	peers, err := navi.NewGroup(
		principal.MustParse("group:navi-peers"),
		navi.CapabilitySet{Allow: navi.Allows(
			navi.Connect(),
			navi.Read("thought"),
			navi.Read("entry"),
			navi.Read("memory"),
			navi.Read("todo"),
		)},
	)
	if err != nil {
		return err
	}
	return n.Groups.Add(peers)
}

// ── portals (ADR 0002, ADR 0017, ADR 0022) ───────────────────────────────

// declarePortals wires the presentation. Three portals are declared:
//
//   - control  — the in-process control surface a gateway (or the TUI) needs.
//   - gateway  — the HTTP observation + control surface, declared when
//     NAVI_PORTAL_ADDR is set. This is how a headless navi is driven.
//   - tui      — the interactive terminal, added only when a terminal is
//     actually watching (a pipe, a redirect, or a systemd journal never gets
//     an alt-screen program).
//
// For a LOCAL playground the TUI runs IN-PROCESS: it is the same binary, so it
// is trusted structurally and needs no key material or trust store. That is
// the simplest thing that works, and it is what `just run-navi-standard`
// relies on. A REMOTE front-end must authenticate (a signed request as
// service:cli with an Ed25519 key against a trust store) — that path is not
// needed locally and is deliberately not configured here.
func declarePortals(n *navi.Navi) error {
	addr := strings.TrimSpace(os.Getenv("NAVI_PORTAL_ADDR"))
	if addr != "" {
		control := portal.NewControl(n)
		if err := n.Portals.Add(control); err != nil {
			return err
		}
		if err := n.Portals.Add(gateway.New(addr, control)); err != nil {
			return err
		}
	}
	if !headless() && stdoutIsTTY() {
		if err := n.Portals.Add(tui.NewPortal()); err != nil {
			return err
		}
	}
	return nil
}

// ── provider reporting ───────────────────────────────────────────────────

// reportProviders pings every declared provider and logs which are usable. It
// never fails the run: the local playground needs only Ollama, and the cloud
// providers are expected to be unusable without a key.
func reportProviders(ctx context.Context, n *navi.Navi) {
	statuses, err := n.CheckProviders(ctx)
	if err != nil {
		// A navi with no providers declared cannot run a thought; assembly
		// will say so. Here it is only worth a line.
		slog.Warn("provider check unavailable", "err", err)
		return
	}
	usable, unusable := 0, 0
	for _, s := range statuses {
		if s.Usable {
			usable++
			slog.Info("provider usable", "provider", s.Name, "model", s.Model,
				"default", s.Default, "local", s.Local)
			continue
		}
		unusable++
		slog.Warn("provider unusable", "provider", s.Name, "model", s.Model,
			"credential", s.Credential, "reason", s.Reason)
	}
	slog.Info("provider check complete", "usable", usable, "unusable", unusable)
}

// ── helpers ──────────────────────────────────────────────────────────────

// addTools adds every tool explicitly (ADR 0020).
func addTools(n *navi.Navi, tools []tool.Tool) error {
	for _, t := range tools {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}
	return nil
}

// declareTokenizer selects the token counter from NAVI_TOKENIZER: a tiktoken
// encoding name (the default is cl100k_base), or "heuristic" for the
// stdlib-only ~4-runes-per-token fallback.
func declareTokenizer() model.Tokenizer {
	v := strings.TrimSpace(os.Getenv("NAVI_TOKENIZER"))
	if strings.EqualFold(v, "heuristic") {
		return model.HeuristicTokenizer{}
	}
	return tokenizer.NewTiktoken(v)
}

// ollamaEndpoint resolves the Ollama base URL: OLLAMA_HOST wins, else the
// compiled default (http://localhost:11434).
func ollamaEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
		return v
	}
	return model.DefaultOllamaEndpoint
}

// headless reports whether the run is explicitly non-interactive. NAVI_DAEMON
// mirrors cmd/navi's --daemon: it forces the headless presentation even when a
// terminal is attached.
func headless() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NAVI_DAEMON"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// stdoutIsTTY reports whether stdout is an interactive terminal — the basis
// for choosing the TUI.
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// recordHealth prints the start-time connector-health view. A deployment would
// persist it beside the database so `navi health` can read it.
func recordHealth(view []navi.ConnectorHealth) {
	for _, h := range view {
		if h.State == navi.ConnectorFailed {
			slog.Warn("door failed to start", "door", h.Name, "addr", h.Addr, "reason", h.Reason)
			continue
		}
		slog.Info("door", "door", h.Name, "state", string(h.State), "addr", h.Addr)
	}
}

// logLevel reads NAVI_LOG_LEVEL ("debug", "info", "warn", "error").
func logLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NAVI_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Credential REFERENCES, never values (ADR 0008): each connector resolves the
// reference at the call site, so a rotated token needs no rebuild.
const (
	hulyCredential   = "HULY_TOKEN"
	githubCredential = "GITHUB_TOKEN"
)

// defaultHulyURL is this workspace's Huly instance. Huly is self-hosted, so the
// connector's own default (huly.app) is wrong here; HULY_URL overrides it.
const defaultHulyURL = "https://huly.flintwillow.software"

// envOr returns the environment value for key, or def when unset or blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envInt parses an integer environment value, falling back to def.
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// envDuration parses a Go duration environment value, falling back to def.
func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
