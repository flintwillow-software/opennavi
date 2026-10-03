package navi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
)

func otherCandidate() datastore.Candidate {
	return datastore.Candidate{
		Name: "other",
		Open: func(datastore.Params) (datastore.Backend, error) { return inmem.New(), nil },
	}
}

func TestDatastoreActivateBindsDeclaredCandidate(t *testing.T) {
	n := New("ds-test")
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		t.Fatal(err)
	}
	if err := n.Datastores.activate(datastore.Config{}); err != nil {
		t.Fatal(err)
	}
	if !n.Datastores.Handle().Bound() {
		t.Fatal("declaring a candidate must bind the handle at assembly")
	}
}

func TestDatastoreActivateRejectsUncompiledBackend(t *testing.T) {
	n := New("ds-test")
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		t.Fatal(err)
	}
	err := n.Datastores.activate(datastore.Config{Backend: "postgres"})
	if err == nil {
		t.Fatal("config naming an uncompiled backend must fail")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error should name the requested backend: %v", err)
	}
}

func TestDatastoreActivateRejectsRoutingAwayFromActive(t *testing.T) {
	n := New("ds-test")
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		t.Fatal(err)
	}
	if err := n.Datastores.Add(otherCandidate()); err != nil {
		t.Fatal(err)
	}
	err := n.Datastores.activate(datastore.Config{
		Backend: "inmem",
		Routing: map[datastore.Aggregate]string{datastore.AggregateMemory: "other"},
	})
	if err == nil {
		t.Fatal("routing away from the active backend must fail until NAVI-366")
	}
}

func TestDatastoreActivateRequiresCandidateWhenClaimed(t *testing.T) {
	n := New("ds-test")
	// A consumer takes a view but no candidate is compiled in.
	_ = n.Datastores.Handle().Stateful()
	err := n.Datastores.activate(datastore.Config{})
	if err == nil {
		t.Fatal("a claimed handle with no candidate must fail at assembly")
	}
}

func TestDatastoreActivateAllowsNoCandidateWhenUnclaimed(t *testing.T) {
	n := New("ds-test")
	if err := n.Datastores.activate(datastore.Config{}); err != nil {
		t.Fatalf("an unused datastore with no candidates is fine: %v", err)
	}
	if n.Datastores.Handle().Bound() {
		t.Fatal("nothing should be bound")
	}
}

// TestRunFailsWhenRuntimeConfigNamesUncompiledBackend pins the acceptance
// criterion at the Run boundary, not just at activate.
func TestRunFailsWhenRuntimeConfigNamesUncompiledBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("datastore:\n  backend: postgres\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	n := New("ds-run-test",
		WithProvider(llm.NewMockProvider()),
		WithRuntimeConfig(path),
	)
	if err := n.Datastores.Add(inmem.Candidate()); err != nil {
		t.Fatal(err)
	}

	err := n.Run(t.Context())
	if err == nil {
		t.Fatal("Run must fail when config names an uncompiled backend")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error should name the requested backend: %v", err)
	}
}
