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
	portalAddr  string
	daemonMode  bool
	// noThinking turns a reasoning model's hidden reasoning off (Ollama's
	// `think: false`), so the model answers instead of spending its budget
	// thinking. Absent from the config it says nothing at all.
	noThinking bool

	logger *logging.Logger
	// slogLogger is the navi's structured logger, routed like the runtime
	// logger so TUI mode does not scribble over the alt-screen.
	slogLogger *slog.Logger
	cancel     context.CancelFunc
}

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

	// Pre-IAM: the reference navi's portals are a trusted operator surface,
	// and the object surface acts as the same operator through the same
	// authorizer a control portal uses (ADR 0017 decision 4). A real
	// authorizer replaces AllowAll when IAM lands.
	auth := portal.AllowAll()

	n := navi.New("navi",
		navi.WithVersion("0.1.0"),
		navi.WithLogger(p.slogLogger),
		navi.WithPermissions(navi.PermissionPolicy{
			AllowTools: rc.Tools.Allowed,
			DenyTools:  rc.Tools.Blocked,
		}),
		navi.WithWorkers(p.workers),
		navi.WithQueueConfig(queueConfig(rc, !p.daemonMode)),
		navi.WithShortTermMemory(),
		navi.WithObjectAuthorizer(auth, "operator"),
	)

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
	if err := addPortals(n, p, auth); err != nil {
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
