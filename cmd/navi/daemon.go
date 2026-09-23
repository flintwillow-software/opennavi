package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/flintwillow-software/opennavi/internal/daemon"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
)

// runDaemon is the `navi daemon` subcommand: a long-running process
// that runs the thought queue, worker pool, and cron scheduler against
// the configured agent. SIGINT/SIGTERM cancel the context; the daemon
// drains in-flight thoughts (park-with-checkpoint) and exits.
func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	runtimeFile := fs.String("config", "", "path to runtime config YAML (optional)")
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	model := fs.String("model", "", "LLM model name (required)")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST)")
	workDir := fs.String("workdir", ".", "working directory for file/shell tools")
	naviID := fs.String("navi-id", "default", "per-navi scoping key for stateful tool storage (todos/notes)")
	compactBudget := fs.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	workers := fs.Int("workers", 2, "thought worker count")
	metricsAddr := fs.String("metrics-addr", "", "Prometheus metrics listen address, e.g. :9090 (default: disabled)")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	logFile := fs.String("log-file", "", "log file path (default stderr)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("daemon: --model is required")
	}

	// Structured logger.
	var lw io.Writer = os.Stderr
	if *logFile != "" {
		f, ferr := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if ferr != nil {
			return fmt.Errorf("daemon: open log file %s: %w", *logFile, ferr)
		}
		defer f.Close()
		lw = f
	}
	logger := logging.New(lw, logging.ParseLevel(*logLevel), nil)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := daemon.Assemble(daemon.RuntimeParams{
		RuntimeFile:   *runtimeFile,
		SessionDB:     *dbPath,
		Model:         *model,
		OllamaHost:    *ollamaHost,
		WorkDir:       *workDir,
		CompactBudget: *compactBudget,
		Workers:       *workers,
		Logger:        logger,
		NaviID:        *naviID,
		MetricsAddr:   *metricsAddr,
	})
	if err != nil {
		return err
	}
	logger.Info("daemon started", map[string]any{"model": *model, "db": *dbPath})

	fmt.Println("navi daemon running — Ctrl+C to stop")
	runErr := rt.Run(ctx)
	if cerr := rt.Store.Close(); cerr != nil {
		fmt.Fprintf(os.Stderr, "navi daemon: store close: %v\n", cerr)
	}
	logger.Info("daemon stopped", nil)
	return runErr
}
