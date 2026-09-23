package daemon

import (
	"os"
	"path/filepath"
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

	// Builtins register with complete metadata; the default active set
	// is the full registered set (no policy configured).
	def, ok := rt.Registry.Get(agent.ToolFileRead)
	if !ok {
		t.Fatal("file_read not registered")
	}
	if def.SourceInfo.Origin != registry.OriginBuiltin {
		t.Fatalf("file_read origin = %v", def.SourceInfo.Origin)
	}
	active := rt.Registry.ActiveNames()
	if len(active) != 2 { // file_read + shell_exec
		t.Fatalf("active set = %v, want the two builtins", active)
	}

	// The stateful store round-trips: per-navi scoping is the seam's
	// concern, here we just verify the tables exist over the shared DB.
	if _, err := rt.Stateful.ListTodos("navi-under-test", "", 0); err != nil {
		t.Fatalf("list todos: %v", err)
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
	if len(active) != 1 || active[0] != agent.ToolFileRead {
		t.Fatalf("active set = %v, want [file_read]", active)
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
