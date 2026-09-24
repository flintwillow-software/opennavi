package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ── NAVI-331: TUI-mode log routing ───────────────────────────────────

// TestDefaultLogPathUnderDBDir: the TUI-mode default log lands at
// <db-dir>/navi.log — inspectable before/after the session.
func TestDefaultLogPathUnderDBDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions", "navi.db") // nested dir → Dir() matters
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}

	got := defaultLogPath(dbPath)
	if want := filepath.Join(dir, "sessions", "navi.log"); got != want {
		t.Fatalf("defaultLogPath = %q, want %q", got, want)
	}
	// The path is openable (the dir exists / is creatable).
	f, err := os.OpenFile(got, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("default log path not openable: %v", err)
	}
	f.Close()
}

// TestLogRoutingDecisionTable: the four mode × flag combinations of
// NAVI-331's routing matrix.
func TestLogRoutingDecisionTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "navi.db")
	explicit := filepath.Join(dir, "explicit.log")

	cases := []struct {
		name       string
		daemon     bool
		logFile    string
		wantStderr bool
		wantFile   string // "" = no file
	}{
		{"tui-no-flag", false, "", false, filepath.Join(dir, "navi.log")},
		{"tui-explicit-file", false, explicit, false, explicit},
		{"daemon-no-flag", true, "", true, ""},
		{"daemon-explicit-file", true, explicit, false, explicit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := resolveLogRoute(tc.daemon, tc.logFile, dbPath)
			if route.stderr != tc.wantStderr {
				t.Fatalf("stderr = %v, want %v", route.stderr, tc.wantStderr)
			}
			if route.path != tc.wantFile {
				t.Fatalf("file = %q, want %q", route.path, tc.wantFile)
			}
		})
	}
}

// TestLogRoutingExplicitFileHonored: an explicit --log-file wins in
// both modes and is never silently replaced by the default.
func TestLogRoutingExplicitFileHonored(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "custom.log")
	route := resolveLogRoute(false, explicit, filepath.Join(dir, "navi.db"))
	if route.path != explicit || route.stderr {
		t.Fatalf("explicit file overridden: %+v", route)
	}
}
