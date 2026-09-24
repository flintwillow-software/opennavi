package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/internal/daemon"
	"github.com/flintwillow-software/opennavi/internal/interfaces/tui"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// runInteractive is the `navi run` subcommand — the binary-run entry
// (S010003 §06): ONE assembly with the daemon — the same thought
// queue, pool, registry-governed tools, and connectors (cron, metrics,
// portal) load in every run mode; run modes differ ONLY by the TUI
// layer (NAVI-325). By default the Bubble Tea TUI is the front door
// (S000406, NAVI-319), attached in-process over the portal, and the
// session's current thought occupies a foreground slot; --daemon is
// the headless presentation — no TUI, classic stdin loop, human-
// readable lines kept (structured-log migration explicitly deferred),
// and no foreground thought (the stdin loop enqueues normal work).
// Ctrl+C parks the foreground thought with its checkpoint (never
// kill-and-lose); the process exits when the session ends and the
// queue drains.
func runInteractive(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	runtimeFile := fs.String("config", "", "path to runtime config YAML (optional)")
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	model := fs.String("model", "", "LLM model name (required)")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (overrides $OLLAMA_HOST and YAML; default http://localhost:11434)")
	workDir := fs.String("workdir", ".", "working directory for file/shell tools")
	naviID := fs.String("navi-id", "default", "per-navi scoping key for stateful tool storage (todos/notes)")
	compactBudget := fs.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	workers := fs.Int("workers", 2, "thought worker count")
	metricsAddr := fs.String("metrics-addr", "", "Prometheus metrics listen address, e.g. :9090 (default: disabled)")
	portalAddr := fs.String("portal-addr", "", "portal observation/control listen address, e.g. :8090 (default: disabled)")
	logLevel := fs.String("log-level", "", "log level: debug, info, warn, error (default info; empty = config/default)")
	logFormat := fs.String("log-format", "", "log format: human | json (default: human on an interactive TTY, json otherwise)")
	logFile := fs.String("log-file", "", "log file path (default stderr; files log JSON)")
	daemonMode := fs.Bool("daemon", false, "headless presentation: no TUI, stdin-loop session driver, no foreground slot (human-readable lines kept)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("run: --model is required")
	}

	// Log routing (NAVI-331): in TUI mode the Bubble Tea alt-screen
	// owns the terminal — stderr log lines would scribble over it. When
	// the TUI is attached (not --daemon) and no explicit --log-file is
	// set, route logs to a file under the DB's directory
	// (<db-dir>/navi.log, JSON per the file convention) and tell the
	// user where they went (one-time line before the alt-screen). --daemon
	// keeps stderr (that's its console); explicit --log-file is honored
	// in both modes; --log-format semantics unchanged.
	route := resolveLogRoute(*daemonMode, *logFile, *dbPath)
	var openedLogFile string
	var lw io.Writer = os.Stderr
	if route.path != "" {
		f, ferr := os.OpenFile(route.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if ferr != nil {
			return fmt.Errorf("run: open log file %s: %w", route.path, ferr)
		}
		defer f.Close()
		lw = f
		openedLogFile = route.path
	}
	// Log-format default (NAVI-328, documented): human on an
	// interactive stderr TTY, json otherwise — pipes/redirects and
	// --log-file always get the structured form (file output stays
	// JSON, including the TUI-mode default file). Assemble overrides
	// with the config section / flag per the merge chain (flag wins).
	format := logging.FormatJSON
	if route.stderr && stderrIsTTY() {
		format = logging.FormatHuman
	}
	logger := logging.New(lw, logging.ParseLevel(*logLevel), nil)
	logger.SetFormat(format)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := daemon.Assemble(daemon.RuntimeParams{
		RuntimeFile:   *runtimeFile,
		SessionDB:     *dbPath,
		Model:         *model,
		OllamaHost:    *ollamaHost,
		WorkDir:       *workDir,
		Logger:        logger,
		LogFormat:     *logFormat,
		LogLevel:      *logLevel,
		NaviID:        *naviID,
		Foreground:    !*daemonMode, // TUI session attached → foreground slot
		CompactBudget: *compactBudget,
		Workers:       *workers,
		MetricsAddr:   *metricsAddr,
		PortalAddr:    *portalAddr,
	})
	if err != nil {
		return err
	}
	defer rt.Store.Close()
	logger.Info("binary run started", map[string]any{"model": *model, "db": *dbPath})

	sessionDone := make(chan struct{})
	runDone := make(chan error, 1)
	go func() { runDone <- rt.RunForeground(ctx, sessionDone) }()

	// One-time log-path note (NAVI-331): printed BEFORE the TUI's
	// alt-screen engages, so users know where logs went (stderr stays
	// untouched from here on in TUI mode).
	if openedLogFile != "" {
		fmt.Printf("navi: logs → %s\n", openedLogFile)
	}

	// Session driver: the Bubble Tea TUI is the front door (S000406,
	// NAVI-319), attached in-process over the portal; --daemon is the
	// headless presentation — same stdin loop, human-readable lines
	// kept, no foreground slot (work enters as normal thoughts).
	//
	// STDIN OWNERSHIP (NAVI-332): the TUI branch must NOT spawn
	// readSession — Bubble Tea reads os.Stdin exclusively; a competing
	// bufio.Scanner consumes keystrokes until newline and the TUI
	// appears frozen (renders once, no key events). Thought creation
	// in TUI mode is the `n` action (NAVI-326), steering is `a`
	// (NAVI-319). --daemon has no TUI, so its stdin loop is the ONLY
	// closer of sessionDone in that mode (single-closer invariant —
	// the TUI-quit path closes it in the other branch; two closers
	// panic on piped-stdin EOF).
	var tuiRun error
	if *daemonMode {
		go readSession(rt, logger, sessionDone, false)
		fmt.Println("navi run --daemon — headless; type a message; Ctrl+C parks the current thought; Ctrl+D ends the session")
	} else {
		tport, terr := tui.NewInProcess(rt.Queue, rt.SessionM)
		if terr != nil {
			return terr
		}
		tuiRun = tui.Run(tport) // Bubble Tea owns the terminal AND stdin; quitting ends the session
		close(sessionDone)      // session over — RunForeground drains remaining work
	}
	if tuiRun != nil {
		return tuiRun
	}
	runErr := <-runDone
	if runErr == nil {
		logger.Info("binary run stopped", nil)
		return nil
	}
	if ctx.Err() != nil && (runErr == context.Canceled || errors.Is(runErr, context.Canceled)) {
		// Interrupt: the in-flight thought was parked with a checkpoint
		// (resume on the next run via crash recovery). Exit cleanly.
		fmt.Println("navi binary run parked — state saved")
		return nil
	}
	return runErr
}

// readSession drives the interactive session: the first stdin line
// becomes a durable thought (same lifecycle as daemon work); lines
// while it runs steer it at the turn boundary via RouteMessage; EOF
// (Ctrl+D) ends the session — the run then drains and exits. With a
// TUI session attached (foreground), thoughts are Foreground-tagged
// and claim the reserved foreground slot; in headless --daemon mode
// work enters as normal thoughts (no foreground slot, NAVI-325).
// Exits early on ctx cancellation so Ctrl+C doesn't wait on stdin.
func readSession(rt *daemon.Runtime, logger *logging.Logger, done chan<- struct{}, foreground bool) {
	defer close(done)
	sc := bufio.NewScanner(os.Stdin)
	var foregroundID string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if foregroundID != "" {
			// A foreground thought is in flight: steer it at the turn
			// boundary (S010312 running path; the bridge injects into
			// the agent's steering queue). Rejections (finished, no
			// in-flight agent, unknown id) surface in the ack event and
			// the error — print the reason for the session.
			if _, err := rt.Queue.RouteMessage(foregroundID, line); err != nil {
				logger.Warn("message not delivered", map[string]any{"thought": foregroundID, "error": err.Error()})
				fmt.Printf("message not delivered: %v\n", err)
			}
			continue
		}
		// Interactive input becomes a durable thought (S010003 §06):
		// same lifecycle/persistence as daemon-enqueued work. With a
		// TUI session attached it claims the reserved foreground slot;
		// headless --daemon work enters as normal thoughts (NAVI-325).
		now := time.Now().UTC()
		th := &thought.Thought{
			ID:         fmt.Sprintf("fg-%d", now.UnixNano()),
			Prompt:     line,
			Priority:   4,
			Status:     thought.ThoughtPending,
			Foreground: foreground,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := rt.Queue.Enqueue(th); err != nil {
			logger.Error("enqueue failed", map[string]any{"error": err.Error()})
			continue
		}
		if foreground {
			foregroundID = th.ID // claims the reserved slot; steerable
		} else {
			// Headless: no foreground reservation — the thought is
			// normal work, but the session driver still tracks the
			// latest input for steering (RouteMessage works on any
			// running thought; no foreground slot involved).
			foregroundID = th.ID
		}
	}
	// EOF (Ctrl+D) ends the session; RunForeground drains the queue
	// and exits.
}

// stderrIsTTY reports whether stderr is an interactive terminal — the
// documented log-format default basis (NAVI-328): human on an
// interactive TTY, json otherwise (pipes, redirects, log files).
func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// logRoute is the resolved log destination for a run (NAVI-331):
// stderr (the console) or a file path. Exactly one of them is used.
type logRoute struct {
	stderr bool   // render to stderr (the console)
	path   string // file path ("" when stderr)
}

// resolveLogRoute applies the NAVI-331 routing decision:
//   - explicit --log-file always wins (both modes) → file;
//   - TUI mode (not --daemon) with no --log-file → default file
//     <db-dir>/navi.log (the alt-screen owns the terminal; JSON per
//     the file convention);
//   - --daemon with no --log-file → stderr (that's its console).
func resolveLogRoute(daemonMode bool, logFile, dbPath string) logRoute {
	if logFile != "" {
		return logRoute{path: logFile}
	}
	if !daemonMode {
		return logRoute{path: defaultLogPath(dbPath)}
	}
	return logRoute{stderr: true}
}

// defaultLogPath is the TUI-mode default: navi.log under the DB's
// directory (the DB path's parent — the run's data dir by convention).
func defaultLogPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "navi.log")
}
