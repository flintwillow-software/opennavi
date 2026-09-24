package config

import (
	"testing"
)

// ── NAVI-328: logging:{format, level} config section ─────────────────

func TestLoggingSectionMergesAndFlagWins(t *testing.T) {
	file := RuntimeConfig{
		Logging: &LoggingConfig{Format: "human", Level: "debug"},
	}
	// Flags layer empty: the config section wins.
	merged := MergeRuntime(file, RuntimeConfig{})
	if merged.Logging == nil || merged.Logging.Format != "human" || merged.Logging.Level != "debug" {
		t.Fatalf("config section lost: %+v", merged.Logging)
	}

	// Flags layer non-empty: flag wins on conflict.
	flags := RuntimeConfig{Logging: &LoggingConfig{Format: "json", Level: "warn"}}
	merged = MergeRuntime(file, flags)
	if merged.Logging == nil {
		t.Fatal("logging section missing after flag merge")
	}
	if merged.Logging.Format != "json" || merged.Logging.Level != "warn" {
		t.Fatalf("flag must win on conflict: %+v", merged.Logging)
	}
}

func TestLoggingSectionPartialMerge(t *testing.T) {
	// Config sets format only; flags set level only — merged has both.
	file := RuntimeConfig{Logging: &LoggingConfig{Format: "human"}}
	flags := RuntimeConfig{Logging: &LoggingConfig{Level: "error"}}
	merged := MergeRuntime(file, flags)
	if merged.Logging == nil || merged.Logging.Format != "human" || merged.Logging.Level != "error" {
		t.Fatalf("partial merge broken: %+v", merged.Logging)
	}
}

func TestLoggingSectionValidation(t *testing.T) {
	bad := []LoggingConfig{
		{Format: "pretty"},
		{Format: "structured"},
		{Level: "verbose"},
		{Level: "fatal"},
	}
	for _, lc := range bad {
		rc := RuntimeConfig{Logging: &lc}
		if err := rc.Validate(); err == nil {
			t.Errorf("logging %+v should fail validation", lc)
		}
	}
	good := []LoggingConfig{
		{},
		{Format: "json"},
		{Format: "human"},
		{Level: "warn"},
		{Level: "warning"}, // alias accepted
		{Format: "human", Level: "debug"},
	}
	for _, lc := range good {
		rc := RuntimeConfig{Logging: &lc}
		if err := rc.Validate(); err != nil {
			t.Errorf("logging %+v should validate: %v", lc, err)
		}
	}
}

// TestLoggingSectionYAMLLoads: the YAML surface parses (the config is
// another way to turn the same knobs as the flags).
func TestLoggingSectionYAMLLoads(t *testing.T) {
	rc, err := LoadRuntimeFile("../../testdata/logging-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Logging == nil {
		t.Fatal("logging section not loaded")
	}
	if rc.Logging.Format != "human" || rc.Logging.Level != "warn" {
		t.Fatalf("logging section = %+v", rc.Logging)
	}
	if err := rc.Validate(); err != nil {
		t.Fatalf("valid YAML failed validation: %v", err)
	}
}
