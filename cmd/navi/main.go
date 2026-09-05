// Command navi is the minimal CLI entry point: parses flags into an
// InterfaceSet, selects enabled interfaces from the registry, and runs
// them (#141). The chat REPL itself lives in internal/interfaces/cli.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/flintwillow-software/opennavi/internal/interfaces"
	cliiface "github.com/flintwillow-software/opennavi/internal/interfaces/cli"
)

func main() {
	dbPath := flag.String("db", "./navi.db", "path to the session SQLite database")
	model := flag.String("model", "", "Ollama model name (required)")
	ollamaHost := flag.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST or http://localhost:11434)")
	workDir := flag.String("workdir", ".", "working directory for file/shell tools")
	compactBudget := flag.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	systemPrompt := flag.String("system-prompt", "", "system prompt for the agent")
	flag.Parse()

	if err := run(runOpts{
		dbPath:        *dbPath,
		model:         *model,
		ollamaHost:    *ollamaHost,
		workDir:       *workDir,
		compactBudget: *compactBudget,
		systemPrompt:  *systemPrompt,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "navi:", err)
		os.Exit(1)
	}
}

// runOpts carries parsed flags; a struct keeps run() testable.
type runOpts struct {
	dbPath        string
	model         string
	ollamaHost    string
	workDir       string
	compactBudget int
	systemPrompt  string
}

func run(opts runOpts) error {
	if opts.model == "" {
		return fmt.Errorf("--model is required (e.g. -model qwen3.8:27b)")
	}
	if opts.ollamaHost != "" {
		os.Setenv("OLLAMA_HOST", opts.ollamaHost)
	}

	// Interface selection: the CLI interface is the only registered
	// interface today; WebUI/Gateway/Cron adapters register here as
	// they land (#141 follow-ups), and the builder (#140) will drive
	// compile-time pruning.
	registry := interfaces.NewRegistry()
	registry.Register("cli", func(cfg interfaces.InterfaceConfig) (interfaces.Interface, error) {
		return &cliiface.Config{
			DBPath:        opts.dbPath,
			Model:         opts.model,
			OllamaHost:    opts.ollamaHost,
			WorkDir:       opts.workDir,
			SystemPrompt:  opts.systemPrompt,
			CompactBudget: opts.compactBudget,
			In:            os.Stdin,
			Out:           os.Stdout,
		}, nil
	})

	set := interfaces.InterfaceSet{CLI: true}
	ifaces, err := registry.Select(set)
	if err != nil {
		return err
	}
	return interfaces.RunAll(context.Background(), ifaces)
}

// keep the sqlite import anchored: future interface constructors (web
// UI, gateway) will open the session store themselves; the CLI opens
// it inside its Run. Referenced indirectly via the cli package's store
// usage — drop this var when a second interface needs the shared DB.
