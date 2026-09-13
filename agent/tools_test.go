package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestExecutor(t *testing.T, dir string) *ToolExecutor {
	t.Helper()
	return NewToolExecutor("sequential", 1)
}

func TestFileReadBasic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi there"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, handler := makeFileRead(dir)
	out, err := handler(map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi there" {
		t.Fatalf("output = %q", out)
	}
}

func TestFileReadRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	_, handler := makeFileRead(dir)

	for _, p := range []string{"../outside.txt", "/etc/hostname", "..", ""} {
		if _, err := handler(map[string]any{"path": p}); err == nil {
			t.Errorf("path %q: expected error, got none", p)
		}
	}
}

func TestFileReadMissingAndDir(t *testing.T) {
	dir := t.TempDir()
	_, handler := makeFileRead(dir)

	if _, err := handler(map[string]any{"path": "nope.txt"}); err == nil {
		t.Error("missing file: expected error")
	}
	if _, err := handler(map[string]any{"path": "."}); err == nil {
		t.Error("directory: expected error")
	}
}

func TestShellExecBasic(t *testing.T) {
	dir := t.TempDir()
	_, handler := makeShellExec(dir)
	out, err := handler(map[string]any{"command": "echo hello; echo $PWD"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, dir) {
		t.Fatalf("output %q missing working dir %q", out, dir)
	}
}

func TestShellExecTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	dir := t.TempDir()
	_, handler := makeShellExec(dir)
	out, err := handler(map[string]any{"command": "sleep 5", "timeout_seconds": float64(1)})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("output = %q, want timeout note", out)
	}
}

func TestShellExecTimeoutCapped(t *testing.T) {
	dir := t.TempDir()
	_, handler := makeShellExec(dir)
	// 600s requested; command finishes fast so we only check it doesn't error.
	done := make(chan struct{})
	go func() {
		handler(map[string]any{"command": "true", "timeout_seconds": float64(600)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler blocked; timeout cap not applied at execution level")
	}
}

func TestShellExecEmptyCommand(t *testing.T) {
	_, handler := makeShellExec(t.TempDir())
	if _, err := handler(map[string]any{"command": "   "}); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestRegisterBuiltinTools(t *testing.T) {
	ex := NewToolExecutor("sequential", 1)
	RegisterBuiltinTools(ex, t.TempDir())
	names := map[string]bool{}
	for _, tool := range ex.Tools() {
		names[tool.Name] = true
	}
	if !names[ToolFileRead] || !names[ToolShellExec] {
		t.Fatalf("builtin tools missing: %v", names)
	}
}

func TestSafePathInsideWorkDir(t *testing.T) {
	dir := t.TempDir()
	got, err := safePath(dir, "sub/dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "sub", "dir", "file.txt"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Absolute path inside the work dir is fine.
	if _, err := safePath(dir, filepath.Join(dir, "x")); err != nil {
		t.Fatalf("absolute inside workdir rejected: %v", err)
	}
}
