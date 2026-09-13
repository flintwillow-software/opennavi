// Command navi-code is the starter CLI coding agent: a navi that
// orients itself in a repository and helps with code work through an
// interactive terminal chat (interface per S000400 §01).
//
// Authoring model (S000200 §04): everything below is the static,
// compiled-in definition — Go only, no YAML definition file. The
// runtime config (provider endpoints, key paths) lives under
// .config/navi/navi-code/ and is set by the end user, not here.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/sdk"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

func main() {
	reg := registry.New()

	// One author tool registered through the tool registry (S130400):
	// registration IS the declaration.
	must(reg.Register(registry.ToolDefinition{
		Name:        "repo_stats",
		Description: "Walks the repository working tree and reports file counts and total line counts per top-level directory. Use for orientation before planning changes.",
		InputSchema: mustSchema(json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Repository root to scan (default: working directory)"}
			}
		}`)),
		OutputFormat:  "markdown",
		Access:        registry.AccessReadOnly,
		Timeout:       30 * time.Second,
		Recovery:      "retry_once",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"code", "repo", "orientation"},
		Version:       "1.0.0",
		PromptSnippet: "Summarize repository structure and size per top-level directory",
		PromptGuidelines: []string{
			"Call repo_stats before planning multi-file changes",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginAuthor, Path: "cmd/navi-code/main.go"},
	}, repoStats))

	// Activate the author tool. Catalog builtins (file_*, shell_exec,
	// git_*, ...) join the active set once the tool catalog ships
	// (epic NAVI-59); they register through the same registry.
	must(reg.SetActive([]string{"repo_stats"}))

	def := sdk.NaviDef{
		Name:    "navi-code",
		Version: "0.1.0",
		Personality: "You are navi-code, a focused CLI coding agent. You orient yourself in the " +
			"repository before acting, prefer small verifiable changes, and keep your reasoning terse.",
		Skills: []sdk.Skill{
			{Name: "coding-conventions", Path: "skills/coding-conventions.md"},
		},
		Interfaces: sdk.InterfaceSet{
			CLI: true,
		},
		Permissions: sdk.PermissionPolicy{
			// Deny takes precedence over Allow; navi-code never runs shell
			// commands until the operator grants it.
			Deny: []string{"shell_exec"},
		},
	}

	// sdk.Build validates the definition and compiles the binary
	// (S000300). It exits after producing the binary.
	if err := sdk.Build(def); err != nil {
		fmt.Fprintln(os.Stderr, "navi-code:", err)
		os.Exit(1)
	}
}

// repoStats walks the tree rooted at args["path"] (default ".") and
// reports file and line counts per top-level directory.
func repoStats(ctx context.Context, args map[string]any) (string, error) {
	root := "."
	if p, ok := args["path"].(string); ok && p != "" {
		root = p
	}
	counts := map[string]int{}
	files := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		top := topDir(root, path)
		files[top]++
		n, lerr := lineCount(path)
		if lerr != nil {
			return nil // unreadable file: count the file, skip lines
		}
		counts[top] += n
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("repo_stats: %w", err)
	}
	var b strings.Builder
	b.WriteString("| Directory | Files | Lines |\n|---|---|---|\n")
	tops := make([]string, 0, len(files))
	for top := range files {
		tops = append(tops, top)
	}
	sort.Strings(tops)
	for _, top := range tops {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", top, files[top], counts[top])
	}
	return b.String(), nil
}

func topDir(root, path string) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || rel == "." {
		return "."
	}
	if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return rel
}

func lineCount(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strings.Count(string(data), "\n"), nil
}

func mustSchema(raw json.RawMessage) registry.Schema {
	s, err := registry.ParseSchema(raw)
	if err != nil {
		panic(fmt.Sprintf("navi-code: tool schema: %v", err))
	}
	return s
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "navi-code:", err)
		os.Exit(1)
	}
}
