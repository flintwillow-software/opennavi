package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/tools/builtin"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

func TestAssembleWiresRegistryAndStatefulStore(t *testing.T) {
	rt, err := Assemble(testParams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	if rt.Registry == nil || rt.Stateful == nil {
		t.Fatal("registry/stateful store not assembled")
	}

	// Builtins register with complete metadata; the todo tools register
	// statefully for the navi (default scope when NaviID is unset).
	def, ok := rt.Registry.Get(agent.ToolFileRead)
	if !ok {
		t.Fatal("file_read not registered")
	}
	if def.SourceInfo.Origin != registry.OriginBuiltin {
		t.Fatalf("file_read origin = %v", def.SourceInfo.Origin)
	}
	active := rt.Registry.ActiveNames()
	want := map[string]bool{
		agent.ToolFileRead: true, agent.ToolShellExec: true,
		"todo_read": true, "todo_write": true, "todo_update": true,
		"note_read": true, "note_write": true, "time_read": true,
		"http_request": true,
	}
	if len(active) != len(want) {
		t.Fatalf("active set = %v, want 9 tools", active)
	}
	for _, name := range active {
		if !want[name] {
			t.Fatalf("unexpected active tool %q", name)
		}
	}

	// End-to-end: todo_write through the assembled registry persists
	// via the stateful store and todo_read returns the items.
	if _, err := rt.Registry.Dispatch(context.Background(), "todo_write",
		[]byte(`{"items":[{"text":"wire note tools","priority":2}]}`)); err != nil {
		t.Fatalf("dispatch todo_write: %v", err)
	}
	out, err := rt.Registry.Dispatch(context.Background(), "todo_read", []byte(`{}`))
	if err != nil {
		t.Fatalf("dispatch todo_read: %v", err)
	}
	if !strings.Contains(out, "wire note tools") {
		t.Fatalf("todo_read output = %q", out)
	}
	items, err := rt.Stateful.ListTodos("default", "", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("stateful list = %v (err %v)", items, err)
	}

	// Note tools dispatch through the same seam (defaults: 64 KiB /
	// 10 MiB caps).
	if _, err := rt.Registry.Dispatch(context.Background(), "note_write",
		[]byte(`{"title":"wiring","body":"note tools registered","tags":["epic"]}`)); err != nil {
		t.Fatalf("dispatch note_write: %v", err)
	}
	out, err = rt.Registry.Dispatch(context.Background(), "note_read", []byte(`{"tag":"epic"}`))
	if err != nil {
		t.Fatalf("dispatch note_read: %v", err)
	}
	if !strings.Contains(out, "note tools registered") {
		t.Fatalf("note_read output = %q", out)
	}

	// time_read dispatches statelessly and honors an IANA zone.
	out, err = rt.Registry.Dispatch(context.Background(), "time_read", []byte(`{"timezone":"Asia/Kolkata"}`))
	if err != nil {
		t.Fatalf("dispatch time_read: %v", err)
	}
	if !strings.Contains(out, "+05:30") {
		t.Fatalf("time_read output = %q, want a +05:30 offset", out)
	}
	if _, err := rt.Registry.Dispatch(context.Background(), "time_read", []byte(`{"timezone":"Mars/Olympus"}`)); err == nil {
		t.Fatal("invalid timezone must error")
	}

	// http_request is governed by the SSRF guard: loopback is denied by
	// default (no dial placed — the guard rejects before connecting).
	_, err = rt.Registry.Dispatch(context.Background(), "http_request",
		[]byte(`{"method":"GET","url":"http://127.0.0.1:1/x"}`))
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("http_request to loopback = %v, want SSRF denial", err)
	}
}

func TestAssembleToolPolicyComposesActiveSet(t *testing.T) {
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
tools:
  blocked:
    - shell_exec
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

	active := rt.Registry.ActiveNames()
	if len(active) != 8 { // file_read + 3 todo + 2 note + time + http (shell_exec blocked)
		t.Fatalf("active set = %v, want file_read + todo + note + time + http tools", active)
	}
	for _, name := range active {
		if name == agent.ToolShellExec {
			t.Fatal("blocked shell_exec is active")
		}
	}
}

func TestAssembleToolCapsConfig(t *testing.T) {
	rcPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider:
  primary: mock
  model: test-model
tool_caps:
  notes:
    max_body_bytes: 16
  http:
    denylist:
      - example.com
    max_response_bytes: 1024
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

	// The configured note cap (64 KiB default → 16 bytes) governs
	// note_write dispatch through the assembled registry.
	big := strings.Repeat("x", 32)
	_, err = rt.Registry.Dispatch(context.Background(), "note_write",
		[]byte(`{"title":"t","body":"`+big+`"}`))
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("oversized note = %v, want cap rejection", err)
	}

	// The configured denylist is enforced on http_request dispatch.
	_, err = rt.Registry.Dispatch(context.Background(), "http_request",
		[]byte(`{"method":"GET","url":"https://example.com/"}`))
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("denylisted host = %v, want SSRF denial", err)
	}
}

func TestActiveNamesDenyWins(t *testing.T) {
	tp := config.ToolPolicy{
		Allowed: []string{"file_read", "shell_exec"},
		Blocked: []string{"shell_exec"},
	}
	got := activeNames([]string{"file_read", "shell_exec"}, tp)
	if len(got) != 1 || got[0] != "file_read" {
		t.Fatalf("activeNames = %v, want [file_read]", got)
	}
}

func TestBindOptionsMapsPolicy(t *testing.T) {
	tp := config.ToolPolicy{
		Allowed: []string{"file_read"},
		Blocked: []string{"shell_exec"},
		Timeout: map[string]time.Duration{"file_read": 2 * time.Second},
	}
	opts := bindOptions(tp)
	if len(opts.Policy.Allowed) != 1 || opts.Policy.Allowed[0] != "file_read" {
		t.Fatalf("policy = %+v", opts.Policy)
	}
	if opts.Policy.Timeouts["file_read"] != 2*time.Second {
		t.Fatalf("timeout override = %v", opts.Policy.Timeouts)
	}
}

// TestAgentRunnerBindsRegistryTools verifies the thought run's
// executor is rebuilt from the registry's active set: builtins land
// through the registry (with policy governance) instead of the raw
// NewAgent seeding.
func TestAgentRunnerBindsRegistryTools(t *testing.T) {
	workDir := t.TempDir()
	reg := registry.New()
	if err := builtin.Register(reg, workDir); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetActive([]string{agent.ToolFileRead}); err != nil { // shell_exec NOT active
		t.Fatal(err)
	}

	r := NewAgentRunner(agentRunnerDeps{
		registry: reg,
		bind:     bindOptions(config.ToolPolicy{}),
	})
	a := agent.NewAgent(agent.AgentConfig{Provider: "daemon-wired", Model: "m"})
	if err := r.bindTools(a); err != nil {
		t.Fatalf("bindTools: %v", err)
	}

	names := map[string]bool{}
	for _, tool := range a.Executor.Tools() {
		names[tool.Name] = true
	}
	if !names[agent.ToolFileRead] {
		t.Fatalf("file_read not bound: %v", names)
	}
	if names[agent.ToolShellExec] {
		t.Fatal("inactive shell_exec must not be bound")
	}
}
