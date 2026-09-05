package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/internal/store/sqlite"
)

// REPL state: one live agent + its bound session (nil until /new or
// /resume).
type repl struct {
	quit    bool
	store   *sqlite.Store
	manager *session.SessionManager
	agent   *agent.Agent
	current *session.Session
	out     io.Writer
	opts    runOpts
}

func runREPL(in io.Reader, out io.Writer, store *sqlite.Store, opts runOpts) error {
	if opts.model == "" {
		return fmt.Errorf("--model is required (e.g. -model qwen3.6:35b)")
	}
	r := &repl{store: store, manager: session.NewSessionManager(store), out: out, opts: opts}
	fmt.Fprintf(out, "navi chat — model %s, db %s\nType /help for commands.\n", opts.model, opts.dbPath)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for {
		fmt.Fprint(out, "navi> ")
		if !scanner.Scan() {
			fmt.Fprintln(out)
			// Quitting mid-session: sync the live conversation to the
			// store so /search and /resume see it.
			if err := r.syncCurrent(); err != nil {
				fmt.Fprintf(out, "error on quit: %v\n", err)
			}
			return nil
		}
		line := scanner.Text()
		if err := r.handle(line); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		}
		if r.quit {
			return nil
		}
	}
}

func (r *repl) handle(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if !strings.HasPrefix(line, "/") {
		return r.chat(line)
	}
	cmd, rest, _ := strings.Cut(line[1:], " ")
	rest = strings.TrimSpace(rest)
	switch cmd {
	case "help":
		r.printHelp()
	case "quit", "q":
		r.quit = true
		if err := r.syncCurrent(); err != nil {
			return err
		}
	case "new":
		return r.cmdNew()
	case "list":
		return r.cmdList()
	case "resume":
		return r.cmdResume(rest)
	case "fork":
		return r.cmdFork()
	case "close":
		return r.cmdClose()
	case "tag":
		return r.cmdTag(rest)
	case "search":
		return r.cmdSearch(rest)
	case "export":
		return r.cmdExport(rest)
	case "import":
		return r.cmdImport(rest)
	default:
		fmt.Fprintf(r.out, "unknown command %q — /help for commands\n", cmd)
	}
	return nil
}

// ── chat: the core loop ──────────────────────────────────────────────────

func (r *repl) chat(text string) error {
	if r.agent == nil {
		return fmt.Errorf("no active session — /new or /resume first")
	}
	if err := r.agent.Prompt(text, nil); err != nil {
		return err
	}
	r.agent.WaitForIdle()
	st := r.agent.State()
	fmt.Fprintln(r.out)
	// Errors are already printed by the event subscriber ([agent error]);
	// returning them here would double-print. Surface via exit-context:
	// a non-idle status means the caller can check State().
	if st.Status == agent.StatusError {
		fmt.Fprintf(r.out, "(run ended in error state)\n")
	}
	return nil
}

// ensureAgent builds a fresh agent for the current session.
func (r *repl) ensureAgent(sess *session.Session) error {
	cfg := agent.AgentConfig{
		Provider:     "ollama",
		Model:        r.opts.model,
		SystemPrompt: r.opts.systemPrompt,
		WorkDir:      r.opts.workDir,
	}
	if r.opts.compactBudget > 0 {
		cfg.Compaction = &agent.CompactionConfig{
			Enabled:   true,
			MaxTokens: r.opts.compactBudget,
			KeepTurns: 2,
		}
	}
	a := agent.NewAgent(cfg)
	a.Provider = agent.NewOllamaProvider()

	// Event visibility: stream deltas inline, call out tools and
	// compaction explicitly — seeing the loop is the point of manual
	// testing.
	stop := make(chan struct{})
	a.Subscribe(func(ev agent.AgentEvent) {
		select {
		case <-stop:
			return
		default:
		}
		switch ev.Type {
		case agent.AgentEventContentDelta:
			if ev.Message != nil {
				fmt.Fprint(r.out, ev.Message.Content)
			}
		case agent.AgentEventToolCall:
			if ev.Tool != nil {
				fmt.Fprintf(r.out, "\n[tool call] %s\n", ev.Tool.Name)
			}
		case agent.AgentEventToolResult:
			if ev.Tool != nil {
				fmt.Fprintf(r.out, "[tool result] %s\n", oneLine(ev.Tool.Result))
			}
		case agent.AgentEventError:
			if ev.Error != nil {
				fmt.Fprintf(r.out, "\n[agent error] %v\n", ev.Error)
			}
		}
	}, false)
	r.agent = a
	r.current = sess
	return nil
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// syncCurrent captures the live agent conversation into the current
// session (pause snapshot).
func (r *repl) syncCurrent() error {
	if r.agent == nil || r.current == nil {
		return nil
	}
	if err := r.manager.SyncFromAgent(r.current, r.agent); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "[session %s synced (%d entries)]\n", shortID(r.current.ID), len(r.current.Entries))
	return nil
}

// ── session lifecycle commands ───────────────────────────────────────────

func (r *repl) cmdNew() error {
	if err := r.syncCurrent(); err != nil {
		return err
	}
	sess, err := r.manager.CreateSession("navi-cli", r.opts.workDir)
	if err != nil {
		return err
	}
	if err := r.ensureAgent(sess); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "[new session %s]\n", shortID(sess.ID))
	return nil
}

func (r *repl) cmdList() error {
	list, err := r.manager.ListSessions(session.ListOptions{Limit: 20})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(r.out, "(no sessions)")
		return nil
	}
	for _, info := range list {
		fmt.Fprintf(r.out, "%s  %-8s %-20s %3d msgs  %s\n",
			shortID(info.ID), info.Status, info.AgentID, info.MessageCount, info.Name)
	}
	return nil
}

func (r *repl) cmdResume(idPrefix string) error {
	if idPrefix == "" {
		return fmt.Errorf("usage: /resume <session-id-prefix>")
	}
	if err := r.syncCurrent(); err != nil {
		return err
	}
	sess, err := r.findByPrefix(idPrefix)
	if err != nil {
		return err
	}
	if err := r.ensureAgent(sess); err != nil {
		return err
	}
	if err := r.manager.RestoreToAgent(sess, r.agent); err != nil {
		return err
	}
	r.current = sess
	fmt.Fprintf(r.out, "[resumed %s — %d entries]\n", shortID(sess.ID), len(sess.Entries))
	return nil
}

func (r *repl) findByPrefix(prefix string) (*session.Session, error) {
	list, err := r.manager.ListSessions(session.ListOptions{Limit: 100})
	if err != nil {
		return nil, err
	}
	var matches []session.SessionInfo
	for _, info := range list {
		if strings.HasPrefix(info.ID, prefix) {
			matches = append(matches, info)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matching %q", prefix)
	case 1:
		return r.manager.LoadSession(matches[0].ID)
	default:
		return nil, fmt.Errorf("ambiguous prefix %q matches %d sessions", prefix, len(matches))
	}
}

func (r *repl) cmdFork() error {
	if r.current == nil {
		return fmt.Errorf("no active session")
	}
	if err := r.syncCurrent(); err != nil {
		return err
	}
	fork, err := r.manager.ForkSession(r.current.ID)
	if err != nil {
		return err
	}
	if err := r.ensureAgent(fork); err != nil {
		return err
	}
	// The fork's inherited history must be live in the new agent —
	// without this the first chat has no context and the next sync
	// wipes the fork's entries (whole-row replace).
	if err := r.manager.RestoreToAgent(fork, r.agent); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "[forked %s → %s]\n", shortID(r.current.ID), shortID(fork.ID))
	return nil
}

func (r *repl) cmdClose() error {
	if r.current == nil {
		return fmt.Errorf("no active session")
	}
	if err := r.syncCurrent(); err != nil {
		return err
	}
	if _, err := r.manager.CloseSession(r.current.ID); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "[closed %s]\n", shortID(r.current.ID))
	r.agent, r.current = nil, nil
	return nil
}

func (r *repl) cmdTag(tag string) error {
	if r.current == nil {
		return fmt.Errorf("no active session")
	}
	if tag == "" {
		return fmt.Errorf("usage: /tag <tag>")
	}
	if _, err := r.manager.TagSession(r.current.ID, tag); err != nil {
		return err
	}
	// Mutate the in-memory session too: a later SyncFromAgent whole-row
	// save would otherwise wipe the tag (store round-trip footgun).
	dup := false
	for _, t := range r.current.Tags {
		if t == tag {
			dup = true
			break
		}
	}
	if !dup {
		r.current.Tags = append(r.current.Tags, tag)
	}
	fmt.Fprintf(r.out, "[tagged %s with %q]\n", shortID(r.current.ID), tag)
	return nil
}

// ── search / export / import ─────────────────────────────────────────────

func (r *repl) cmdSearch(query string) error {
	if query == "" {
		return fmt.Errorf("usage: /search <query>")
	}
	results, err := r.store.Search(query, 10)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(r.out, "(no matches)")
		return nil
	}
	for _, info := range results {
		fmt.Fprintf(r.out, "%s  %-8s %3d msgs  %s\n", shortID(info.ID), info.Status, info.MessageCount, info.Name)
	}
	return nil
}

func (r *repl) cmdExport(args string) error {
	format, rest, _ := strings.Cut(args, " ")
	path, idPrefix, _ := strings.Cut(strings.TrimSpace(rest), " ")
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("usage: /export json|jsonl <path> [session-id-prefix]")
	}
	sess := r.current
	if idPrefix != "" {
		var err error
		sess, err = r.findByPrefix(idPrefix)
		if err != nil {
			return err
		}
	}
	if sess == nil {
		return fmt.Errorf("no active session — pass a session id prefix")
	}
	switch format {
	case "json":
		return r.store.ExportJSON(sess, path)
	case "jsonl":
		return r.store.ExportJSONL(sess, path)
	default:
		return fmt.Errorf("unknown format %q — use json or jsonl", format)
	}
}

func (r *repl) cmdImport(path string) error {
	if path == "" {
		return fmt.Errorf("usage: /import <path>")
	}
	sess, err := r.store.ImportJSONL(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "[imported %s — %d entries]\n", shortID(sess.ID), len(sess.Entries))
	return nil
}

func (r *repl) printHelp() {
	fmt.Fprint(r.out, `commands:
  /new              start a new session
  /list             list recent sessions
  /resume <id>      resume a session (id prefix ok)
  /fork             fork the current session at its current point
  /close            snapshot + close the current session
  /tag <tag>        tag the current session
  /search <query>   full-text search across sessions
  /export json|jsonl <path> [id]  export a session
  /import <path>    import a JSONL export
  /quit             leave
anything else is sent to the agent.
`)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
