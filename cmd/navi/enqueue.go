package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// runEnqueue is the `navi enqueue` subcommand: writes a thought
// directly to the shared SQLite queue DB (daemon MVP step 3, #241).
// If the daemon isn't running, the thought is enqueued and picked up
// on next daemon start.
func runEnqueue(args []string) error {
	fs := flag.NewFlagSet("enqueue", flag.ExitOnError)
	priority := fs.Int("priority", 4, "thought priority 1-10 (1 = critical, 4 = normal)")
	dbPath := fs.String("db", "", "path to the session SQLite database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("enqueue: prompt is required (navi enqueue \"prompt\" [-priority N] [-db path])")
	}
	prompt := strings.Join(rest, " ")

	if *priority < 1 || *priority > 10 {
		return fmt.Errorf("enqueue: priority %d out of range [1..10]", *priority)
	}
	if *dbPath == "" {
		return fmt.Errorf("enqueue: -db path is required")
	}

	cipher, err := sqlite.CipherFromEnv()
	if err != nil {
		return err
	}
	store, err := sqlite.Open(*dbPath, sqlite.Options{Cipher: cipher})
	if err != nil {
		return fmt.Errorf("enqueue: open db %s: %w", *dbPath, err)
	}
	defer store.Close()

	thoughtStore, err := sqlite.NewThoughtStore(store.DB())
	if err != nil {
		return fmt.Errorf("enqueue: thought store: %w", err)
	}

	now := time.Now().UTC()
	t := &thought.Thought{
		ID:        fmt.Sprintf("cli-%d", now.UnixNano()),
		Prompt:    prompt,
		Priority:  *priority,
		Status:    thought.ThoughtPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := thoughtStore.SaveThought(t); err != nil {
		return fmt.Errorf("enqueue: save: %w", err)
	}

	fmt.Printf("enqueued %s (priority %d) — daemon picks up on next start\n", t.ID, *priority)
	return nil
}
