// Command navi-code is a starter coding navi: a real navi module that
// assembles through navi.New, declares its tools/skills/model, and runs
// the TUI.
//
// Authoring model: everything below is the static,
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
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/flintwillow-software/opennavi/connector/github"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/portal/tui"
	"github.com/flintwillow-software/opennavi/skill"
	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/builtin"
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

	// GitHub egress (ADR 0014, ADR 0020): the connector is declared at build
	// time, so the capability exists whether or not the operator configured a
	// token — a missing credential makes the calls refuse, not the navi.
	// SendTools hands back ordinary tool.Tool values, added explicitly here and
	// never auto-registered, so this file is the whole story of what
	// navi-code can say outward.
	gh := github.New(github.Config{
		// An explicit reference wins — that is what CI and a deployment set —
		// and the developer's own `gh auth login` is the fallback, so a local
		// run needs no exported variable. A credential REFERENCE, never a
		// value (ADR 0008): the token is read at the moment of each call.
		Token: github.ChainToken(
			github.StaticToken{Ref: githubCredentialRef},
			&github.GHCLIToken{},
		),
		// The repository egress tools act on by default. A tool call may
		// override it, so an unset value is not an error.
		Repository: githubRepository(),
	})
	if err := n.Connectors.Add(gh); err != nil {
		return err
	}
	ghTools, err := gh.SendTools()
	if err != nil {
		return err
	}
	for _, t := range ghTools {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
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

// githubCredentialRef is the environment variable navi-code reads its GitHub
// token from. It is compiled in as a default reference the operator can see;
// the value is resolved at the call site and never stored (ADR 0008). When it
// is unset, the connector falls back to the developer's own `gh auth login`
// (see GHCLIToken).
const githubCredentialRef = "GITHUB_TOKEN"

// githubRepository resolves the repository egress tools act on: an explicit
// override, else the working directory's git remote, else empty (the model may
// pass repository per call). This is deployment config, not a secret, so it is
// read at start rather than compiled in.
func githubRepository() string {
	if v := strings.TrimSpace(os.Getenv("NAVI_CODE_GITHUB_REPO")); v != "" {
		return v
	}
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return parseGitRemote(strings.TrimSpace(string(out)))
}

// parseGitRemote turns a git remote URL into owner/name, or "" when it is not
// a GitHub remote. Both the ssh and https spellings are accepted:
//
//	git@github.com:acme/widgets.git   -> acme/widgets
//	https://github.com/acme/widgets   -> acme/widgets
func parseGitRemote(remote string) string {
	s := remote
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[j+1:] // drop the host
		}
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:] // scp-like: git@host:owner/name
	} else {
		return ""
	}
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), ".git"), "/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return s
}

// repoStatsTool declares repo_stats as a public component tool.
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
