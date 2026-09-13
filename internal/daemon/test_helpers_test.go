package daemon

import (
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/thought"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

// testStore opens a minimal sqlite store over a temp DB (test helper
// for packages that need the raw store).
func testStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// claimAndKill simulates a kill -9: the thought was claimed (running)
// in the DB but the process died before completing.
func claimAndKill(dbPath, thoughtID string) {
	st, err := sqlite.Open(dbPath, sqlite.Options{})
	if err != nil {
		return
	}
	defer st.Close()
	ts, err := sqlite.NewThoughtStore(st.DB())
	if err != nil {
		return
	}
	got, err := ts.LoadThought(thoughtID)
	if err != nil {
		return
	}
	got.Status = thought.ThoughtRunning
	got.SlotID = "slot-1"
	got.WorkerID = "worker-dead"
	_ = ts.SaveThought(got)
}
