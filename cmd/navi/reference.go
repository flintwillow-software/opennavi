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
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/connector/cron"
	"github.com/flintwillow-software/opennavi/datastore"
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

	runtimeFile   string
	dbPath        string
	model         string
	ollamaHost    string
	workDir       string
	naviID        string
	compactBudget int
	workers       int
	portalAddr    string
	daemonMode    bool

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
	thoughtStore, err := sqlite.NewThoughtStore(store.DB())
	if err != nil {
		return err
	}

	naviID := p.naviID
	if naviID == "" {
		naviID = "default"
	}

	n := navi.New("navi",
		navi.WithVersion("0.1.0"),
		navi.WithLogger(p.slogLogger),
		navi.WithPermissions(navi.PermissionPolicy{
			AllowTools: rc.Tools.Allowed,
			DenyTools:  rc.Tools.Blocked,
		}),
		navi.WithWorkers(p.workers),
		navi.WithThoughtStore(thoughtStore),
		navi.WithQueueConfig(queueConfig(rc, !p.daemonMode)),
		navi.WithCompactBudget(p.compactBudget),
	)

	// The reference navi declares the non-durable reference datastore: it
	// integrates through the contract (the catalog takes
	// n.Datastores.Handle().Stateful()), but transcripts, memory, and tool
	// state stay in memory until the durable backends land
	// (datastore/fs, NAVI-365; datastore/sqlite, NAVI-364). The thought
	// queue and cron tick state stay on SQLite.
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
	if err := addPortals(n, p); err != nil {
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
	if err := addProvider(n, primary, rc.Provider.Model, rc.Provider.Endpoint, true); err != nil {
		return err
	}
	if fb := strings.ToLower(strings.TrimSpace(rc.Provider.Fallback)); fb != "" && fb != primary {
		if err := addProvider(n, fb, "", "", false); err != nil {
			return err
		}
	}
	return nil
}

func addProvider(n *navi.Navi, name, modelName, endpoint string, isDefault bool) error {
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
	return add(thoughts.Tools(n.Thoughts))
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
func addPortals(n *navi.Navi, p runParams) error {
	if p.portalAddr != "" {
		// Pre-IAM: the gateway is a trusted operator surface. A real
		// authorizer replaces AllowAll when IAM lands.
		control := portal.NewControl(portal.AllowAll())
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
