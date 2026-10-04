package sqlite_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/conformance"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/thought"
)

// openFresh returns a backend on its own database file: the suite opens a
// backend per check, so no check can be blamed on state another left behind.
func openFresh(t *testing.T) conformance.OpenFunc {
	t.Helper()
	base := t.TempDir()
	var n int
	return func() (datastore.Backend, error) {
		n++
		return sqlite.Open(filepath.Join(base, fmt.Sprintf("navi-%d.db", n)), sqlite.Options{})
	}
}

// TestConformance runs the contract's shared suite against the sqlite backend.
// It covers the metadata-without-bodies property through
// conformance.ReadReporter, which this backend implements.
func TestConformance(t *testing.T) {
	conformance.Run(t, openFresh(t))
}

// TestCandidateIsNotTheDefault: `fs` is the compiled default (ADR 0011), and
// sqlite is the single-file alternative selected by name.
func TestCandidateIsNotTheDefault(t *testing.T) {
	c := sqlite.Candidate()
	if c.Name != "sqlite" || c.Default || c.Open == nil {
		t.Fatalf("candidate = %+v, want a selectable non-default sqlite backend", c)
	}
	if _, err := c.Open(datastore.Params{sqlite.ParamPath: filepath.Join(t.TempDir(), "navi.db")}); err != nil {
		t.Fatalf("Open via candidate: %v", err)
	}
}

// TestOpenRequiresAPath: a backend with no path must fail rather than guess a
// location to write a navi's state.
func TestOpenRequiresAPath(t *testing.T) {
	_, err := sqlite.OpenParams(datastore.Params{})
	if err == nil {
		t.Fatal("OpenParams with no path must fail")
	}
	if !strings.Contains(err.Error(), sqlite.ParamPath) {
		t.Fatalf("error should name the missing parameter: %v", err)
	}
}

// TestWALIsEnabled: the backend is configured for the datastore's single
// writer — WAL so a flush does not block a read, plus a busy timeout so a
// second process sharing the file (the `navi enqueue` mailbox) waits rather
// than fails.
func TestWALIsEnabled(t *testing.T) {
	b, err := sqlite.Open(filepath.Join(t.TempDir(), "navi.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var mode string
	if err := b.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var busy int
	if err := b.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy <= 0 {
		t.Fatalf("busy_timeout = %d, want a positive wait", busy)
	}
}

// TestTranscriptBodiesAreEncrypted: the connection cipher applies to the
// transcript body (message and tool input), not to the metadata projection, so
// a transcript sits encrypted at rest while transcript enumeration stays
// plaintext and body-free.
func TestTranscriptBodiesAreEncrypted(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cipher, err := sqlite.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sqlite.Open(filepath.Join(t.TempDir(), "navi.db"), sqlite.Options{Cipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	const secret = "the launch code is hunter2"
	entries := []datastore.Entry{
		{
			ID: "e1", Time: time.Now().UTC(), Role: datastore.RoleAssistant,
			Message: secret, ToolCallID: "call-1", ToolName: "shell_exec",
			ToolInput: []byte(`{"command":"use hunter2"}`),
		},
	}
	if err := b.Thoughts().SaveTranscript("t-enc", entries); err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}

	var message, toolInput string
	if err := b.DB().QueryRow(`SELECT message, tool_input FROM thought_transcripts WHERE thought_id = 't-enc'`).
		Scan(&message, &toolInput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message, "hunter2") || strings.Contains(toolInput, "hunter2") {
		t.Fatalf("plaintext secret in the database: message=%q tool_input=%q", message, toolInput)
	}
	if !strings.HasPrefix(message, "enc1:") || !strings.HasPrefix(toolInput, "enc1:") {
		t.Fatalf("transcript body is not ciphertext: message=%q tool_input=%q", message, toolInput)
	}

	got, err := b.Thoughts().LoadTranscript("t-enc")
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(got) != 1 || got[0].Message != secret || string(got[0].ToolInput) != `{"command":"use hunter2"}` {
		t.Fatalf("encrypted transcript did not round-trip: %+v", got)
	}

	// Metadata reads still work and never decrypt a body.
	b.ResetReads()
	info, err := b.Thoughts().TranscriptInfo("t-enc")
	if err != nil || info.MessageCount != 1 {
		t.Fatalf("TranscriptInfo = %+v (%v), want 1 message", info, err)
	}
	if n := b.BodyReads(); n != 0 {
		t.Fatalf("TranscriptInfo read %d bodies, want 0", n)
	}
}

// TestEmptyTranscriptExists: a transcript written with no entries is a
// transcript (zero messages), not a missing one — the projection records the
// write even though there is no body.
func TestEmptyTranscriptExists(t *testing.T) {
	b, err := sqlite.Open(filepath.Join(t.TempDir(), "navi.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.Thoughts().SaveTranscript("t-empty", nil); err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}
	info, err := b.Thoughts().TranscriptInfo("t-empty")
	if err != nil {
		t.Fatalf("TranscriptInfo after empty write: %v", err)
	}
	if info.MessageCount != 0 {
		t.Fatalf("MessageCount = %d, want 0", info.MessageCount)
	}
	got, err := b.Thoughts().LoadTranscript("t-empty")
	if err != nil {
		t.Fatalf("LoadTranscript after empty write: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty transcript has %d entries", len(got))
	}
}

// TestExistingDatabaseReopensWithoutLoss: `Open` migrates in place, so a
// database written by an earlier run keeps its thoughts — the pre-rename
// `navi.db` is upgraded, never half-read. The retired sessions/messages tables
// stay untouched alongside the new transcript tables.
func TestExistingDatabaseReopensWithoutLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navi.db")

	first, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	th := &thought.Thought{ID: "t-keep", Prompt: "survive the reopen", Status: thought.ThoughtPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := first.Thoughts().SaveThought(th); err != nil {
		t.Fatalf("SaveThought: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := first.DB().Exec(`INSERT INTO sessions (id, agent_id, profile, created_at, updated_at)
		VALUES ('legacy', 'agent', 'profile', ?, ?)`, now, now); err != nil {
		t.Fatalf("write legacy session row: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	got, err := second.Thoughts().LoadThought("t-keep")
	if err != nil {
		t.Fatalf("LoadThought after reopen: %v", err)
	}
	if got.Prompt != "survive the reopen" {
		t.Fatalf("prompt = %q, want the stored value", got.Prompt)
	}
}
