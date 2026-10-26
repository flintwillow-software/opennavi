package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/thought"
)

// ErrHealthFailed reports that a health probe failed. The JSON summary has
// already been printed to stdout; the caller turns this into a non-zero exit.
// A library must not exit the process itself, so the role returns the failure
// rather than calling os.Exit the way a standalone command can.
var ErrHealthFailed = errors.New("health: one or more probes failed")

// HealthResult is the JSON summary `Health` prints.
type HealthResult struct {
	Provider string      `json:"provider"`
	Store    string      `json:"store"`
	NaviID   string      `json:"navi_id,omitempty"`
	Todos    *TodoHealth `json:"todos,omitempty"`

	// Connectors is the start-time connector-health view the host wrote beside
	// the database (NAVI-329): each door's ok/disabled/failed state and, for a
	// failure, the reason (e.g. "listen :9090: address in use"). Present only
	// after the navi has started against this database.
	Connectors          []navi.ConnectorHealth `json:"connectors,omitempty"`
	ConnectorsUpdatedAt *time.Time             `json:"connectors_updated_at,omitempty"`
	ConnectorsError     string                 `json:"connectors_error,omitempty"`
}

// TodoHealth is the navi's current todo state: counts by status.
type TodoHealth struct {
	Pending    int    `json:"pending"`
	InProgress int    `json:"in_progress"`
	Done       int    `json:"done"`
	Error      string `json:"error,omitempty"`
}

// Health is the `health` client role: probes provider reachability and store
// writability, prints a JSON summary, and returns ErrHealthFailed when any
// probe failed (so the caller exits non-zero).
func Health(args []string) error {
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

	hr := HealthResult{Provider: "ok", Store: "ok", NaviID: *naviID}
	failed := false

	// Provider probe: Ollama /api/tags via the provider's Ping.
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
		defer store.Close()
		if serr := storeProbe(ctx, store); serr != nil {
			hr.Store = fmt.Sprintf("fail: %v", serr)
			failed = true
		}
		// Todo state: counts by status for the navi. A probe failure is
		// reported but does not fail the command — todo storage may
		// legitimately be untouched (no tool activity yet); only store-level
		// failures surface. The probe reads through the still-open store.
		hr.Todos = todoProbe(store, *naviID)
	}

	// Connector health: the start-time view the host wrote beside the
	// database. A missing snapshot means the navi has not started against
	// this DB; report that rather than guessing states. A connector failure is
	// informational — it does not fail the probe.
	snap, ok, herr := readHealthSnapshot(healthSnapshotPath(*dbPath))
	switch {
	case herr != nil:
		hr.ConnectorsError = herr.Error()
	case ok:
		hr.Connectors = snap.Connectors
		at := snap.UpdatedAt
		hr.ConnectorsUpdatedAt = &at
	default:
		hr.ConnectorsError = "no connector health snapshot (navi not started against this database?)"
	}

	blob, _ := json.MarshalIndent(hr, "", "  ")
	fmt.Println(string(blob))
	if failed {
		return ErrHealthFailed
	}
	return nil
}

// todoProbe reads the navi's todo counts from the stateful store.
func todoProbe(st *sqlite.Store, naviID string) *TodoHealth {
	ss, err := sqlite.NewStatefulStore(st.DB())
	if err != nil {
		return &TodoHealth{Error: fmt.Sprintf("stateful store: %v", err)}
	}
	th := &TodoHealth{}
	for _, probe := range []struct {
		status datastore.TodoStatus
		target *int
	}{
		{datastore.TodoPending, &th.Pending},
		{datastore.TodoInProgress, &th.InProgress},
		{datastore.TodoDone, &th.Done},
	} {
		items, err := ss.ListTodos(naviID, probe.status, 0)
		if err != nil {
			return &TodoHealth{Error: fmt.Sprintf("list todos (%s): %v", probe.status, err)}
		}
		*probe.target = len(items)
	}
	return th
}

// storeProbe verifies the SQLite store is writable by saving, loading, and
// deleting a sentinel thought.
func storeProbe(ctx context.Context, store *sqlite.Store) error {
	ts, err := sqlite.NewThoughtStore(store.DB())
	if err != nil {
		return fmt.Errorf("thought store: %w", err)
	}
	now := time.Now().UTC()
	sentinel := &thought.Thought{
		ID:        fmt.Sprintf("health-check-%d", now.UnixNano()),
		Prompt:    "health-check sentinel",
		Priority:  10, // lowest — never claimed before real thoughts
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

// healthSnapshot is the connector-health document the host writes beside the
// database (NAVI-329).
type healthSnapshot struct {
	Connectors []navi.ConnectorHealth `json:"connectors"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

// healthSnapshotPath is the snapshot's path for a database path: the DB path
// with the extension replaced, so one navi's health sits beside its state.
func healthSnapshotPath(dbPath string) string {
	return replaceExt(dbPath, ".health.json")
}

func replaceExt(path, ext string) string {
	base := path
	if i := lastIndexByte(base, '.'); i > lastIndexByte(base, '/') {
		base = base[:i]
	}
	return base + ext
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// readHealthSnapshot reads the snapshot, reporting ok=false when the file does
// not exist (the navi has not started against this database).
func readHealthSnapshot(path string) (healthSnapshot, bool, error) {
	var snap healthSnapshot
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, false, nil
		}
		return snap, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(blob, &snap); err != nil {
		return snap, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return snap, true, nil
}
