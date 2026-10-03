package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flintwillow-software/opennavi/internal/ops/logging"
)

// runInteractive is the `navi run` subcommand: it assembles the reference
// navi through navi.New and serves it. The TUI is the front door;
// --daemon is the headless presentation, which also serves the HTTP
// portal gateway (remote portal attachments) when --portal-addr is set.
func runInteractive(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	runtimeFile := fs.String("config", "", "path to runtime config YAML (optional)")
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	model := fs.String("model", "", "LLM model name (required)")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (overrides $OLLAMA_HOST and YAML; default http://localhost:11434)")
	workDir := fs.String("workdir", ".", "working directory for file/shell tools")
	naviID := fs.String("navi-id", "default", "per-navi scoping key for stateful tool storage (todos/notes)")
	workers := fs.Int("workers", 2, "thought worker count")
	metricsAddr := fs.String("metrics-addr", "", "Prometheus metrics listen address (not yet wired on the reference navi)")
	portalAddr := fs.String("portal-addr", "", "portal observation/control listen address, e.g. :8090 (default: disabled)")
	logLevel := fs.String("log-level", "", "log level: debug, info, warn, error (default info; empty = config/default)")
	logFormat := fs.String("log-format", "", "log format: human | json (default: human on an interactive TTY, json otherwise)")
	logFile := fs.String("log-file", "", "log file path (default stderr; files log JSON)")
	daemonMode := fs.Bool("daemon", false, "headless presentation: no TUI, stdin-loop session driver, no foreground slot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("run: --model is required")
	}

	// Log routing (NAVI-331): in TUI mode the Bubble Tea alt-screen owns
	// the terminal, so logs go to <db-dir>/navi.log unless --log-file is
	// explicit; --daemon keeps stderr.
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
	human := route.stderr && stderrIsTTY()
	if v := strings.ToLower(strings.TrimSpace(*logFormat)); v != "" {
		human = v == "human"
	}
	format := logging.FormatJSON
	if human {
		format = logging.FormatHuman
	}
	logger := logging.New(lw, logging.ParseLevel(*logLevel), nil)
	logger.SetFormat(format)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if openedLogFile != "" {
		fmt.Printf("navi: logs → %s\n", openedLogFile)
	}
	if *metricsAddr != "" {
		fmt.Fprintln(os.Stderr, "navi: --metrics-addr is not wired on the reference navi yet")
	}

	return serveReferenceNavi(runParams{
		ctx:         ctx,
		runtimeFile: *runtimeFile,
		dbPath:      *dbPath,
		model:       *model,
		ollamaHost:  *ollamaHost,
		workDir:     *workDir,
		naviID:      *naviID,
		workers:     *workers,
		portalAddr:  *portalAddr,
		daemonMode:  *daemonMode,
		logger:      logger,
		slogLogger:  slogFrom(lw, human, slogLevel(*logLevel)),
		cancel:      stop,
	})
}

// slogLevel maps the runtime log-level name onto slog's level.
func slogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
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

// stderrIsTTY reports whether stderr is an interactive terminal — the
// documented log-format default basis (NAVI-328): human on an interactive
// TTY, json otherwise (pipes, redirects, log files).
func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// logRoute is the resolved log destination for a run (NAVI-331): stderr
// (the console) or a file path. Exactly one of them is used.
type logRoute struct {
	stderr bool   // render to stderr (the console)
	path   string // file path ("" when stderr)
}

// resolveLogRoute applies the NAVI-331 routing decision:
//   - explicit --log-file always wins (both modes) → file;
//   - TUI mode (not --daemon) with no --log-file → default file
//     <db-dir>/navi.log (the alt-screen owns the terminal; JSON per the
//     file convention);
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
