package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestParseSchema(t *testing.T) {
	schema, err := ParseSchema(json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if len(schema.raw) == 0 {
		t.Fatal("schema raw empty")
	}
	if _, err := ParseSchema(json.RawMessage(nil)); err == nil {
		t.Error("empty schema accepted")
	}
	if _, err := ParseSchema(json.RawMessage(`["not","an","object"]`)); err == nil {
		t.Error("non-object schema accepted")
	}
}

func TestAccessStrings(t *testing.T) {
	if AccessReadOnly.String() != "read-only" || AccessWrite.String() != "write" || AccessExecute.String() != "execute" || AccessAny.String() != "any" {
		t.Errorf("access strings wrong")
	}
}

func TestModeStrings(t *testing.T) {
	if ToolModeParallel.String() != "parallel" || ToolModeSequential.String() != "sequential" || ModeAny.String() != "any" {
		t.Errorf("mode strings wrong")
	}
}

func TestOriginStrings(t *testing.T) {
	if OriginSDK.String() != "sdk" || OriginBuiltin.String() != "builtin" || OriginAuthor.String() != "author" || OriginExtension.String() != "extension" {
		t.Errorf("origin strings wrong")
	}
}

func TestDefNamespace(t *testing.T) {
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object"}`))
	d := Def{
		Name:          "github_issue_read",
		Description:   "reads a github issue",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        AccessReadOnly,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          ToolModeParallel,
		Tags:          []string{"github"},
		Version:       "1.0.0",
		PromptSnippet: "read a github issue",
		SourceInfo:    SourceInfo{Origin: OriginSDK, Path: "ext/gh"},
	}
	if d.Namespace() != "github" {
		t.Errorf("namespace = %q, want github", d.Namespace())
	}
	// The descriptor is the 13 public ToolDefinition fields —
	// prepareArguments is deliberately absent from the ABI (the M4
	// port attaches argument preparation by registration option).
	if d.Timeout != 10*time.Second || d.Access != AccessReadOnly {
		t.Errorf("def fields wrong")
	}
}

func TestToolPair(t *testing.T) {
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object"}`))
	handled := false
	tl := Tool{
		Def: Def{
			Name:          "echo_ping",
			Description:   "pings",
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        AccessReadOnly,
			Timeout:       5 * time.Second,
			Recovery:      "retry_once",
			Mode:          ToolModeParallel,
			PromptSnippet: "ping",
			SourceInfo:    SourceInfo{Origin: OriginAuthor, Path: "main.go"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			handled = true
			return "pong", nil
		},
	}
	out, err := tl.Handler(context.Background(), map[string]any{})
	if err != nil || out != "pong" || !handled {
		t.Errorf("handler pair failed: %q %v", out, err)
	}
	if tl.Def.Namespace() != "echo" {
		t.Errorf("pair namespace = %q", tl.Def.Namespace())
	}
}
