// Command navi-code is a starter coding navi: a real navi module that
// assembles through navi.New, declares its tools/skills/model, and runs
// the TUI.
//
// Authoring model (S000200 §04): everything below is the static,
// compiled-in definition — Go only, no YAML definition file. Runtime
// settings (provider endpoints, credential references) come from the
// config file named by NAVI_CONFIG, if set.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tools/builtin"
)

//go:embed coding-conventions.md
var codingConventions string

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "navi-code:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []navi.Option{
		navi.WithVersion("0.1.0"),
		navi.WithPersonality("You are navi-code, a focused CLI coding agent. You orient yourself in the " +
			"repository before acting, prefer small verifiable changes, and keep your reasoning terse."),
		// navi-code never runs shell commands until the operator grants it.
		navi.WithPermissions(navi.PermissionPolicy{DenyTools: []string{"shell_exec"}}),
	}
	if cfg := os.Getenv("NAVI_CONFIG"); cfg != "" {
		opts = append(opts, navi.WithRuntimeConfig(cfg))
	}
	n := navi.New("navi-code", opts...)

	if err := n.Skills.Add(skill.Skill{
		Name:        "coding_conventions",
		Description: "Repository conventions and working style for code changes.",
		Content:     codingConventions,
		Tags:        []string{"code", "conventions"},
		Version:     "1.0.0",
	}); err != nil {
		return err
	}

	// Builtins: file_read + shell_exec, bound to the working directory.
	// shell_exec is compiled in but denied by the permission ceiling above
	// (deny wins), so it is never bound into a thought's surface.
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	builtins, err := builtin.Tools(workDir)
	if err != nil {
		return err
	}
	for _, t := range builtins {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}

	// One author tool: a complete tool.Tool descriptor + handler.
	if err := n.Tools.Add(repoStatsTool()); err != nil {
		return err
	}

	// One model, marked default (ADR 0009). Runtime config may retarget
	// the provider/model; the declared set is what model_list reports.
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		return err
	}

	// The TUI is the front door (ADR 0002: portals).
	if err := n.Portals.Add(tui.NewPortal()); err != nil {
		return err
	}

	return n.Run(ctx)
}

// repoStatsTool declares repo_stats as a public contract tool.
func repoStatsTool() tool.Tool {
	schema, _ := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Repository root to scan (default: working directory)"}
		}
	}`))
	return tool.Tool{
		Def: tool.Def{
			Name:          "repo_stats",
			Description:   "Walks the repository working tree and reports file counts and total line counts per top-level directory. Use for orientation before planning changes.",
			InputSchema:   schema,
			OutputFormat:  "markdown",
			Access:        tool.AccessReadOnly,
			Timeout:       30 * time.Second,
			Recovery:      "retry_once",
			Mode:          tool.ToolModeParallel,
			Tags:          []string{"code", "repo", "orientation"},
			Version:       "1.0.0",
			PromptSnippet: "Summarize repository structure and size per top-level directory",
			PromptGuidelines: []string{
				"Call repo_stats before planning multi-file changes",
			},
			SourceInfo: tool.SourceInfo{Origin: tool.OriginAuthor, Path: "cmd/navi-code/main.go"},
		},
		Handler: repoStats,
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
