package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/tool/registry"
)

func newRegistry(t *testing.T, workDir string) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, workDir); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	if err := reg.SetActive(reg.AllNames()); err != nil {
		t.Fatalf("activate builtins: %v", err)
	}
	return reg
}

// ── descriptor / registration ───────────────────────────────────────────

func TestRegisterCompleteMetadata(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	for _, name := range []string{ToolFileRead, ToolShellExec} {
		def, ok := reg.Get(name)
		if !ok {
			t.Fatalf("builtin %s not registered", name)
		}
		// Complete descriptor: registration enforces it, but assert the
		// key enforcement fields explicitly.
		if def.Access == registry.AccessAny || def.Mode == registry.ModeAny {
			t.Fatalf("builtin %s: incomplete access/mode", name)
		}
		if def.Timeout <= 0 || def.Recovery == "" || def.SourceInfo.Origin != registry.OriginBuiltin {
			t.Fatalf("builtin %s: incomplete metadata: %+v", name, def)
		}
		if len(def.InputSchema.Raw()) == 0 {
			t.Fatalf("builtin %s: empty schema", name)
		}
	}
}

func TestRegisterAccessLevels(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	read := reg.Find(registry.FindFilter{Access: registry.AccessReadOnly})
	exec := reg.Find(registry.FindFilter{Access: registry.AccessExecute})
	if len(read) != 1 || read[0].Name != ToolFileRead {
		t.Fatalf("read-only tools = %v", read)
	}
	if len(exec) != 1 || exec[0].Name != ToolShellExec {
		t.Fatalf("execute tools = %v", exec)
	}
}

func TestDispatchFileReadThroughRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello navi"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := newRegistry(t, dir)

	// Registry dispatch validates schema, then runs the handler bound
	// to the registration workDir.
	out, err := reg.Dispatch(context.Background(), ToolFileRead, []byte(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatalf("dispatch file_read: %v", err)
	}
	if out != "hello navi" {
		t.Fatalf("file_read output = %q", out)
	}

	// Path escape is rejected by the handler.
	_, err = reg.Dispatch(context.Background(), ToolFileRead, []byte(`{"path":"../outside.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape error = %v", err)
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	if _, err := reg.Dispatch(context.Background(), "file_delete", []byte(`{}`)); err == nil {
		t.Fatal("unknown tool dispatch should fail")
	}
}

func TestShellExecSequentialMode(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	def, ok := reg.Get(ToolShellExec)
	if !ok {
		t.Fatal("shell_exec not registered")
	}
	if def.Mode != registry.ToolModeSequential {
		t.Fatalf("shell_exec mode = %v, want sequential", def.Mode)
	}
}

func TestToolsReturnsBothBuiltins(t *testing.T) {
	tools, err := Tools(t.TempDir())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Def.Name] = true
		if tl.Handler == nil {
			t.Fatalf("tool %s has no handler", tl.Def.Name)
		}
	}
	if !names[ToolFileRead] || !names[ToolShellExec] {
		t.Fatalf("tools = %v, want file_read + shell_exec", names)
	}
}

// ── file_read implementation ────────────────────────────────────────────

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

// ── shell_exec implementation ───────────────────────────────────────────

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
