// Command navi is a minimal CLI entry point for the opennavi agent
// runtime (#226): an interactive chat REPL over internal/agent,
// internal/session, and the SQLite session store. Plain stdlib — the
// #141 interface abstraction will absorb/refactor this later.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
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
	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return err
	}
	store, err := sqlite.Open(opts.dbPath, sqlite.Options{Cipher: cipher})
	if err != nil {
		return err
	}
	defer store.Close()
	return runREPL(os.Stdin, os.Stdout, store, opts)
}
