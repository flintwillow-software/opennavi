package navi

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/builtin"
	toolhttp "github.com/flintwillow-software/opennavi/tool/http"
	"github.com/flintwillow-software/opennavi/tool/note"
	"github.com/flintwillow-software/opennavi/tool/thoughts"
	timepkg "github.com/flintwillow-software/opennavi/tool/time"
	"github.com/flintwillow-software/opennavi/tool/todo"
)

// daemonDefaultActiveSet is the daemon's default 13-tool active set
// (internal/daemon/wiring_test.go); catalog parity means the navi's
// collection-assembled surface reproduces it exactly.
var daemonDefaultActiveSet = []string{
	"file_read", "shell_exec",
	"todo_read", "todo_write", "todo_update",
	"note_read", "note_write",
	"time_read", "http_request",
	"thoughts_read", "thoughts_create", "thoughts_update", "thoughts_delete",
}

// catalogNavi builds a navi from the ported catalog collections.
func catalogNavi(t *testing.T, opts ...Option) *Navi {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "catalog.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	stateful, err := sqlite.NewStatefulStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	n := New("catalog-parity", opts...)
	add := func(tools []tool.Tool, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools {
			if err := n.Tools.Add(tl); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(builtin.Tools(t.TempDir()))
	add(todo.Tools("catalog-parity", stateful))
	add(note.Tools("catalog-parity", stateful))
	one, err := timepkg.Tool()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Tools.Add(one); err != nil {
		t.Fatal(err)
	}
	hp, err := toolhttp.Tool()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Tools.Add(hp); err != nil {
		t.Fatal(err)
	}
	add(thoughts.Tools(n.Thoughts))
	return n
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalNameSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := sortedCopy(a), sortedCopy(b)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// TestCatalogParityDefaultActiveSet is the M4 gate: the catalog ported
// onto the collections assembles the same default active set the daemon
// does.
func TestCatalogParityDefaultActiveSet(t *testing.T) {
	n := catalogNavi(t)
	if err := n.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}

	got := n.registry.ActiveNames()
	if !equalNameSets(got, daemonDefaultActiveSet) {
		t.Fatalf("navi default active set = %v, want the daemon's 13 %v", got, daemonDefaultActiveSet)
	}
}

// TestCatalogParityCeilingNarrowsDefault pins the compiled permission
// ceiling behaviour: denying shell_exec at the navi drops it from the
// default surface, exactly as the daemon's blocked-tool test does (12
// tools).
func TestCatalogParityCeilingNarrowsDefault(t *testing.T) {
	n := catalogNavi(t, WithPermissions(PermissionPolicy{
		DenyTools: []string{"shell_exec"},
	}))
	if err := n.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}

	got := n.registry.ActiveNames()
	for _, name := range got {
		if name == "shell_exec" {
			t.Fatal("ceiling-denied shell_exec is active")
		}
	}
	if len(got) != len(daemonDefaultActiveSet)-1 {
		t.Fatalf("active set = %v (len %d), want 12", got, len(got))
	}
}
