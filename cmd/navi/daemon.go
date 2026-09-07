package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flintwillow-software/opennavi/internal/daemon"
)

// runDaemon is the `navi daemon` subcommand: a long-running process
// that runs the thought queue, worker pool, and cron scheduler against
// the configured agent. SIGINT/SIGTERM cancel the context; the daemon
// drains in-flight thoughts (park-with-checkpoint) and exits.
func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	defPath := fs.String("definition", "", "path to navi.yaml (optional; minimal built-in definition used otherwise)")
	runtimeFile := fs.String("config", "", "path to runtime config YAML (optional)")
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	model := fs.String("model", "", "LLM model name (required)")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST)")
	workDir := fs.String("workdir", ".", "working directory for file/shell tools")
	compactBudget := fs.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	workers := fs.Int("workers", 2, "thought worker count")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("daemon: --model is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := daemon.Assemble(daemon.RuntimeParams{
		DefinitionPath: *defPath,
		RuntimeFile:    *runtimeFile,
		SessionDB:      *dbPath,
		Model:          *model,
		OllamaHost:     *ollamaHost,
		WorkDir:        *workDir,
		CompactBudget:  *compactBudget,
		Workers:        *workers,
	})
	if err != nil {
		return err
	}

	fmt.Println("navi daemon running — Ctrl+C to stop")
	return rt.Run(ctx)
}
