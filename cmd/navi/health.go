package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
)

// healthResult is the JSON summary printed by `navi health`.
type healthResult struct {
	Provider string      `json:"provider"`
	Store    string      `json:"store"`
	NaviID   string      `json:"navi_id,omitempty"`
	Todos    *todoHealth `json:"todos,omitempty"`
}

// todoHealth is the navi's current todo state: counts
// by status surfaced in the health view.
type todoHealth struct {
	Pending    int    `json:"pending"`
	InProgress int    `json:"in_progress"`
	Done       int    `json:"done"`
	Error      string `json:"error,omitempty"`
}

// runHealth is the `navi health` subcommand: probes provider
// reachability and store writability, prints a JSON summary, exits
// non-zero on any failure.
func runHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	naviID := fs.String("navi-id", "default", "per-navi scoping key for stateful tool state")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST)")
	timeout := fs.Duration("timeout", 5*time.Second, "per-probe timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	hr := healthResult{Provider: "ok", Store: "ok", NaviID: *naviID}
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

	// Todo state: counts by status for the navi. A
	// probe failure is reported but does not fail the health command —
	// todo storage may legitimately be untouched (no tool activity
	// yet); only store-level failures surface.
	if store != nil {
		hr.Todos = todoProbe(store, *naviID)
	}

	blob, _ := json.MarshalIndent(hr, "", "  ")
	fmt.Println(string(blob))
	if failed {
		os.Exit(1)
	}
	return nil
}

// todoProbe reads the navi's todo counts from the stateful store.
func todoProbe(st *sqlite.Store, naviID string) *todoHealth {
	ss, err := sqlite.NewStatefulStore(st.DB())
	if err != nil {
		return &todoHealth{Error: fmt.Sprintf("stateful store: %v", err)}
	}
	th := &todoHealth{}
	for _, probe := range []struct {
		status store.TodoStatus
		target *int
	}{
		{store.TodoPending, &th.Pending},
		{store.TodoInProgress, &th.InProgress},
		{store.TodoDone, &th.Done},
	} {
		items, err := ss.ListTodos(naviID, probe.status, 0)
		if err != nil {
			return &todoHealth{Error: fmt.Sprintf("list todos (%s): %v", probe.status, err)}
		}
		*probe.target = len(items)
	}
	return th
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
