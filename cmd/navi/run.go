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
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/internal/daemon"
	"github.com/flintwillow-software/opennavi/internal/interfaces/tui"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// runInteractive is the `navi run` subcommand — the binary-run entry
// (S010003 §06): the same thought queue, pool, and registry-governed
// tools as `navi daemon`, driven in-process with no connectors. The
// session's current thought occupies a foreground slot; by default the
// Bubble Tea TUI is the front door (S000406, NAVI-319), attached
// in-process over the portal; stdin lines enqueue durable thoughts
// (first input) or steer the in-flight run (RouteMessage, turn-boundary
// injection); Ctrl+C parks the foreground thought with its checkpoint
// (never kill-and-lose); the process exits when the session ends and
// the queue drains. --headless keeps the classic stdin loop (TUI
// skipped) for pipes/automation.
func runInteractive(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	runtimeFile := fs.String("config", "", "path to runtime config YAML (optional)")
	dbPath := fs.String("db", "./navi.db", "path to the session SQLite database")
	model := fs.String("model", "", "LLM model name (required)")
	ollamaHost := fs.String("ollama-host", "", "Ollama base URL (default $OLLAMA_HOST)")
	workDir := fs.String("workdir", ".", "working directory for file/shell tools")
	naviID := fs.String("navi-id", "default", "per-navi scoping key for stateful tool storage (todos/notes)")
	compactBudget := fs.Int("compact-budget", 0, "context token budget for compaction (0 = disabled)")
	workers := fs.Int("workers", 2, "thought worker count")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	logFile := fs.String("log-file", "", "log file path (default stderr)")
	headless := fs.Bool("headless", false, "run the classic stdin loop instead of the TUI")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("run: --model is required")
	}

	var lw io.Writer = os.Stderr
	if *logFile != "" {
		f, ferr := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if ferr != nil {
			return fmt.Errorf("run: open log file %s: %w", *logFile, ferr)
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
		Logger:        logger,
		NaviID:        *naviID,
		Foreground:    true, // binary run: in-process queue, foreground slot
		CompactBudget: *compactBudget,
		Workers:       *workers,
	})
	if err != nil {
		return err
	}
	defer rt.Store.Close()
	logger.Info("binary run started", map[string]any{"model": *model, "db": *dbPath})

	sessionDone := make(chan struct{})
	runDone := make(chan error, 1)
	go func() { runDone <- rt.RunForeground(ctx, sessionDone) }()

	// Session driver: the Bubble Tea TUI is the front door (S000406,
	// NAVI-319), attached in-process over the portal; --headless keeps
	// the classic stdin loop for pipes/automation.
	var tuiRun error
	if *headless {
		go readSession(rt, logger, sessionDone)
		fmt.Println("navi binary run — type a message; Ctrl+C parks the current thought; Ctrl+D ends the session")
	} else {
		tport, terr := tui.NewInProcess(rt.Queue)
		if terr != nil {
			return terr
		}
		tuiRun = tui.Run(tport) // Bubble Tea owns the terminal; quitting ends the session
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
// becomes the foreground thought (durable, same lifecycle as daemon
// work); lines while it runs steer it at the turn boundary via
// RouteMessage; EOF (Ctrl+D) ends the session — the run then drains
// and exits. Exits early on ctx cancellation so Ctrl+C doesn't wait
// on stdin.
func readSession(rt *daemon.Runtime, logger *logging.Logger, done chan<- struct{}) {
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
		// same lifecycle/persistence as daemon-enqueued work, claiming
		// the foreground slot.
		now := time.Now().UTC()
		th := &thought.Thought{
			ID:         fmt.Sprintf("fg-%d", now.UnixNano()),
			Prompt:     line,
			Priority:   4,
			Status:     thought.ThoughtPending,
			Foreground: true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := rt.Queue.Enqueue(th); err != nil {
			logger.Error("enqueue failed", map[string]any{"error": err.Error()})
			continue
		}
		foregroundID = th.ID
	}
	// EOF (Ctrl+D) ends the session; RunForeground drains the queue
	// and exits.
}
