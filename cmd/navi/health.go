package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

// healthResult is the JSON summary printed by `navi health`.
type healthResult struct {
	Provider string `json:"provider"`
	Store    string `json:"store"`
}

// runHealth is the `navi health` subcommand: probes provider
// reachability and store writability, prints a JSON summary, exits
// non-zero on any failure.
func runHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST)")
	timeout := fs.Duration("timeout", 5*time.Second, "per-probe timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	hr := healthResult{Provider: "ok", Store: "ok"}
	failed := false

	// Provider probe: Ollama /api/tags via llm.OllamaProvider.Ping.
	host := *ollamaHost
	if host == "" {
		host = llm.OllamaHostFromEnv()
	}
	provider := llm.NewOllamaProviderAt(host)
	if perr := provider.Ping(ctx); perr != nil {
		hr.Provider = fmt.Sprintf("fail: %v", perr)
		failed = true
	}

	// Store probe: open + round-trip a sentinel thought.
	store, serr := sqlite.Open(*dbPath, sqlite.Options{})
	if serr != nil {
		hr.Store = fmt.Sprintf("fail: %v", serr)
		failed = true
	} else {
		if serr := storeProbe(ctx, store); serr != nil {
			hr.Store = fmt.Sprintf("fail: %v", serr)
			failed = true
		}
		store.Close()
	}

	blob, _ := json.MarshalIndent(hr, "", "  ")
	fmt.Println(string(blob))
	if failed {
		os.Exit(1)
	}
	return nil
}

// storeProbe verifies the SQLite store is writable by saving, loading,
// and deleting a sentinel thought.
func storeProbe(ctx context.Context, store *sqlite.Store) error {
	ts, err := sqlite.NewThoughtStore(store.DB())
	if err != nil {
		return fmt.Errorf("thought store: %w", err)
	}
	now := time.Now().UTC()
	sentinel := &thought.Thought{
		ID:        fmt.Sprintf("health-check-%d", now.UnixNano()),
		Prompt:    "health-check sentinel",
		Priority:  10, // lowest — will never be claimed before real thoughts
		Status:    thought.ThoughtPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ts.SaveThought(sentinel); err != nil {
		return fmt.Errorf("save sentinel: %w", err)
	}
	got, err := ts.LoadThought(sentinel.ID)
	if err != nil {
		return fmt.Errorf("load sentinel: %w", err)
	}
	if got.ID != sentinel.ID {
		return fmt.Errorf("sentinel ID mismatch: got %q, want %q", got.ID, sentinel.ID)
	}
	if err := ts.DeleteThought(sentinel.ID); err != nil {
		return fmt.Errorf("delete sentinel: %w", err)
	}
	return nil
}
