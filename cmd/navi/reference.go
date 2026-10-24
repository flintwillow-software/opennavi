// Reference-navi assembly (M5): cmd/navi run builds a real navi through
// navi.New and runs it. The retired internal/daemon.Assemble used to sit
// behind this: the runtime-config merge, catalog wiring, connectors, and
// portals live here (or in the SDK), not behind a private daemon.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/connector/cron"
	"github.com/flintwillow-software/opennavi/connector/metrics"
	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/fs"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	toolhttp "github.com/flintwillow-software/opennavi/tool/http"
	"github.com/flintwillow-software/opennavi/tool/note"
	"github.com/flintwillow-software/opennavi/tool/thoughts"
	timepkg "github.com/flintwillow-software/opennavi/tool/time"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

// runParams carries the command-layer inputs into the reference navi.
type runParams struct {
	ctx context.Context

	runtimeFile string
	dbPath      string
	model       string
	ollamaHost  string
	workDir     string
	naviID      string
	workers     int
	metricsAddr string
	portalAddr  string
	// webhookAddr is the webhook ingress listen address (ADR 0029). The navi
	// owns the listener; a connector declares only its route.
	webhookAddr string
	daemonMode  bool
	// noThinking turns a reasoning model's hidden reasoning off (Ollama's
	// `think: false`), so the model answers instead of spending its budget
	// thinking. Absent from the config it says nothing at all.
	noThinking bool

	// Trust configuration (NAVI-401/402). trustStore is an ADR 0008 reference
	// to a keyed trust-store document; oidcIssuer/oidcAudience declare an OIDC
	// identity provider. devAllowAll is the explicit development default: no
	// credential is required and every connection is trusted.
	trustStore   string
	oidcIssuer   string
	oidcAudience string
	devAllowAll  bool

	logger *logging.Logger
	// slogLogger is the navi's structured logger, routed like the runtime
	// logger so TUI mode does not scribble over the alt-screen.
	slogLogger *slog.Logger
	cancel     context.CancelFunc
}

// objectActor is the reference navi's own service principal, navi:<name>
// (ADR 0021 decision 10). It is what --dev-allow-all resolves every
// connection to and what the in-process console acts as; a network binding
// re-scopes the object surface to the connection's verified principal
// (ADR 0022 decision 1).
var objectActor = principal.MustParse("navi:navi")

// serveReferenceNavi assembles the reference navi and serves it until ctx ends (or,
// in TUI mode, the user quits).
func serveReferenceNavi(p runParams) error {
	rc, err := mergeRuntime(p)
	if err != nil {
		return err
	}

	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return err
	}
	store, err := sqlite.Open(rc.SessionDB, sqlite.Options{Cipher: cipher})
	if err != nil {
		return err
	}
	defer store.Close()

	naviID := p.naviID
	if naviID == "" {
		naviID = "default"
	}

	// Portals are authenticated by the navi (ADR 0021 decisions 6-8, ADR 0022
	// decision 8): a connection's credential is resolved through the declared
	// trust sources, and the principal it yields is what the control surface
	// authorizes. The authorizer is the navi itself — the compiled policy
	// (NAVI-399, NAVI-404) — so a control action or object read is decided
	// against the verified actor and the request facts the binding supplied,
	// never a value a client asserted. The navi's own service principal
	// (navi:navi) holds the compiled ceiling by construction, which is what
	// keeps the in-process console and --dev-allow-all working; a deployment
	// declares roles for the principals its trust source resolves.

	opts := []navi.Option{
		navi.WithVersion("0.1.0"),
		navi.WithLogger(p.slogLogger),
		navi.WithPermissions(navi.PermissionPolicy{
			AllowTools: rc.Tools.Allowed,
			DenyTools:  rc.Tools.Blocked,
		}),
		navi.WithWorkers(p.workers),
		navi.WithQueueConfig(queueConfig(rc, !p.daemonMode)),
		navi.WithShortTermMemory(),
		// Record the start-time connector health so a later `navi health`
		// sees a degraded metrics or portal listener without reading logs
		// (NAVI-329).
		navi.WithHealthRecorder(connectorHealthRecorder(rc, p)),
	}
	// The navi's webhook ingress (ADR 0029 decision 2): ONE listener the navi
	// owns, which connectors register their routes on. Absent, no listener is
	// declared and a connector that declared a webhook binding fails Start
	// naming that rather than silently serving nothing.
	if addr := strings.TrimSpace(rc.WebhookAddr); addr != "" {
		opts = append(opts, navi.WithWebhooks(addr))
	}

	// The compiled policy (ADR 0021 decisions 3-5). connector:github may
	// enqueue work — its inbound events become thoughts — but call no tools and
	// read no types directly. The navi's own service principal holds the
	// compiled ceiling by construction, so this is the one role the reference
	// navi needs to name; a deployment declares roles for its own principals.
	githubEnqueue, rerr := navi.NewRole("github-enqueue",
		navi.TrustPolicy{Principals: []principal.Ref{principal.MustParse("connector:github")}},
		navi.CapabilitySet{Allow: navi.Allows(navi.Enqueue())})
	if rerr != nil {
		return fmt.Errorf("navi: github role: %w", rerr)
	}

	if p.trustStore != "" {
		store, terr := navi.NewFileTrustStore(navi.FileTrustStoreConfig{Path: p.trustStore})
		if terr != nil {
			return fmt.Errorf("navi: trust store: %w", terr)
		}
		opts = append(opts, navi.WithTrustSource(store))
	}
	if p.oidcIssuer != "" {
		idp, ierr := navi.NewOIDC(navi.OIDCConfig{Issuer: p.oidcIssuer, Audience: p.oidcAudience})
		if ierr != nil {
			return fmt.Errorf("navi: OIDC trust source: %w", ierr)
		}
		opts = append(opts, navi.WithTrustSource(idp))
	}
	if p.devAllowAll {
		p.slogLogger.Warn("navi: --dev-allow-all trusts every portal connection without a credential; never use it on an untrusted network")
		opts = append(opts, navi.WithPortalAuthenticator(portal.AllowAllAuthenticator(objectActor)))
	} else if p.portalAddr != "" && p.trustStore == "" && p.oidcIssuer == "" {
		p.slogLogger.Warn("navi: --portal-addr is set but no trust source is declared; every remote attachment will be refused (fail closed)",
			"hint", "declare --trust-store or --oidc-issuer, or opt into --dev-allow-all for local use")
	}

	n := navi.New("navi", opts...)

	if err := n.Roles.Add(githubEnqueue); err != nil {
		return fmt.Errorf("navi: github role: %w", err)
	}

	// The reference navi declares all three backends: `fs` is the compiled
	// default (ADR 0011 — markdown + frontmatter, human-inspectable, durable),
	// so transcripts, memory, and tool state survive a restart; `sqlite` is the
	// single-file transactional option selected with `datastore.backend:
	// sqlite`, rooted at the same database the cross-process mailbox and cron
	// tick state already use; `inmem` stays selectable with `datastore.backend:
	// inmem` for a volatile run.
	//
	// The fs root sits beside the session database — `var/` is this repo's only
	// artifact directory — and a runtime config `datastore.params.path`
	// overrides either durable backend's location.
	if err := n.Datastores.Add(fs.CandidateAt(fsStateRoot(rc.SessionDB))); err != nil {
		return err
	}
	if err := n.Datastores.Add(sqlite.CandidateAt(rc.SessionDB)); err != nil {
		return err
	}
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		return err
	}
	if err := addProviders(n, rc); err != nil {
		return err
	}
	if err := addCatalogTools(n, rc, naviID, n.Datastores.Handle().Stateful(), p.workDir); err != nil {
		return err
	}
	if err := addCron(n, rc, store.DB()); err != nil {
		return err
	}
	if err := addMetrics(n, rc); err != nil {
		return err
	}
	if err := addPortals(n, p, n); err != nil {
		return err
	}

	if p.daemonMode {
		go func() {
			readSession(n.Thoughts, p.logger)
			p.cancel()
		}()
		fmt.Println("navi run --daemon — headless; type a message; Ctrl+C parks the current thought; Ctrl+D ends the session")
	}

	return n.Run(p.ctx)
}

// fsStateRoot is where the fs backend keeps a navi's state: beside the session
// database, so the reference navi's artifacts stay under `var/`. A runtime
// config `datastore.params.path` overrides it.
func fsStateRoot(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "state")
}

// mergeRuntime applies the merge chain (file → env → flags) and validates.
func mergeRuntime(p runParams) (config.RuntimeConfig, error) {
	fileRC := config.RuntimeConfig{}
	if p.runtimeFile != "" {
		var err error
		fileRC, err = config.LoadRuntimeFile(p.runtimeFile)
		if err != nil {
			return config.RuntimeConfig{}, err
		}
	}
	flags := config.RuntimeConfig{}
	if p.model != "" {
		flags.Provider.Model = p.model
	}
	if p.dbPath != "" {
		flags.SessionDB = p.dbPath
	}
	if p.ollamaHost != "" {
		flags.Provider.Endpoint = p.ollamaHost
	}
	if p.noThinking {
		f := false
		flags.Provider.Thinking = &f
	}
	if p.metricsAddr != "" {
		flags.MetricsAddr = p.metricsAddr
	}
	if p.webhookAddr != "" {
		flags.WebhookAddr = p.webhookAddr
	}
	rc := config.MergeRuntime(fileRC, flags)
	if err := rc.Validate(); err != nil {
		return config.RuntimeConfig{}, err
	}
	return rc, nil
}

// addProviders declares the primary and fallback provider descriptors
// (ADR 0009), applying the resolved endpoint to the primary.
func addProviders(n *navi.Navi, rc config.RuntimeConfig) error {
	primary := strings.ToLower(strings.TrimSpace(rc.Provider.Primary))
	if primary == "" {
		primary = "ollama"
	}
	if err := addProvider(n, primary, rc.Provider.Model, rc.Provider.Endpoint, rc.Provider.Thinking, true); err != nil {
		return err
	}
	if fb := strings.ToLower(strings.TrimSpace(rc.Provider.Fallback)); fb != "" && fb != primary {
		// The fallback is a different provider, so the primary's reasoning
		// setting does not apply to it.
		if err := addProvider(n, fb, "", "", nil, false); err != nil {
			return err
		}
	}
	return nil
}

func addProvider(n *navi.Navi, name, modelName, endpoint string, thinking *bool, isDefault bool) error {
	lower := strings.ToLower(name)
	if modelName == "" {
		switch lower {
		case "openrouter", "openai":
			modelName = "anthropic/claude-sonnet-4"
		default:
			modelName = "qwen3.8:27b"
		}
	}
	var opts []model.Option
	if isDefault {
		opts = append(opts, model.Default())
	}
	if endpoint != "" {
		opts = append(opts, model.WithEndpoint(endpoint))
	}
	if thinking != nil {
		opts = append(opts, model.WithThinking(*thinking))
	}
	switch lower {
	case "openrouter", "openai":
		return n.Models.Add(model.OpenRouter(modelName, opts...))
	case "ollama", "":
		return n.Models.Add(model.Ollama(modelName, opts...))
	default:
		return fmt.Errorf("navi: unknown provider %q", name)
	}
}

// addCatalogTools registers the catalog the reference navi ships.
func addCatalogTools(n *navi.Navi, rc config.RuntimeConfig, naviID string, state datastore.ToolState, workDir string) error {
	add := func(tools []tool.Tool, err error) error {
		if err != nil {
			return err
		}
		for _, t := range tools {
			if err := n.Tools.Add(t); err != nil {
				return err
			}
		}
		return nil
	}
	if err := add(builtin.Tools(workDir)); err != nil {
		return err
	}
	if err := add(todo.Tools(naviID, state)); err != nil {
		return err
	}
	if err := add(note.Tools(naviID, state, noteCaps(rc.ToolCaps)...)); err != nil {
		return err
	}
	// Expose the stateful aggregates as object types, bound to the same view
	// the tools were given (ADR 0017 decision 8): a portal renders todos and
	// notes through the object surface.
	for _, reg := range navi.ToolStateTypes(naviID, state) {
		if err := n.Types.Add(reg); err != nil {
			return err
		}
	}
	tm, err := timepkg.Tool()
	if err != nil {
		return err
	}
	if err := n.Tools.Add(tm); err != nil {
		return err
	}
	hp, err := toolhttp.Tool(httpEgress(rc.ToolCaps)...)
	if err != nil {
		return err
	}
	if err := n.Tools.Add(hp); err != nil {
		return err
	}
	if err := add(thoughts.Tools(n.Thoughts)); err != nil {
		return err
	}
	// The agent-chosen skill-loading path (ADR 0013 decision 5): added
	// explicitly like any other tool.
	skillTool, err := n.SkillTool()
	if err != nil {
		return err
	}
	return n.Tools.Add(skillTool)
}

// addCron wires configured schedules as the cron connector (ADR 0010),
// backed by the shared store so tick state persists and missed ticks are
// coalesced across restarts (NAVI-358).
func addCron(n *navi.Navi, rc config.RuntimeConfig, db *sql.DB) error {
	if rc.Cron == nil || len(rc.Cron.Schedules) == 0 {
		return nil
	}
	schedules := make([]cron.Schedule, 0, len(rc.Cron.Schedules))
	for _, s := range rc.Cron.Schedules {
		schedules = append(schedules, cron.Schedule{
			Name:     s.Name,
			Expr:     s.Expr,
			Prompt:   s.Prompt,
			Priority: s.Priority,
			TZ:       s.TZ,
			Disabled: !s.IsEnabled(),
		})
	}
	return n.Connectors.Add(cron.New(cron.Config{Schedules: schedules}, cron.WithStateStore(db)))
}

// addMetrics wires the thought-queue metrics connector when a listen
// address is configured (NAVI-357). The address comes from the merged
// runtime config: the --metrics-addr flag (top layer) over the YAML's
// metrics_addr, so the config turns the same knob as the flag. Absent,
// the collector is not declared at all — zero cost unset.
func addMetrics(n *navi.Navi, rc config.RuntimeConfig) error {
	addr := strings.TrimSpace(rc.MetricsAddr)
	if addr == "" {
		return nil
	}
	return n.Connectors.Add(metrics.New(addr))
}

// addPortals wires the TUI (in-process) and, when configured, the HTTP
// gateway with a control portal so remote front-ends can observe and
// control (queue thoughts, steer) across processes.
func addPortals(n *navi.Navi, p runParams, auth portal.Authorizer) error {
	if p.portalAddr != "" {
		control := portal.NewControl(auth)
		if err := n.Portals.Add(control); err != nil {
			return err
		}
		if err := n.Portals.Add(gateway.New(p.portalAddr, control)); err != nil {
			return err
		}
	}
	if !p.daemonMode {
		tp := tui.NewPortal()
		if err := n.Portals.Add(tp); err != nil {
			return err
		}
		// Quitting the TUI ends the run (park-in-flight semantics come
		// from the navi's shutdown path).
		go func() {
			<-tp.Done()
			p.cancel()
		}()
	}
	return nil
}

// referenceDoors is the reference navi's operator-facing connector
// health view (NAVI-329): the doors a human checks with `navi health`.
// The navi records every component's health; the in-process TUI and
// control portals are not part of this view.
var referenceDoors = []string{"scheduler", "metrics", "portal"}

// connectorHealthRecorder persists the start-time connector health and
// warns about degraded doors. It runs once, at serve start.
func connectorHealthRecorder(rc config.RuntimeConfig, p runParams) navi.HealthRecorder {
	return func(reported []navi.ConnectorHealth) {
		view := referenceConnectorHealth(reported)
		if err := writeHealthSnapshot(healthSnapshotPath(rc.SessionDB), view); err != nil && p.logger != nil {
			p.logger.Warn("connector health snapshot not written", map[string]any{"error": err.Error()})
		}
		for _, h := range view {
			if h.State != navi.ConnectorFailed {
				continue
			}
			if p.logger != nil {
				p.logger.Warn("connector degraded", map[string]any{
					"connector": h.Name, "addr": h.Addr, "error": h.Reason,
				})
			}
			// A human-readable line alongside the structured log; only in
			// daemon mode, where stderr is the console (in TUI mode the
			// alt-screen owns the terminal).
			if p.daemonMode {
				fmt.Fprintf(os.Stderr, "navi: WARNING: connector %s is degraded: %s\n", h.Name, h.Reason)
			}
		}
	}
}

// referenceConnectorHealth projects the navi's full component-health
// view onto the reference navi's three doors, filling in `disabled` for
// a door that declares no component (metrics/portal absent, no cron
// schedules).
func referenceConnectorHealth(reported []navi.ConnectorHealth) []navi.ConnectorHealth {
	byName := make(map[string]navi.ConnectorHealth, len(reported))
	for _, h := range reported {
		byName[h.Name] = h
	}
	view := make([]navi.ConnectorHealth, 0, len(referenceDoors))
	for _, name := range referenceDoors {
		if h, ok := byName[name]; ok {
			view = append(view, h)
			continue
		}
		view = append(view, navi.ConnectorHealth{Name: name, State: navi.ConnectorDisabled})
	}
	return view
}

// readSession drives the headless session: the first line becomes a
// durable thought, later lines steer the in-flight thought at the turn
// boundary; EOF ends the session.
func readSession(ts thought.ThoughtSystem, logger *logging.Logger) {
	sc := bufio.NewScanner(os.Stdin)
	var current string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if current != "" {
			if err := ts.RouteMessage(current, line); err != nil {
				logger.Warn("message not delivered", map[string]any{"thought": current, "error": err.Error()})
				fmt.Printf("message not delivered: %v\n", err)
			}
			continue
		}
		now := time.Now().UTC()
		th := &thought.Thought{
			ID:        fmt.Sprintf("fg-%d", now.UnixNano()),
			Prompt:    line,
			Priority:  4,
			Status:    thought.ThoughtPending,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := ts.Enqueue(th); err != nil {
			logger.Error("enqueue failed", map[string]any{"error": err.Error()})
			continue
		}
		current = th.ID
	}
}

// ── config → navi helpers ───────────────────────────────────────────────

func queueConfig(rc config.RuntimeConfig, foreground bool) thought.Config {
	cfg := thought.Config{
		MaxConcurrent:  thoughtQueueMax(rc),
		PrioritySlots:  thoughtPrioritySlots(rc),
		SlotExhaustion: thought.PolicyQueue,
	}
	if foreground {
		if cfg.MaxConcurrent < 2 {
			cfg.MaxConcurrent = 2
		}
		if cfg.PrioritySlots+1 > cfg.MaxConcurrent {
			cfg.PrioritySlots = cfg.MaxConcurrent - 1
		}
		cfg.ForegroundSlots = 1
	}
	return cfg
}

func thoughtQueueMax(rc config.RuntimeConfig) int {
	if rc.ThoughtQueue != nil && rc.ThoughtQueue.MaxConcurrent > 0 {
		return rc.ThoughtQueue.MaxConcurrent
	}
	return 1
}

func thoughtPrioritySlots(rc config.RuntimeConfig) int {
	if rc.ThoughtQueue != nil {
		return rc.ThoughtQueue.PrioritySlots
	}
	return 0
}

// noteCaps maps tool_caps.notes config onto note registration options
// (zero fields keep the package defaults).
func noteCaps(tc *config.ToolCapsConfig) []note.Options {
	if tc == nil || tc.Notes == nil {
		return nil
	}
	return []note.Options{{
		MaxBodyBytes: tc.Notes.MaxBodyBytes,
		MaxNaviBytes: tc.Notes.MaxNaviBytes,
	}}
}

// httpEgress maps tool_caps.http config onto http registration options.
func httpEgress(tc *config.ToolCapsConfig) []toolhttp.Options {
	if tc == nil || tc.HTTP == nil {
		return nil
	}
	return []toolhttp.Options{{
		MaxResponseBytes: tc.HTTP.MaxResponseBytes,
		Allowlist:        tc.HTTP.Allowlist,
		Denylist:         tc.HTTP.Denylist,
	}}
}

// slogFrom builds the navi's structured logger over the routed writer.
// JSON keeps file output consistent with the runtime logger; the human
// console keeps the standard handler.
func slogFrom(w io.Writer, human bool, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if human {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
