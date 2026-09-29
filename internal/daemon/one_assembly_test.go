package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/llm"
)

// ── NAVI-325: one assembly + dead --ollama-host flag ─────────────────

// TestOllamaHostFlagBeatsEnvBeatsYAML: the --ollama-host flag is a
// flags-layer override — it must reach the provider endpoint over
// OLLAMA_HOST env and YAML config (the flag was DEAD before NAVI-325:
// RuntimeParams.OllamaHost never entered MergeRuntime).
func TestOllamaHostFlagBeatsEnvBeatsYAML(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://env-host:11434")
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: ollama
  model: test-model
  endpoint: http://yaml-host:11434
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	rt, err := Assemble(RuntimeParams{
		RuntimeFile: rcPath,
		SessionDB:   filepath.Join(t.TempDir(), "host.db"),
		Model:       "test-model",
		OllamaHost:  "http://flag-host:11434",
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "x"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	// The flag landed in the merged runtime config (top layer).
	if got := rt.Runtime.Provider.Endpoint; got != "http://flag-host:11434" {
		t.Fatalf("endpoint = %q, want flag-host (flag > env > YAML)", got)
	}
}

// TestOllamaHostEnvBeatsYAML: without the flag, the env layer wins
// over the YAML layer (pre-existing chain, now observable end-to-end).
func TestOllamaHostEnvBeatsYAML(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://env-host:11434")
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: ollama
  model: test-model
  endpoint: http://yaml-host:11434
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	rt, err := Assemble(RuntimeParams{
		SessionDB:   filepath.Join(t.TempDir(), "host-env.db"),
		Model:       "test-model",
		RuntimeFile: rcPath,
		OverrideProvider: llm.NewMockProvider([]llm.StreamEvent{
			{Type: llm.StreamEventContentDelta, Content: "x"},
			{Type: llm.StreamEventStop},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()
	if got := rt.Runtime.Provider.Endpoint; got != "http://env-host:11434" {
		t.Fatalf("endpoint = %q, want env-host (env > YAML)", got)
	}
}

// TestOneAssemblyConnectorsInEveryMode: the run modes assemble the
// SAME runtime — connectors (cron scheduler, metrics, portal) are
// present whether or not a session is attached; the foreground slot
// is reserved only when a session (TUI) exists (NAVI-325).
func TestOneAssemblyConnectorsInEveryMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		foreground  bool
		wantFGSlots int
	}{
		{"tui-session-attached", true, 1},
		{"headless-daemon", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := Assemble(RuntimeParams{
				SessionDB:        filepath.Join(t.TempDir(), "one.db"),
				Model:            "test-model",
				Workers:          1,
				Foreground:       tc.foreground,
				OverrideProvider: llm.NewMockProvider(binaryEvents("ok")),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Store.Close()

			// Connectors: the cron scheduler assembles in both modes.
			if rt.Scheduler == nil {
				t.Fatalf("%s: cron scheduler not assembled", tc.name)
			}

			// The foreground reservation follows the session, not the
			// mode: 1 slot with a session attached, 0 in headless.
			fg := 0
			for _, s := range rt.Queue.Slots() {
				if s.Foreground {
					fg++
				}
			}
			if fg != tc.wantFGSlots {
				t.Fatalf("%s: foreground slots = %d, want %d", tc.name, fg, tc.wantFGSlots)
			}

			// The steering bridge is registered in both modes (one
			// assembly) — messages may target any running thought.
			if rt.inflight == nil {
				t.Fatalf("%s: steering bridge not wired", tc.name)
			}
		})
	}
}
