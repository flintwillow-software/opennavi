package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

// runEnqueue is the `navi enqueue` subcommand: writes a thought
// directly to the shared SQLite queue DB (daemon MVP step 3, #241).
// If the daemon isn't running, the thought is enqueued and picked up
// on next daemon start.
//
// Flags and the positional prompt can appear in any order: the args
// are partitioned manually (flag.NewFlagSet.Parse stops at the first
// positional, which would drop -db/-priority placed after the prompt).
// A token starting with "-" is a flag; everything else is a prompt
// word. Both `enqueue "prompt" -db X` and `enqueue -db X "prompt"`
// work.
func runEnqueue(args []string) error {
	fs := flag.NewFlagSet("enqueue", flag.ExitOnError)
	priority := fs.Int("priority", 4, "thought priority 1-10 (1 = critical, 4 = normal)")
	dbPath := fs.String("db", "", "path to the session SQLite database")
	provider := fs.String("provider", "", "provider override for this thought (default: runtime primary; unknown names fail at run time)")
	model := fs.String("model", "", "model override for this thought (default: runtime model)")

	// Partition: flags (with values) vs prompt positionals.
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			// Known value-taking flags consume the next token. Go's flag
			// package accepts both -flag and --flag spellings.
			switch strings.TrimLeft(arg, "-") {
			case "priority", "db", "provider", "model":
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		positional = append(positional, arg)
	}

	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(positional) == 0 {
		return fmt.Errorf("enqueue: prompt is required (navi enqueue \"prompt\" [-priority N] [-db path])")
	}
	prompt := strings.Join(positional, " ")

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
		Provider:  *provider,
		Model:     *model,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := thoughtStore.SaveThought(t); err != nil {
		return fmt.Errorf("enqueue: save: %w", err)
	}

	fmt.Printf("enqueued %s (priority %d) — daemon picks up on next start\n", t.ID, *priority)
	return nil
}
