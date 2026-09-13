package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/session"
	"github.com/flintwillow-software/opennavi/store/sqlite"
)

// newTestREPL builds a REPL over a temp store with no live agent —
// enough to exercise command dispatch (chat requires a live Ollama).
func newTestREPL(t *testing.T) (*repl, *bytes.Buffer) {
	t.Helper()
	store, err := sqlite.Open(t.TempDir()+"/test.db", sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	out := &bytes.Buffer{}
	r := &repl{store: store, manager: session.NewSessionManager(store), out: out, cfg: &Config{WorkDir: t.TempDir()}}
	return r, out
}

func TestHandleCommands(t *testing.T) {
	r, out := newTestREPL(t)

	if err := r.handle(context.Background(), "/help"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/resume <id>") {
		t.Fatalf("help output missing commands: %q", out.String())
	}

	// Chat with no agent is a clear error, not a panic.
	out.Reset()
	if err := r.handle(context.Background(), "hello"); err == nil {
		t.Fatal("chat without session should error")
	}

	// /new creates and binds a session.
	out.Reset()
	if err := r.handle(context.Background(), "/new"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[new session ") {
		t.Fatalf("new output = %q", out.String())
	}
	if r.current == nil || r.agent == nil {
		t.Fatal("/new did not bind session + agent")
	}

	// /tag, /list, /search against the live session.
	if err := r.handle(context.Background(), "/tag smoke"); err != nil {
		t.Fatal(err)
	}
	if len(r.current.Tags) != 1 || r.current.Tags[0] != "smoke" {
		t.Fatalf("tags = %v", r.current.Tags)
	}
	out.Reset()
	if err := r.handle(context.Background(), "/search smoke"); err != nil {
		t.Fatal(err)
	}
	// Session has no searchable messages yet — but search must not error.
	_ = out

	// /close syncs + closes and clears the binding.
	out.Reset()
	if err := r.handle(context.Background(), "/close"); err != nil {
		t.Fatal(err)
	}
	if r.current != nil || r.agent != nil {
		t.Fatal("/close did not clear the binding")
	}

	// Unknown command.
	out.Reset()
	if err := r.handle(context.Background(), "/bogus"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unknown command") {
		t.Fatalf("unknown command output = %q", out.String())
	}
}

func TestHandleQuitSyncs(t *testing.T) {
	r, out := newTestREPL(t)
	if err := r.handle(context.Background(), "/new"); err != nil {
		t.Fatal(err)
	}
	if err := r.handle(context.Background(), "/quit"); err != nil {
		t.Fatal(err)
	}
	if !r.quit {
		t.Fatal("quit flag not set")
	}
	if !strings.Contains(out.String(), "[session") {
		t.Fatalf("quit did not sync: %q", out.String())
	}
}
