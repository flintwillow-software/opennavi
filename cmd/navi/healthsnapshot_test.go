package main

import (
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/navi"
)

// ── NAVI-329: connector-health snapshot ──────────────────────────────

func TestHealthSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navi.health.json")
	want := []navi.ConnectorHealth{
		{Name: "scheduler", State: navi.ConnectorOK},
		{Name: "metrics", State: navi.ConnectorFailed, Addr: ":9090", Reason: "listen :9090: address in use"},
		{Name: "portal", State: navi.ConnectorDisabled},
	}
	if err := writeHealthSnapshot(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := readHealthSnapshot(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("read reported no snapshot")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}
	if len(got.Connectors) != len(want) {
		t.Fatalf("connectors = %d, want %d", len(got.Connectors), len(want))
	}
	for i := range want {
		if got.Connectors[i] != want[i] {
			t.Errorf("connector[%d] = %+v, want %+v", i, got.Connectors[i], want[i])
		}
	}
}

func TestHealthSnapshotMissingIsNotAnError(t *testing.T) {
	_, ok, err := readHealthSnapshot(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing snapshot returned an error: %v", err)
	}
	if ok {
		t.Fatal("missing snapshot reported as found")
	}
}

func TestReferenceConnectorHealthProjectsThreeDoors(t *testing.T) {
	// Only metrics reported (as failed); scheduler and portal must be
	// filled in as disabled, and the in-process portals must not leak in.
	reported := []navi.ConnectorHealth{
		{Name: "tui", State: navi.ConnectorOK},
		{Name: "metrics", State: navi.ConnectorFailed, Addr: ":9090", Reason: "listen :9090: address in use"},
	}
	got := referenceConnectorHealth(reported)
	want := []navi.ConnectorHealth{
		{Name: "scheduler", State: navi.ConnectorDisabled},
		{Name: "metrics", State: navi.ConnectorFailed, Addr: ":9090", Reason: "listen :9090: address in use"},
		{Name: "portal", State: navi.ConnectorDisabled},
	}
	if len(got) != len(want) {
		t.Fatalf("doors = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("door[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestConnectorHealthRecorderWritesSnapshot(t *testing.T) {
	dir := t.TempDir()
	rc := config.RuntimeConfig{SessionDB: filepath.Join(dir, "navi.db")}
	rec := connectorHealthRecorder(rc, runParams{})
	rec([]navi.ConnectorHealth{
		{Name: "metrics", State: navi.ConnectorFailed, Addr: ":9090", Reason: "listen :9090: address in use"},
	})

	got, ok, err := readHealthSnapshot(healthSnapshotPath(rc.SessionDB))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("recorder did not write a snapshot")
	}
	var failed *navi.ConnectorHealth
	for i := range got.Connectors {
		if got.Connectors[i].State == navi.ConnectorFailed {
			failed = &got.Connectors[i]
		}
	}
	if failed == nil {
		t.Fatalf("no failed door in snapshot: %+v", got.Connectors)
	}
	if failed.Reason != "listen :9090: address in use" {
		t.Fatalf("reason = %q", failed.Reason)
	}
	// The disabled doors are filled in so the health view is complete.
	if len(got.Connectors) != 3 {
		t.Fatalf("doors = %d, want 3", len(got.Connectors))
	}
}

// ── NAVI-357: metrics wiring ─────────────────────────────────────────

func TestAddMetricsWiresOnlyWhenConfigured(t *testing.T) {
	n := navi.New("t")
	if err := addMetrics(n, config.RuntimeConfig{}); err != nil {
		t.Fatalf("addMetrics (unset): %v", err)
	}
	if n.Connectors.Len() != 0 {
		t.Fatalf("unset metrics_addr declared %d connectors, want 0", n.Connectors.Len())
	}
	if err := addMetrics(n, config.RuntimeConfig{MetricsAddr: " :9090 "}); err != nil {
		t.Fatalf("addMetrics (set): %v", err)
	}
	if n.Connectors.Len() != 1 {
		t.Fatalf("set metrics_addr declared %d connectors, want 1", n.Connectors.Len())
	}
}

func TestMergeRuntimeCarriesMetricsAddr(t *testing.T) {
	rc, err := mergeRuntime(runParams{dbPath: filepath.Join(t.TempDir(), "navi.db"), metricsAddr: ":9090"})
	if err != nil {
		t.Fatalf("mergeRuntime: %v", err)
	}
	if rc.MetricsAddr != ":9090" {
		t.Fatalf("MetricsAddr = %q, want :9090", rc.MetricsAddr)
	}
}

func TestMergeRuntimeCarriesWebhookAddr(t *testing.T) {
	rc, err := mergeRuntime(runParams{dbPath: filepath.Join(t.TempDir(), "navi.db"), webhookAddr: ":8081"})
	if err != nil {
		t.Fatalf("mergeRuntime: %v", err)
	}
	if rc.WebhookAddr != ":8081" {
		t.Fatalf("WebhookAddr = %q, want :8081", rc.WebhookAddr)
	}
}
