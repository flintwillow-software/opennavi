package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// def builds a complete ToolDefinition; tests override fields to
// exercise validation.
func def(name string) ToolDefinition {
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object"}`))
	return ToolDefinition{
		Name:          name,
		Description:   "Test tool " + name + " for the " + name + " domain.",
		InputSchema:   schema,
		OutputFormat:  "text",
		Access:        AccessReadOnly,
		Timeout:       10 * time.Second,
		Recovery:      "retry_once",
		Mode:          ToolModeParallel,
		Tags:          []string{"test"},
		PromptSnippet: "Test tool (" + name + ")",
		SourceInfo:    SourceInfo{Origin: OriginBuiltin},
	}
}

func okHandler(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }

func mustRegister(t *testing.T, r *Registry, d ToolDefinition, opts ...Option) {
	t.Helper()
	if err := r.Register(d, okHandler, opts...); err != nil {
		t.Fatalf("Register(%s): %v", d.Name, err)
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := New()
	mustRegister(t, r, def("file_read"))

	got, ok := r.Get("file_read")
	if !ok {
		t.Fatal("Get(file_read) not found after registration")
	}
	if got.Description != def("file_read").Description {
		t.Errorf("description round-trip mismatch: %q", got.Description)
	}
	if _, ok := r.Get("missing_tool"); ok {
		t.Error("Get returned true for unknown tool")
	}
}

func TestListPreservesRegistrationOrder(t *testing.T) {
	r := New()
	for _, name := range []string{"shell_exec", "file_read", "git_diff"} {
		mustRegister(t, r, def(name))
	}
	names := r.AllNames()
	want := []string{"shell_exec", "file_read", "git_diff"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("AllNames() = %v, want %v", names, want)
	}
	if got := len(r.List()); got != 3 {
		t.Errorf("List() length = %d, want 3", got)
	}
}

func TestDuplicateNameRejected(t *testing.T) {
	r := New()
	mustRegister(t, r, def("file_read"))

	err := r.Register(def("file_read"), okHandler)
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate registration error = %v, want ErrDuplicateTool", err)
	}
	// The original registration remains intact (S130400 §02).
	got, ok := r.Get("file_read")
	if !ok || got.Access != AccessReadOnly {
		t.Error("original registration not intact after duplicate rejection")
	}
	if n := len(r.AllNames()); n != 1 {
		t.Errorf("AllNames length = %d after duplicate rejection, want 1", n)
	}
}

func TestIncompleteDefinitionRejected(t *testing.T) {
	r := New()
	// Minimal descriptor: name + description only (spec scenario).
	d := ToolDefinition{Name: "file_write", Description: "Write a file."}
	err := r.Register(d, okHandler)
	if err == nil {
		t.Fatal("incomplete definition accepted")
	}
	for _, field := range []string{"inputSchema", "outputFormat", "access", "timeout", "recovery", "mode", "promptSnippet", "sourceInfo.origin"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not list missing field %q", err, field)
		}
	}
}

func TestInvalidNamePatternRejected(t *testing.T) {
	r := New()
	for _, name := range []string{"nodash", "_leading", "trailing_", "has space"} {
		d := def(name)
		if name == "nodash" {
			continue // no underscore: invalid
		}
		if err := r.Register(d, okHandler); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if err := r.Register(def("nodash"), okHandler); err == nil {
		t.Error("name without namespace_action pattern accepted")
	}
}

func TestNamespaceDerivedFromFirstSegment(t *testing.T) {
	r := New()
	mustRegister(t, r, def("github_issue_read"))

	got, _ := r.Get("github_issue_read")
	if got.Namespace() != "github" {
		t.Errorf("Namespace() = %q, want github", got.Namespace())
	}
}

func TestCrossDomainSameActionCoexist(t *testing.T) {
	r := New()
	mustRegister(t, r, def("github_issue_read"))
	mustRegister(t, r, def("plane_issue_read"))

	githubOnly := r.Find(FindFilter{Namespace: "github"})
	if len(githubOnly) != 1 || githubOnly[0].Name != "github_issue_read" {
		t.Errorf("Find(namespace=github) = %v, want [github_issue_read]", names(githubOnly))
	}
}

func names(defs []ToolDefinition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

func TestFindFilters(t *testing.T) {
	r := New()
	d1 := def("file_read")
	d2 := def("file_write")
	d2.Access = AccessWrite
	d2.Mode = ToolModeSequential
	d3 := def("git_diff")
	d3.Tags = []string{"git", "read"}
	d3.SourceInfo = SourceInfo{Origin: OriginAuthor}
	for _, d := range []ToolDefinition{d1, d2, d3} {
		mustRegister(t, r, d)
	}

	if got := names(r.Find(FindFilter{Namespace: "file"})); len(got) != 2 {
		t.Errorf("namespace=file: got %v, want 2 file tools", got)
	}
	if got := names(r.Find(FindFilter{Access: AccessWrite})); len(got) != 1 || got[0] != "file_write" {
		t.Errorf("access=write: got %v, want [file_write]", got)
	}
	if got := names(r.Find(FindFilter{Mode: ToolModeSequential})); len(got) != 1 || got[0] != "file_write" {
		t.Errorf("mode=sequential: got %v, want [file_write]", got)
	}
	if got := names(r.Find(FindFilter{Tags: []string{"git"}})); len(got) != 1 || got[0] != "git_diff" {
		t.Errorf("tag=git: got %v, want [git_diff]", got)
	}
	if got := names(r.Find(FindFilter{Origin: OriginAuthor})); len(got) != 1 || got[0] != "git_diff" {
		t.Errorf("origin=author: got %v, want [git_diff]", got)
	}
	if got := names(r.Find(FindFilter{})); len(got) != 3 {
		t.Errorf("empty filter: got %v, want all 3", got)
	}
}

func TestFindMaxAccessCapsAccessLevel(t *testing.T) {
	r := New()
	read := def("file_read")
	write := def("file_write")
	write.Access = AccessWrite
	execute := def("shell_exec")
	execute.Access = AccessExecute
	for _, d := range []ToolDefinition{read, write, execute} {
		mustRegister(t, r, d)
	}

	got := names(r.Find(FindFilter{MaxAccess: AccessReadOnly}))
	if len(got) != 1 || got[0] != "file_read" {
		t.Errorf("MaxAccess=read-only: got %v, want [file_read]", got)
	}
	if got := names(r.Find(FindFilter{MaxAccess: AccessWrite})); len(got) != 2 {
		t.Errorf("MaxAccess=write: got %v, want 2", got)
	}
}

func TestRegisteredVsActive(t *testing.T) {
	r := New()
	mustRegister(t, r, def("file_read"))
	mustRegister(t, r, def("shell_exec"))

	// Nothing active yet: the LLM-facing list is empty even though the
	// registry holds both tools (S130400 §03 registered vs active).
	if got := r.ActiveNames(); len(got) != 0 {
		t.Errorf("ActiveNames() = %v before SetActive, want empty", got)
	}
	if err := r.SetActive([]string{"file_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if got := names(r.Active()); len(got) != 1 || got[0] != "file_read" {
		t.Errorf("Active() = %v, want [file_read]", got)
	}

	// Activate the second at runtime without re-registration.
	if err := r.SetActive([]string{"file_read", "shell_exec"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if got := len(r.Active()); got != 2 {
		t.Errorf("Active() length = %d after activation, want 2", got)
	}
}

func TestSetActiveUnknownToolRejected(t *testing.T) {
	r := New()
	if err := r.SetActive([]string{"ghost_tool"}); err == nil {
		t.Error("SetActive accepted an unknown tool name")
	}
}

func TestDispatchHappyPath(t *testing.T) {
	r := New()
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`))
	d := def("file_read")
	d.InputSchema = schema
	called := false
	err := r.Register(d, func(ctx context.Context, args map[string]any) (string, error) {
		called = true
		if args["path"] != "main.go" {
			t.Errorf("handler got args %v, want path=main.go", args)
		}
		return "contents", nil
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetActive([]string{"file_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	out, err := r.Dispatch(context.Background(), "file_read", json.RawMessage(`{"path":"main.go"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if out != "contents" || !called {
		t.Errorf("Dispatch out=%q called=%v, want contents/true", out, called)
	}
}

func TestDispatchRequiresActiveMembership(t *testing.T) {
	r := New()
	mustRegister(t, r, def("file_read"))

	// Unknown tool.
	if _, err := r.Dispatch(context.Background(), "ghost_tool", nil); err == nil {
		t.Error("Dispatch of unknown tool accepted")
	}
	// Registered but inactive: not dispatchable.
	if _, err := r.Dispatch(context.Background(), "file_read", json.RawMessage(`{}`)); err == nil {
		t.Error("Dispatch of inactive tool accepted")
	}
}

func TestDispatchInvalidArgumentsRejectedWithoutHandler(t *testing.T) {
	r := New()
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`))
	d := def("file_read")
	d.InputSchema = schema
	called := false
	err := r.Register(d, func(ctx context.Context, args map[string]any) (string, error) {
		called = true
		return "", nil
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetActive([]string{"file_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// Missing required property (S130400 §06 invalid-arguments error).
	if _, err := r.Dispatch(context.Background(), "file_read", json.RawMessage(`{}`)); err == nil {
		t.Error("Dispatch accepted arguments missing required path")
	} else if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error %q does not mention invalid arguments", err)
	}
	// Not JSON at all.
	if _, err := r.Dispatch(context.Background(), "file_read", json.RawMessage(`not json`)); err == nil {
		t.Error("Dispatch accepted non-JSON arguments")
	}
	// Type violation.
	if _, err := r.Dispatch(context.Background(), "file_read", json.RawMessage(`{"path":42}`)); err == nil {
		t.Error("Dispatch accepted wrong-typed path")
	}
	if called {
		t.Error("handler ran despite invalid arguments")
	}
}

func TestDispatchPrepareArgumentsHook(t *testing.T) {
	r := New()
	// Current schema requires "query"; old sessions stored "q".
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`))
	d := def("file_find")
	d.InputSchema = schema
	var gotQuery string
	err := r.Register(d, func(ctx context.Context, args map[string]any) (string, error) {
		gotQuery, _ = args["query"].(string)
		return "hit", nil
	}, WithPrepareArguments(func(args map[string]any) (map[string]any, error) {
		if q, ok := args["q"].(string); ok {
			args["query"] = q
			delete(args, "q")
		}
		return args, nil
	}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetActive([]string{"file_find"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// The old shape fails without the hook; passes through it.
	if _, err := r.Dispatch(context.Background(), "file_find", json.RawMessage(`{"q":"needle"}`)); err != nil {
		t.Errorf("Dispatch with old argument shape through prepare hook: %v", err)
	}
	if gotQuery != "needle" {
		t.Errorf("handler got query %q, want needle", gotQuery)
	}
}

func TestConcurrentRegistrationAndDiscovery(t *testing.T) {
	r := New()
	mustRegister(t, r, def("file_read"))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = r.Get("file_read")
			_ = r.List()
			_ = r.Find(FindFilter{Namespace: "file"})
			_ = r.Active()
			_ = r.SetActive([]string{"file_read"})
			_ = r.Register(def("file_other"), okHandler) // duplicate rejected, no race
		}(i)
	}
	wg.Wait()
}
