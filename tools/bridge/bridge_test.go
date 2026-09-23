package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// toolDef builds a complete ToolDefinition (registration requires the
// full metadata descriptor).
func toolDef(name string, access registry.ToolAccess, timeout time.Duration) registry.ToolDefinition {
	schema, err := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {"query": {"type": "string"}},
		"required": ["query"]
	}`))
	if err != nil {
		panic(err)
	}
	return registry.ToolDefinition{
		Name:          name,
		Description:   "test tool " + name,
		InputSchema:   schema,
		OutputFormat:  "text",
		Access:        access,
		Timeout:       timeout,
		Recovery:      "retry_once",
		Mode:          registry.ToolModeSequential,
		Tags:          []string{"test"},
		Version:       "1.0.0",
		PromptSnippet: "test " + name,
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginBuiltin},
	}
}

// newRegistry registers three tools and returns the registry.
func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	slow := func(ctx context.Context, args map[string]any) (string, error) {
		time.Sleep(500 * time.Millisecond) // ignores ctx: exercises the hard timeout
		return "slow done", nil
	}
	ok := func(ctx context.Context, args map[string]any) (string, error) {
		return "ok:" + args["query"].(string), nil
	}
	mustRegister(t, reg, toolDef("test_read", registry.AccessReadOnly, 5*time.Second), ok)
	mustRegister(t, reg, toolDef("test_write", registry.AccessWrite, 5*time.Second), ok)
	mustRegister(t, reg, toolDef("test_slow", registry.AccessReadOnly, 5*time.Second), slow)
	t.Cleanup(func() { _ = reg })
	return reg
}

func mustRegister(t *testing.T, reg *registry.Registry, def registry.ToolDefinition, h registry.Handler) {
	t.Helper()
	if err := reg.Register(def, h); err != nil {
		t.Fatalf("register %s: %v", def.Name, err)
	}
}

func newExecutor(t *testing.T) *agent.ToolExecutor {
	t.Helper()
	return agent.NewToolExecutor("parallel", 4)
}

func boundSet(t *testing.T, ex *agent.ToolExecutor) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, tool := range ex.Tools() {
		names[tool.Name] = true
	}
	return names
}

func TestBindRegistersActiveOnly(t *testing.T) {
	reg := newRegistry(t)
	if err := reg.SetActive([]string{"test_read", "test_write"}); err != nil {
		t.Fatalf("set active: %v", err)
	}
	ex := newExecutor(t)
	bound, err := Bind(ex, reg, Options{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(bound) != 2 || bound[0] != "test_read" || bound[1] != "test_write" {
		t.Fatalf("bound = %v, want [test_read test_write]", bound)
	}
	got := boundSet(t, ex)
	if got["test_read"] && got["test_write"] && !got["test_slow"] {
		return // inactive tool was not bound
	}
	t.Fatalf("executor tools = %v", got)
}

func TestBindReturnsAllRegisteredWhenAllActive(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive(reg.AllNames())
	ex := newExecutor(t)
	bound, err := Bind(ex, reg, Options{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(bound) != 3 {
		t.Fatalf("bound = %v, want 3 tools", bound)
	}
}

func TestBindPolicyAllowedRestricts(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive(reg.AllNames())
	ex := newExecutor(t)
	bound, err := Bind(ex, reg, Options{Policy: Policy{Allowed: []string{"test_read", "test_slow"}}})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(bound) != 2 || bound[0] != "test_read" || bound[1] != "test_slow" {
		t.Fatalf("bound = %v, want [test_read test_slow]", bound)
	}
}

func TestBindPolicyBlockedWins(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive(reg.AllNames())
	ex := newExecutor(t)
	bound, err := Bind(ex, reg, Options{Policy: Policy{
		Allowed: []string{"test_read", "test_write"},
		Blocked: []string{"test_write"},
	}})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(bound) != 1 || bound[0] != "test_read" {
		t.Fatalf("bound = %v, want [test_read]", bound)
	}
}

func TestBindAccessFilter(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive(reg.AllNames())
	ex := newExecutor(t)
	bound, err := Bind(ex, reg, Options{Access: []registry.ToolAccess{registry.AccessReadOnly}})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	for _, name := range bound {
		if name != "test_read" && name != "test_slow" {
			t.Fatalf("non-read-only tool %q bound", name)
		}
	}
	if len(bound) != 2 {
		t.Fatalf("bound = %v, want 2 read-only tools", bound)
	}
}

func TestBindTimeoutOverrideAndDeadline(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive([]string{"test_slow"})
	ex := newExecutor(t)
	if _, err := Bind(ex, reg, Options{Policy: Policy{Timeouts: map[string]time.Duration{"test_slow": 25 * time.Millisecond}}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	results := ex.Execute([]agent.ToolCall{{Name: "test_slow", Arguments: json.RawMessage(`{"query":"x"}`)}})
	if !results[0].IsError {
		t.Fatalf("expected timeout error, got %q", results[0].Result)
	}
	if !strings.Contains(results[0].Result, "timed out after 25ms") {
		t.Fatalf("error = %q, want timeout message", results[0].Result)
	}
}

func TestBindDefinitionTimeoutEnforced(t *testing.T) {
	reg := registry.New()
	def := toolDef("test_slow", registry.AccessReadOnly, 25*time.Millisecond)
	if err := reg.Register(def, func(ctx context.Context, args map[string]any) (string, error) {
		time.Sleep(500 * time.Millisecond) // ignores ctx
		return "slow done", nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg.SetActive([]string{"test_slow"})
	ex := newExecutor(t)
	if _, err := Bind(ex, reg, Options{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	results := ex.Execute([]agent.ToolCall{{Name: "test_slow", Arguments: json.RawMessage(`{"query":"x"}`)}})
	if !results[0].IsError || !strings.Contains(results[0].Result, "timed out") {
		t.Fatalf("result = %+v, want timeout error", results[0])
	}
}

func TestBindDispatchesThroughRegistryValidation(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive([]string{"test_read"})
	ex := newExecutor(t)
	if _, err := Bind(ex, reg, Options{}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	// Invalid arguments are rejected by the registry's schema check,
	// not the handler.
	results := ex.Execute([]agent.ToolCall{{Name: "test_read", Arguments: json.RawMessage(`{}`)}})
	if !results[0].IsError || !strings.Contains(results[0].Result, "invalid arguments") {
		t.Fatalf("result = %+v, want schema-validation error", results[0])
	}

	// Valid arguments dispatch and return the handler's output.
	results = ex.Execute([]agent.ToolCall{{Name: "test_read", Arguments: json.RawMessage(`{"query":"hi"}`)}})
	if results[0].IsError || results[0].Result != "ok:hi" {
		t.Fatalf("result = %+v, want ok:hi", results[0])
	}
}

func TestBindSurfacesSchemaAsParameters(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive([]string{"test_read"})
	ex := newExecutor(t)
	if _, err := Bind(ex, reg, Options{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	tool := ex.Tools()[0]
	if tool.Name != "test_read" {
		t.Fatalf("tool name = %q", tool.Name)
	}
	if len(tool.Parameters) == 0 || !strings.Contains(string(tool.Parameters), `"query"`) {
		t.Fatalf("parameters = %s, want the registry schema document", tool.Parameters)
	}
}

func TestBindInactiveToolRefusesDispatchAfterRebindWindow(t *testing.T) {
	reg := newRegistry(t)
	reg.SetActive([]string{"test_read"})
	ex := newExecutor(t)
	if _, err := Bind(ex, reg, Options{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Deactivate after binding: the registry refuses dispatch even
	// though the executor still holds the entry (binding is a
	// snapshot; active-set enforcement stays in the registry).
	if err := reg.SetActive(nil); err != nil {
		t.Fatalf("set active nil: %v", err)
	}
	results := ex.Execute([]agent.ToolCall{{Name: "test_read", Arguments: json.RawMessage(`{"query":"x"}`)}})
	if !results[0].IsError || !strings.Contains(results[0].Result, "not active") {
		t.Fatalf("result = %+v, want not-active error", results[0])
	}
}

func TestBindNilArguments(t *testing.T) {
	ex := newExecutor(t)
	if _, err := Bind(ex, nil, Options{}); err == nil || !strings.Contains(err.Error(), "registry is required") {
		t.Fatalf("err = %v, want registry required", err)
	}
	if _, err := Bind(nil, registry.New(), Options{}); err == nil || !strings.Contains(err.Error(), "executor is required") {
		t.Fatalf("err = %v, want executor required", err)
	}
}
