package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// BuiltinTool pairs a builtin descriptor with its implementation.
type BuiltinTool struct {
	Tool AgentTool
	Run  func(map[string]any) (string, error)
}

// BuiltinTools returns the builtin tools (file_read, shell_exec)
// bound to the given working directory. Used both by
// RegisterBuiltinTools and by registry-based registration
// (tools/builtin).
func BuiltinTools(workDir string) []BuiltinTool {
	tr, hr := makeFileRead(workDir)
	ts, hs := makeShellExec(workDir)
	return []BuiltinTool{{Tool: tr, Run: hr}, {Tool: ts, Run: hs}}
}

// RegisterBuiltinTools registers the real tools (file_read, shell_exec)
// bound to the given working directory.
func RegisterBuiltinTools(ex *ToolExecutor, workDir string) {
	for _, bt := range BuiltinTools(workDir) {
		ex.RegisterTool(bt.Tool, bt.Run)
	}
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

func makeFileRead(workDir string) (AgentTool, func(map[string]any) (string, error)) {
	descriptor := AgentTool{
		Name:        ToolFileRead,
		Description: "Read the contents of a file within the working directory.",
		Parameters: jsonSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative (preferred) or absolute path inside the working directory.",
				},
			},
			"required": []string{"path"},
		}),
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
	return descriptor, handler
}

// ── shell_exec ───────────────────────────────────────────────────────────

const (
	defaultShellTimeout = 30 * time.Second
	maxShellTimeout     = 5 * time.Minute
)

func makeShellExec(workDir string) (AgentTool, func(map[string]any) (string, error)) {
	descriptor := AgentTool{
		Name: ToolShellExec,
		Description: "Execute a shell command in the working directory and return its combined output. " +
			"Executes with full shell access relative to the working directory; commands can read/write any path the " +
			"agent process can reach, including outside it — containment is the caller's responsibility (run the " +
			"agent under an OS-level sandbox if isolation is required). " +
			"Use for deliberate operations only; prefer file_read for accidental path-traversal safety.",
		Parameters: jsonSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute (run via /bin/sh -c).",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": fmt.Sprintf("Timeout in seconds (default %d, max %d).", int(defaultShellTimeout.Seconds()), int(maxShellTimeout.Seconds())),
				},
			},
			"required": []string{"command"},
		}),
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
	return descriptor, handler
}

// ── Demo tools (kept for examples and tests) ─────────────────────────────

func exampleSearch(args map[string]any) (string, error) {
	q, _ := args["query"].(string)
	return fmt.Sprintf("search result for: %s", q), nil
}

func exampleCalc(args map[string]any) (string, error) {
	a, _ := args["a"].(float64)
	b, _ := args["b"].(float64)
	return fmt.Sprintf("%.2f", a+b), nil
}

// RegisterDemoTools adds example tools to the given executor.
func RegisterDemoTools(ex *ToolExecutor) {
	ex.RegisterTool(AgentTool{
		Name:        "example_search",
		Description: "Example search tool.",
	}, exampleSearch)
	ex.RegisterTool(AgentTool{
		Name:        "example_calc",
		Description: "Example calculator tool.",
	}, exampleCalc)
}
