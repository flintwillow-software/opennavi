// Command navi-sqlite is a starter navi whose state lives in one SQLite
// file: it composes a navi through navi.New, declares the sqlite datastore
// candidate as this binary's default, and registers the stateful todo tool
// against the bound datastore — so thoughts, transcripts, memory, and todos
// persist transactionally in a single database that survives restarts.
//
// Authoring model: the Go definition below is the static, compiled-in half.
// Deployment settings (provider endpoints, credential references, a
// different `datastore.backend`) come from the config file named by
// NAVI_CONFIG, if set.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "navi-sqlite:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbPath := os.Getenv("NAVI_DB")
	if dbPath == "" {
		dbPath = filepath.Join("var", "navi-sqlite.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}

	opts := []navi.Option{
		navi.WithVersion("0.1.0"),
		navi.WithPersonality("You are navi-sqlite, a small assistant whose state lives in one " +
			"SQLite file. Keep answers terse and durable work in the todo tool."),
	}
	if cfg := os.Getenv("NAVI_CONFIG"); cfg != "" {
		opts = append(opts, navi.WithRuntimeConfig(cfg))
	}
	n := navi.New("navi-sqlite", opts...)

	// The datastore is a compiled candidate, selected at runtime (ADR 0011).
	// This binary ships exactly one candidate and marks it default, so no
	// config is required; a `datastore.params.path` parameter still overrides
	// where the file lives.
	candidate := sqlite.CandidateAt(dbPath)
	candidate.Default = true
	if err := n.Datastores.Add(candidate); err != nil {
		return err
	}

	// Builtins: file_read + shell_exec, bound to the working directory.
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	builtins, err := builtin.Tools(workDir)
	if err != nil {
		return err
	}
	for _, t := range builtins {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}

	// The todo tool is stateful: it reads and writes the bound datastore's
	// tool-state aggregate, so todos live in the same SQLite file as the
	// queue's thoughts and the navi's memory.
	todos, err := todo.Tools("default", n.Datastores.Handle().Stateful())
	if err != nil {
		return err
	}
	for _, t := range todos {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}

	// One model, marked default (ADR 0009). Runtime config may retarget the
	// provider/model; the declared set is what model_list reports.
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		return err
	}

	// The TUI is the front door (ADR 0002: portals).
	if err := n.Portals.Add(tui.NewPortal()); err != nil {
		return err
	}

	return n.Run(ctx)
}
