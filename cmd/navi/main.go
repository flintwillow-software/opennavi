// Command navi is the minimal CLI entry point: dispatches subcommands
// (daemon, enqueue) or falls through to the interactive chat REPL.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	clifw "github.com/flintwillow-software/opennavi/internal/interfaces/cli"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			if err := runDaemon(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return
		case "health":
			if err := runHealth(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return

		case "enqueue":
			if err := runEnqueue(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return
		}
	}

	// Default: chat REPL.
	dbPath := flag.String("db", "./navi.db", "path to the session SQLite database")
	model := flag.String("model", "", "Ollama model name (required)")
	ollamaHost := flag.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST or http://localhost:11434)")
	workDir := flag.String("workdir", ".", "working directory for file/shell tools")
	compactBudget := flag.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	systemPrompt := flag.String("system-prompt", "", "system prompt for the agent")
	flag.Parse()

	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "navi:", err)
		os.Exit(1)
	}
	store, err := sqlite.Open(*dbPath, sqlite.Options{Cipher: cipher})
	if err != nil {
		fmt.Fprintln(os.Stderr, "navi:", err)
		os.Exit(1)
	}
	defer store.Close()

	cfg := &clifw.Config{
		DBPath:        *dbPath,
		Model:         *model,
		OllamaHost:    *ollamaHost,
		WorkDir:       *workDir,
		SystemPrompt:  *systemPrompt,
		CompactBudget: *compactBudget,
		In:            os.Stdin,
		Out:           os.Stdout,
	}
	if err := cfg.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "navi:", err)
		os.Exit(1)
	}
}
