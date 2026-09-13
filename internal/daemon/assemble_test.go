package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if rt.Queue == nil || rt.Pool == nil {
		t.Fatal("queue/pool not assembled")
	}
	if rt.Scheduler == nil {
		t.Fatal("scheduler not assembled (nil runtime cron must still wire a scheduler)")
	}
}

func TestAssembleWithRuntimeConfig(t *testing.T) {
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
thought_queue:
  max_concurrent: 2
  priority_slots: 1
cron:
  schedules:
    - name: health-check
      expr: "*/5 * * * *"
      prompt: "Check service health."
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Assemble(RuntimeParams{
		RuntimeFile: rcPath,
		SessionDB:   filepath.Join(t.TempDir(), "daemon.db"),
		Model:       "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if rt.Runtime.ThoughtQueue == nil || rt.Runtime.ThoughtQueue.MaxConcurrent != 2 {
		t.Fatalf("thought queue = %+v", rt.Runtime.ThoughtQueue)
	}
	if rt.Runtime.Cron == nil || len(rt.Runtime.Cron.Schedules) != 1 {
		t.Fatalf("cron = %+v", rt.Runtime.Cron)
	}
}

func TestAssembleInvalidRuntimeConfigFails(t *testing.T) {
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: mock
thought_queue:
  max_concurrent: -3
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Assemble(RuntimeParams{
		RuntimeFile: rcPath,
		SessionDB:   filepath.Join(t.TempDir(), "daemon.db"),
		Model:       "test-model",
	}); err == nil {
		t.Fatal("invalid runtime config should fail assembly")
	}
}
