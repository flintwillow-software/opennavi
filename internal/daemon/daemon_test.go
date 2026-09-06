package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
)

// testStore assembles a minimal Runtime over a temp DB and returns its
// sqlite store (memory tests share the DB via New(sqlite store's DB)).
func testStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "daemon.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testParams(t *testing.T) RuntimeParams {
	t.Helper()
	return RuntimeParams{
		SessionDB: filepath.Join(t.TempDir(), "daemon.db"),
		Model:     "test-model",
		Workers:   2,
	}
}

func TestAssembleMinimal(t *testing.T) {
	rt, err := Assemble(testParams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Def.Name != "navi" {
		t.Fatalf("def = %+v", rt.Def)
	}
	if rt.Queue == nil || rt.Pool == nil {
		t.Fatal("queue/pool not assembled")
	}
}

func TestAssembleWithDefinition(t *testing.T) {
	defPath := filepath.Join(t.TempDir(), "navi.yaml")
	yaml := `
name: daemon-navi
version: 1.0.0
tools:
  - name: file_read
    description: Read files
thought_queue:
  max_concurrent: 2
  priority_slots: 1
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Assemble(RuntimeParams{
		DefinitionPath: defPath,
		SessionDB:      filepath.Join(t.TempDir(), "daemon.db"),
		Model:          "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Def.Name != "daemon-navi" {
		t.Fatalf("def name = %q", rt.Def.Name)
	}
}

func TestAssembleInvalidDefinitionFails(t *testing.T) {
	defPath := filepath.Join(t.TempDir(), "navi.yaml")
	if err := os.WriteFile(defPath, []byte("name: BAD_NAME"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Assemble(RuntimeParams{
		DefinitionPath: defPath,
		SessionDB:      filepath.Join(t.TempDir(), "daemon.db"),
		Model:          "test-model",
	}); err == nil {
		t.Fatal("invalid definition should fail assembly")
	}
}
