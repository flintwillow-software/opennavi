// Command navi-fleet is the full-fat reference navi: every connector the
// workspace runs on, composed over one SQLite datastore.
//
// It is deliberately the opposite of the minimal starters. Where `navi-code`
// declares a single forge connector and `navi-sqlite` declares a datastore,
// `navi-fleet` declares the whole set — Huly (egress + inbound), Forgejo
// (egress), and GitHub (egress + opt-in inbound) — so it is the worked example
// of a navi that acts across services *and* hears from them, and the basis for
// the dogfooding deployment (epic NAVI-405, deploy NAVI-407).
//
// Everything below is the static, compiled-in half (ADR 0006/0013): which
// services exist, which event kinds are polled, and which tools are on the
// model's surface. Deployment settings — endpoints, workspace, credential
// references, the project and repositories to watch — come from the environment
// and the config file named by NAVI_CONFIG. Runtime config may NARROW what a
// declared connector watches; it can never add one (ADR 0023 decision 1).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/connector/forgejo"
	"github.com/flintwillow-software/opennavi/connector/github"
	"github.com/flintwillow-software/opennavi/connector/huly"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

// Credential REFERENCES, never values (ADR 0008): the connector resolves each at
// the call site, so a rotated token needs no rebuild.
const (
	hulyCredential    = "HULY_TOKEN"
	forgejoCredential = "FORGEJO_TOKEN"
	githubCredential  = "GITHUB_TOKEN"
)

// defaultHulyURL is this workspace's Huly instance. Huly is self-hosted, so the
// connector's own DefaultURL (huly.app) is wrong here; HULY_URL overrides it.
const defaultHulyURL = "https://huly.flintwillow.software"

// personality is the navi's compiled identity. Its *capability* is the
// connector and tool set below, not this prose (ADR 0014 decision 5).
const personality = "You are navi-fleet, an engineering navi for the flintwillow-software " +
	"workspace. You work issues where they live: Huly is the tracker and the source of " +
	"truth, Forgejo and GitHub are the forges. You orient yourself before acting, make " +
	"small verifiable changes on a feature branch, never commit to the default branch, " +
	"and keep your reasoning terse. When an issue is assigned to you, pick it up, move it " +
	"to In Progress in Huly, and comment with what you did."

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "navi-fleet:", err)
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
	return n.Run(ctx)
}

// compose assembles the navi and opens its datastore. It is separate from run
// so a test can validate the whole composition without serving it.
func compose() (*navi.Navi, *sqlite.Store, error) {
	name := envOr("NAVI_NAME", "navi-fleet")

	opts := []navi.Option{
		navi.WithVersion("0.1.0"),
		navi.WithPersonality(personality),
	}
	if cfg := strings.TrimSpace(os.Getenv("NAVI_CONFIG")); cfg != "" {
		opts = append(opts, navi.WithRuntimeConfig(cfg))
	}
	n := navi.New(name, opts...)

	// One SQLite file backs the queue, transcripts, memory, and tool state —
	// and the connectors' persisted inbound seen-sets, which share this *sql.DB
	// so a redelivery is suppressed across a restart (ADR 0023 decision 5).
	dbPath := envOr("NAVI_DB", filepath.Join("var", name+".db"))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, nil, err
	}
	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return nil, nil, err
	}
	store, err := sqlite.Open(dbPath, sqlite.Options{Cipher: cipher})
	if err != nil {
		return nil, nil, err
	}

	n, err = build(n, store, dbPath)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return n, store, nil
}

// build declares the datastore candidate, the tools, the connectors, the model,
// and the portal. It takes an open store and its path so compose owns closing
// the store on failure.
func build(n *navi.Navi, store *sqlite.Store, dbPath string) (*navi.Navi, error) {
	// The datastore is a compiled candidate, selected at runtime (ADR 0011).
	candidate := sqlite.CandidateAt(dbPath)
	candidate.Default = true
	if err := n.Datastores.Add(candidate); err != nil {
		return nil, err
	}

	// Tools: the builtins bound to the working directory, plus the stateful
	// todo list on the bound datastore.
	workDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	builtins, err := builtin.Tools(workDir)
	if err != nil {
		return nil, err
	}
	for _, t := range builtins {
		if err := n.Tools.Add(t); err != nil {
			return nil, err
		}
	}
	todos, err := todo.Tools("default", n.Datastores.Handle().Stateful())
	if err != nil {
		return nil, err
	}
	for _, t := range todos {
		if err := n.Tools.Add(t); err != nil {
			return nil, err
		}
	}

	// Connectors. Each is declared at build time and its egress tools are added
	// explicitly — never auto-registered (ADR 0020) — so this function is the
	// whole story of what navi-fleet can reach and say.
	if err := addHuly(n, n.Name(), store.DB()); err != nil {
		return nil, err
	}
	if err := addForgejo(n); err != nil {
		return nil, err
	}
	if err := addGitHub(n, n.Name()); err != nil {
		return nil, err
	}

	// One model, marked default (ADR 0009). Runtime config may retarget the
	// provider/model; the declared set is what model_list reports.
	if err := n.Models.Add(model.Ollama(envOr("NAVI_MODEL", "qwen3.8:27b"), model.Default())); err != nil {
		return nil, err
	}

	// The TUI is the front door (ADR 0002: portals).
	if err := n.Portals.Add(tui.NewPortal()); err != nil {
		return nil, err
	}
	return n, nil
}

// addHuly wires the tracker: the egress tools plus the inbound subscriptions
// that make "assign an issue to this navi and it picks it up" work (NAVI-561).
// The inbound half reuses the egress client and credential (ADR 0023 decision
// 6), and the seen-set is persisted in the shared store so a restart does not
// replay a redelivery.
func addHuly(n *navi.Navi, naviName string, db *sql.DB) error {
	actor, err := principal.New(principal.KindNavi, naviName)
	if err != nil {
		return err
	}
	h := huly.New(huly.Config{
		URL:        envOr("HULY_URL", defaultHulyURL),
		Workspace:  strings.TrimSpace(os.Getenv("HULY_WORKSPACE")),
		Credential: hulyCredential,
		// The event kinds are a build-time declaration (ADR 0023 decision 1):
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
			// Inbound work acts as the navi itself, not as the connector: an
			// issue assigned to this navi is the navi's job (ADR 0021 d10).
			Principal: actor,
		}},
		PollInterval: 60 * time.Second,
	}, huly.WithStateStore(db))
	if err := n.Connectors.Add(h); err != nil {
		return err
	}
	tools, err := h.SendTools()
	if err != nil {
		return err
	}
	return addTools(n, tools)
}

// addForgejo wires the forge a navi acts in. Forgejo emits Gitea-style webhooks,
// so its inbound binding is the shared webhook seam (ADR 0029) and lands later;
// this slice is the egress half.
func addForgejo(n *navi.Navi) error {
	fj := forgejo.New(forgejo.Config{
		BaseURL:    strings.TrimSpace(os.Getenv("FORGEJO_URL")), // empty = forgejo.DefaultBaseURL
		Credential: forgejoCredential,
		Repository: forgejoRepository(),
	})
	if err := n.Connectors.Add(fj); err != nil {
		return err
	}
	tools, err := fj.SendTools()
	if err != nil {
		return err
	}
	return addTools(n, tools)
}

// addGitHub wires the public forge: egress plus the opt-in mention ingress the
// navi-code template established. Ingress is off unless NAVI_GITHUB_USERNAME
// names a login to watch for.
func addGitHub(n *navi.Navi, naviName string) error {
	cfg := github.Config{
		Token: github.ChainToken(
			github.StaticToken{Ref: githubCredential},
			&github.GHCLIToken{},
		),
		Repository: githubRepository(),
	}
	if subs := githubIngress(cfg.Repository, os.Getenv("NAVI_GITHUB_USERNAME"), naviName); len(subs) > 0 {
		cfg.PollInterval = 30 * time.Second
		cfg.Subscriptions = subs
	}
	gh := github.New(cfg)
	if err := n.Connectors.Add(gh); err != nil {
		return err
	}
	tools, err := gh.SendTools()
	if err != nil {
		return err
	}
	return addTools(n, tools)
}

// addTools adds every tool a connector handed back, explicitly (ADR 0020).
func addTools(n *navi.Navi, tools []tool.Tool) error {
	for _, t := range tools {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}
	return nil
}

// githubIngress builds the opt-in GitHub ingress declaration (ADR 0023). The
// event kind, intent, and service principal are declared here; only the login
// is a runtime input.
func githubIngress(repo, mention, naviName string) []github.Subscription {
	mention = strings.TrimSpace(mention)
	if mention == "" || strings.TrimSpace(repo) == "" {
		return nil
	}
	actor, err := principal.New(principal.KindNavi, naviName)
	if err != nil {
		return nil
	}
	return []github.Subscription{{
		Name:       "mentions",
		Repository: repo,
		Events:     []github.EventKind{github.EventIssueMention},
		Mention:    mention,
		Principal:  actor,
	}}
}

// forgejoRepository resolves the repository the Forgejo egress tools act on: an
// explicit override, else the working directory's git remote. Deployment config,
// not a secret, so it is read at start.
func forgejoRepository() string {
	if v := strings.TrimSpace(os.Getenv("NAVI_FORGEJO_REPO")); v != "" {
		return v
	}
	return remoteRepository()
}

// githubRepository resolves the repository the GitHub egress tools act on, the
// same way (an explicit override, else the git remote).
func githubRepository() string {
	if v := strings.TrimSpace(os.Getenv("NAVI_GITHUB_REPO")); v != "" {
		return v
	}
	return remoteRepository()
}

// remoteRepository parses the working directory's `origin` remote into
// owner/name, or "" when there is none.
func remoteRepository() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return parseGitRemote(strings.TrimSpace(string(out)))
}

// parseGitRemote turns a git remote URL into owner/name. Both the ssh and https
// spellings are accepted, for any host:
//
//	git@github.com:acme/widgets.git            -> acme/widgets
//	https://git.flintwillow.software/acme/widgets -> acme/widgets
func parseGitRemote(remote string) string {
	s := remote
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[j+1:] // drop the host
		}
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:] // scp-like: git@host:owner/name
	} else {
		return ""
	}
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), ".git"), "/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return s
}

// envOr returns the environment value for key, or def when it is unset or
// blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
