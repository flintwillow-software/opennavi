package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
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
	dbPath := fs.String("db", "", "path to the session SQLite database (the durable mailbox the fallback writes)")
	provider := fs.String("provider", "", "provider override for this thought (default: runtime primary; unknown names fail at run time)")
	model := fs.String("model", "", "model override for this thought (default: runtime model)")
	// Portal channel resolution order (NAVI-337): explicit --portal
	// wins, then the NAVI_PORTAL env var. With a known portal the
	// control channel is tried first (2s) and the DB mailbox is the
	// fallback on refusal/unreachability; without one, the DB write is
	// the direct path. provider/model carry over the DB path only (the
	// portal channel's shim does not carry them yet).
	portal := fs.String("portal", "", "portal control endpoint (http://host:port); tries the control channel first, falls back to the DB mailbox")
	if *portal == "" {
		if env := os.Getenv("NAVI_PORTAL"); env != "" {
			portal = &env
		}
	}

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
			case "priority", "db", "provider", "model", "portal":
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

	// Portal-first channel (NAVI-337): when a portal endpoint is
	// known — and the request is expressible on it (provider/model
	// ride the DB path only until the shim carries them) — POST the
	// enqueue control; the navi's ack confirms delivery. Refusals
	// (403 fail-closed until IAM) and unreachable endpoints fall back
	// to the DB mailbox with a visible note.
	var note string
	if *portal != "" && *provider == "" && *model == "" {
		if ok, perr := enqueueViaPortal(*portal, t.ID, t.Prompt, t.EffectivePriority(), time.Time{}); ok {
			fmt.Printf("enqueued %s via portal %s (priority %d)\n", t.ID, *portal, *priority)
			return nil
		} else {
			note = fmt.Sprintf("portal %s: %v — persisted to store\n", *portal, perr)
		}
	} else if *portal != "" {
		note = "portal channel does not carry provider/model yet — persisted to store\n"
	}

	if err := thoughtStore.SaveThought(t); err != nil {
		return fmt.Errorf("enqueue: save: %w", err)
	}
	if note != "" {
		fmt.Printf("note: %s", note)
	}
	fmt.Printf("enqueued %s (priority %d) — daemon picks up on next start (or NAVI-336 reconcile)\n", t.ID, *priority)
	return nil
}

// enqueueViaPortal POSTs the enqueue control action to a navi's
// portal endpoint. Priority/deferral ride along (the shim consumes
// priority; defer_until is forwarded for forward-compat and ignored
// until the shim learns it). Returns ok=true on the shim's 200 ack,
// or the refusal/unreachability reason for the DB fallback.
func enqueueViaPortal(addr, id, prompt string, prio int, notBefore time.Time) (bool, error) {
	payload := map[string]any{"action": "enqueue", "id": id, "prompt": prompt}
	if prio > 0 {
		payload["priority"] = prio
	}
	if !notBefore.IsZero() {
		payload["defer_until"] = notBefore.UTC().Format(time.RFC3339Nano)
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimSuffix(addr, "/")+"/portal/control?identity=cli", bytes.NewReader(blob))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("unauthorized (fail closed — no authorizer on the navi)")
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("http %d", resp.StatusCode)
	}
	return true, nil
}
