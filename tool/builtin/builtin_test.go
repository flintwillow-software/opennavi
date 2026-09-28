package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/agent"
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

func TestRegisterCompleteMetadata(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	for _, name := range []string{agent.ToolFileRead, agent.ToolShellExec} {
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
	if len(read) != 1 || read[0].Name != agent.ToolFileRead {
		t.Fatalf("read-only tools = %v", read)
	}
	if len(exec) != 1 || exec[0].Name != agent.ToolShellExec {
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
	out, err := reg.Dispatch(context.Background(), agent.ToolFileRead, []byte(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatalf("dispatch file_read: %v", err)
	}
	if out != "hello navi" {
		t.Fatalf("file_read output = %q", out)
	}

	// Path escape is rejected by the handler.
	_, err = reg.Dispatch(context.Background(), agent.ToolFileRead, []byte(`{"path":"../outside.txt"}`))
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
	def, ok := reg.Get(agent.ToolShellExec)
	if !ok {
		t.Fatal("shell_exec not registered")
	}
	if def.Mode != registry.ToolModeSequential {
		t.Fatalf("shell_exec mode = %v, want sequential", def.Mode)
	}
}
