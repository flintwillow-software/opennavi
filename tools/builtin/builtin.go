// Package builtin registers the compiled-in builtin tools (file_read,
// shell_exec) into the public tool registry with complete
// metadata descriptors. This is the daemon's registry-first tool path:
// the registry owns the LLM-facing metadata and dispatch enforcement,
// and tools/bridge binds the active set into the agent executor — so
// builtin tools are governed by the same policy and active-set rules
// as catalog and author tools.
//
// When the full tool catalog lands (, NAVI-59), catalog tools
// register through the same shape; this package remains the home of
// the two always-compiled builtins.
package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// Register registers file_read and shell_exec into the registry,
// bound to workDir. Handlers run through the registry's dispatch
// validation; the runtime (tools/bridge) applies timeout envelopes
// and policy at bind time.
func Register(reg *registry.Registry, workDir string) error {
	for _, bt := range agent.BuiltinTools(workDir) {
		def, err := definition(bt.Tool)
		if err != nil {
			return err
		}
		tool := bt // capture for the closure
		handler := func(ctx context.Context, args map[string]any) (string, error) {
			return tool.Run(args)
		}
		if err := reg.Register(def, handler); err != nil {
			return fmt.Errorf("builtin: register %s: %w", def.Name, err)
		}
	}
	return nil
}

// Tools returns file_read and shell_exec as public contract tools bound
// to workDir — the collection form of Register for the navi composition
// root.
func Tools(workDir string) ([]tool.Tool, error) {
	var out []tool.Tool
	for _, bt := range agent.BuiltinTools(workDir) {
		def, err := definition(bt.Tool)
		if err != nil {
			return nil, err
		}
		contract, err := registry.ToDef(def)
		if err != nil {
			return nil, fmt.Errorf("builtin: %w", err)
		}
		run := bt.Run
		out = append(out, tool.Tool{
			Def: contract,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return run(args)
			},
		})
	}
	return out, nil
}

// definition renders a builtin's executor descriptor as a complete
// ToolDefinition with catalog metadata.
func definition(t agent.AgentTool) (registry.ToolDefinition, error) {
	schema, err := registry.ParseSchema(t.Parameters)
	if err != nil {
		return registry.ToolDefinition{}, fmt.Errorf("builtin %s: parse schema: %w", t.Name, err)
	}
	switch t.Name {
	case agent.ToolFileRead:
		return registry.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
			// OutputFormat "text": file contents are returned verbatim
			// (even when the file is code or markdown).
			OutputFormat:  "text",
			Access:        registry.AccessReadOnly,
			Timeout:       30 * time.Second,
			Recovery:      "retry_once",
			Mode:          registry.ToolModeParallel,
			Tags:          []string{"file", "read", "filesystem"},
			Version:       "1.0.0",
			PromptSnippet: "Read a file inside the working directory",
			PromptGuidelines: []string{
				"Prefer relative paths; paths outside the working directory are rejected.",
			},
			SourceInfo: registry.SourceInfo{Origin: registry.OriginBuiltin},
		}, nil
	case agent.ToolShellExec:
		return registry.ToolDefinition{
			Name:          t.Name,
			Description:   t.Description,
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        registry.AccessExecute,
			Timeout:       5 * time.Minute, // handler default is 30s; metadata is the cap
			Recovery:      "retry_once",
			Mode:          registry.ToolModeSequential,
			Tags:          []string{"shell", "command", "exec"},
			Version:       "1.0.0",
			PromptSnippet: "Execute a shell command in the working directory",
			PromptGuidelines: []string{
				"Use for deliberate operations only; the command runs with the navi process's permissions.",
			},
			SourceInfo: registry.SourceInfo{Origin: registry.OriginBuiltin},
		}, nil
	default:
		return registry.ToolDefinition{}, fmt.Errorf("builtin: unknown builtin tool %q", t.Name)
	}
}
