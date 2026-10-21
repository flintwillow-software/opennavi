package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flintwillow-software/opennavi/navi"
)

// Connector health snapshot (NAVI-329). The running navi records its
// start-time connector health to a small JSON file beside its database,
// and `navi health` (a separate process) reads it back. A file rather
// than a database table because this is a runtime snapshot of the last
// start, not durable navi state — the same reasoning that puts navi.log
// beside navi.db. The write is atomic (temp + rename) so a concurrent
// reader never sees a partial document.

// healthSnapshot is the on-disk document.
type healthSnapshot struct {
	UpdatedAt  time.Time              `json:"updated_at"`
	Connectors []navi.ConnectorHealth `json:"connectors"`
}

// healthSnapshotPath is the snapshot's location: beside the session
// database, so `navi health -db <path>` finds the same file the host
// wrote.
func healthSnapshotPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "navi.health.json")
}

// writeHealthSnapshot persists the connector-health view atomically.
func writeHealthSnapshot(path string, connectors []navi.ConnectorHealth) error {
	doc := healthSnapshot{UpdatedAt: time.Now().UTC(), Connectors: connectors}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("health snapshot: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("health snapshot: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("health snapshot: rename %s: %w", path, err)
	}
	return nil
}

// readHealthSnapshot loads the connector-health view. A missing file is
// not an error: the navi may simply never have started against this
// database. The bool reports whether a snapshot was found.
func readHealthSnapshot(path string) (healthSnapshot, bool, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return healthSnapshot{}, false, nil
		}
		return healthSnapshot{}, false, fmt.Errorf("health snapshot: read %s: %w", path, err)
	}
	var doc healthSnapshot
	if err := json.Unmarshal(blob, &doc); err != nil {
		return healthSnapshot{}, false, fmt.Errorf("health snapshot: parse %s: %w", path, err)
	}
	return doc, true, nil
}
