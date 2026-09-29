// Package builtin implements the compiled-in builtin tools (file_read,
// shell_exec) and registers them with complete metadata descriptors. This
// is the registry-first tool path: the registry owns the LLM-facing
// metadata and dispatch enforcement, and internal/bridge binds the active
// set into the agent executor — so builtin tools are governed by the same
// policy and active-set rules as catalog and author tools.
//
// The implementations live here rather than in internal/agent (NAVI-356):
// the catalog is a public package and must not import runtime internals.
// `internal/agent` is the generic agent loop; it no longer seeds a default
// tool set, so tools reach an agent only through the registry and
// internal/bridge. That also makes the working directory consistent — the
// loop previously registered its builtins with an empty WorkDir, which the
// bridge then replaced.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// Built-in tool names.
const (
	ToolFileRead  = "file_read"
	ToolShellExec = "shell_exec"
)

// maxFileReadBytes caps how much of a file file_read returns.
const maxFileReadBytes = 512 * 1024

// maxShellOutputBytes caps shell_exec combined output.
const maxShellOutputBytes = 100 * 1024

const (
	defaultShellTimeout = 30 * time.Second
	maxShellTimeout     = 5 * time.Minute
)

// bound pairs a builtin's registry descriptor with its implementation.
type bound struct {
	def registry.ToolDefinition
	run func(args map[string]any) (string, error)
}

// Register registers file_read and shell_exec into the registry, bound
// to workDir. Handlers run through the registry's dispatch validation;
// the runtime (internal/bridge) applies timeout envelopes and policy at
// bind time.
func Register(reg *registry.Registry, workDir string) error {
	for _, bt := range tools(workDir) {
		run := bt.run // capture for the closure
		handler := func(ctx context.Context, args map[string]any) (string, error) {
			return run(args)
		}
		if err := reg.Register(bt.def, handler); err != nil {
			return fmt.Errorf("builtin: register %s: %w", bt.def.Name, err)
		}
	}
	return nil
}

// Tools returns file_read and shell_exec as public component tools bound
// to workDir — the collection form of Register for the navi composition
// root.
func Tools(workDir string) ([]tool.Tool, error) {
	bts := tools(workDir)
	out := make([]tool.Tool, 0, len(bts))
	for _, bt := range bts {
		def, err := registry.ToDef(bt.def)
		if err != nil {
			return nil, fmt.Errorf("builtin: %w", err)
		}
		run := bt.run // capture for the closure
		out = append(out, tool.Tool{
			Def: def,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return run(args)
			},
		})
	}
	return out, nil
}

// tools builds the builtin descriptors and their implementations bound to
// workDir.
func tools(workDir string) []bound {
	frDef, frRun := makeFileRead(workDir)
	shDef, shRun := makeShellExec(workDir)
	return []bound{{def: frDef, run: frRun}, {def: shDef, run: shRun}}
}

// safePath resolves p relative to workDir and rejects paths that escape
// it (via .., symlinks aside — symlink resolution is left to the OS
// because the tools may legitimately read linked files inside workDir).
func safePath(workDir, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(absWork, p)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(absWork, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes working directory %q", p, absWork)
	}
	return abs, nil
}

// ── file_read ────────────────────────────────────────────────────────────

func makeFileRead(workDir string) (registry.ToolDefinition, func(map[string]any) (string, error)) {
	schema, _ := registry.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Relative (preferred) or absolute path inside the working directory."
			}
		},
		"required": ["path"]
	}`))
	def := registry.ToolDefinition{
		Name:        ToolFileRead,
		Description: "Read the contents of a file within the working directory.",
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
	}
	handler := func(args map[string]any) (string, error) {
		raw, _ := args["path"].(string)
		path, err := safePath(workDir, raw)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		content := string(data)
		if len(data) > maxFileReadBytes {
			content = content[:maxFileReadBytes] + fmt.Sprintf("\n[truncated: showing %d of %d bytes]", maxFileReadBytes, len(data))
		}
		return content, nil
	}
	return def, handler
}

// ── shell_exec ───────────────────────────────────────────────────────────

func makeShellExec(workDir string) (registry.ToolDefinition, func(map[string]any) (string, error)) {
	schema, _ := registry.ParseSchema(json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "The shell command to execute (run via /bin/sh -c)."
			},
			"timeout_seconds": {
				"type": "number",
				"description": "Timeout in seconds (default %d, max %d)."
			}
		},
		"required": ["command"]
	}`, int(defaultShellTimeout.Seconds()), int(maxShellTimeout.Seconds()))))
	def := registry.ToolDefinition{
		Name: ToolShellExec,
		Description: "Execute a shell command in the working directory and return its combined output. " +
			"Executes with full shell access relative to the working directory; commands can read/write any path the " +
			"agent process can reach, including outside it — containment is the caller's responsibility (run the " +
			"agent under an OS-level sandbox if isolation is required). " +
			"Use for deliberate operations only; prefer file_read for accidental path-traversal safety.",
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
	}
	handler := func(args map[string]any) (string, error) {
		command, _ := args["command"].(string)
		if strings.TrimSpace(command) == "" {
			return "", fmt.Errorf("empty command")
		}
		timeout := defaultShellTimeout
		if t, ok := args["timeout_seconds"].(float64); ok && t > 0 {
			timeout = time.Duration(t * float64(time.Second))
			if timeout > maxShellTimeout {
				timeout = maxShellTimeout
			}
		}

		dir := workDir
		if dir == "" {
			dir = "."
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()

		var sb strings.Builder
		output := string(out)
		truncated := false
		if len(output) > maxShellOutputBytes {
			output = output[:maxShellOutputBytes]
			truncated = true
		}
		sb.WriteString(output)
		if ctx.Err() == context.DeadlineExceeded {
			sb.WriteString(fmt.Sprintf("\n[error: command timed out after %s]", timeout))
			return sb.String(), fmt.Errorf("command timed out after %s", timeout)
		}
		if err != nil {
			sb.WriteString(fmt.Sprintf("\n[exit status: %v]", err))
		}
		if truncated {
			sb.WriteString(fmt.Sprintf("\n[truncated at %d bytes]", maxShellOutputBytes))
		}
		return sb.String(), nil
	}
	return def, handler
}
