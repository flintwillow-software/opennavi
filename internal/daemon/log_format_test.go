package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/llm"
)

// captureLog assembles a runtime with the given params (a piped logger
// is injected) and returns the lines the runtime logs through it.
func captureLog(t *testing.T, params RuntimeParams) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	params.Logger = logging.New(w, logging.Debug, nil)
	params.SessionDB = filepath.Join(t.TempDir(), "logfmt.db")
	params.OverrideProvider = llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: "x"},
		{Type: llm.StreamEventStop},
	})
	rt, aerr := Assemble(params)
	if aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	defer rt.Store.Close()
	rt.Logger.Info("knob probe", map[string]any{"k": "v"})
	w.Close()

	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func baseParams() RuntimeParams {
	return RuntimeParams{Model: "test-model", Workers: 1}
}

func TestLogFormatFlagApplies(t *testing.T) {
	// --log-format human through Assemble → the runtime logger renders
	// human lines (flag wins over config by construction — there is no
	// config in this test).
	params := baseParams()
	params.LogFormat = "human"
	out := captureLog(t, params)
	if len(out) != 1 {
		t.Fatalf("lines = %d: %v", len(out), out)
	}
	if !strings.Contains(out[0], "INFO  knob probe k=v") {
		t.Fatalf("human line not applied: %q", out[0])
	}
}

func TestLogLevelFlagApplies(t *testing.T) {
	params := baseParams()
	params.LogLevel = "error"
	params.LogFormat = "human"
	out := captureLog(t, params)
	// Info probe below the error minimum → nothing logged.
	if len(out) != 0 {
		t.Fatalf("level knob not applied: %v", out)
	}
}

func TestLoggingConfigSectionWinsOverTTYDefault(t *testing.T) {
	// Config section (human) with no flag: applied through Assemble.
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
logging:
  format: human
  level: debug
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	params := baseParams()
	params.RuntimeFile = rcPath
	out := captureLog(t, params)
	if len(out) != 1 {
		t.Fatalf("lines = %d: %v", len(out), out)
	}
	if !strings.Contains(out[0], "INFO  knob probe k=v") {
		t.Fatalf("config section not applied: %q", out[0])
	}
}

func TestLogFormatFlagWinsOverConfig(t *testing.T) {
	// Config says human; the flag says json — flag wins (merge chain).
	rcPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
logging:
  format: human
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	params := baseParams()
	params.RuntimeFile = rcPath
	params.LogFormat = "json"
	out := captureLog(t, params)
	if len(out) != 1 {
		t.Fatalf("lines = %d: %v", len(out), out)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(out[0]), &line); err != nil {
		t.Fatalf("flag must win over config (json expected): %v (%q)", err, out[0])
	}
}

func TestLoggingConfigWinsWhenFlagUnset(t *testing.T) {
	// Config says json; no flag — the config turns the knob.
	rcPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
logging:
  format: json
  level: debug
`
	if err := os.WriteFile(rcPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	params := baseParams()
	params.RuntimeFile = rcPath
	// cmd would apply a TTY default on the logger; simulate by setting
	// human first — the config section must override it.
	out := captureLogWithPresetFormat(t, params, logging.FormatHuman)
	if len(out) != 1 {
		t.Fatalf("lines = %d: %v", len(out), out)
	}
	if !strings.HasPrefix(out[0], "{") {
		t.Fatalf("config must override the TTY default: %q", out[0])
	}
}

// captureLogWithPresetFormat pre-sets the logger's format (simulating
// the cmd-layer TTY default) before Assemble applies the config.
func captureLogWithPresetFormat(t *testing.T, params RuntimeParams, preset logging.Format) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	logger := logging.New(w, logging.Debug, nil)
	logger.SetFormat(preset)
	params.Logger = logger
	params.SessionDB = filepath.Join(t.TempDir(), "logfmt.db")
	params.OverrideProvider = llm.NewMockProvider([]llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, Content: "x"},
		{Type: llm.StreamEventStop},
	})
	rt, aerr := Assemble(params)
	if aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	defer rt.Store.Close()
	rt.Logger.Info("knob probe", map[string]any{"k": "v"})
	w.Close()

	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// TestDefaultJSONUnchanged: with no knobs at all, the runtime logger
// stays JSON (the cmd-layer TTY default survives; existing behavior).
func TestDefaultJSONUnchanged(t *testing.T) {
	out := captureLog(t, baseParams())
	if len(out) != 1 {
		t.Fatalf("lines = %d", len(out))
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(out[0]), &line); err != nil {
		t.Fatalf("no-knob default must stay JSON: %v (%q)", err, out[0])
	}
}
