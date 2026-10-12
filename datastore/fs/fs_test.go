package fs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/conformance"
	"github.com/flintwillow-software/opennavi/datastore/fs"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// openFresh returns a backend on its own root: the suite opens a backend per
// check, and one root is one writer (each root takes a lock).
func openFresh(t *testing.T) func() (datastore.Backend, error) {
	t.Helper()
	base := t.TempDir()
	var n int
	return func() (datastore.Backend, error) {
		n++
		return fs.New(filepath.Join(base, fmt.Sprintf("state-%d", n)))
	}
}

// TestConformance runs the contract's shared suite against the fs backend.
func TestConformance(t *testing.T) {
	conformance.Run(t, openFresh(t))
}

func TestCandidateIsTheDefault(t *testing.T) {
	c := fs.Candidate()
	if c.Name != "fs" || !c.Default || c.Open == nil {
		t.Fatalf("candidate = %+v, want the default fs backend", c)
	}
	if _, err := c.Open(datastore.Params{fs.ParamPath: t.TempDir()}); err != nil {
		t.Fatalf("Open via candidate: %v", err)
	}
}

// TestOpenRequiresARoot: a backend with no root must fail rather than guess a
// location to write a navi's state.
func TestOpenRequiresARoot(t *testing.T) {
	_, err := fs.Open(datastore.Params{})
	if err == nil {
		t.Fatal("Open with no path must fail")
	}
	if !strings.Contains(err.Error(), fs.ParamPath) {
		t.Fatalf("error should name the missing parameter: %v", err)
	}
}

// TestThoughtFileIsHumanReadable is the point of the backend: a person opens
// thought.md and understands the thought and its conversation.
func TestThoughtFileIsHumanReadable(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	th := &thought.Thought{
		ID: "t1", Prompt: "deploy the thing", Priority: 3,
		Status: thought.ThoughtPending, DependsOn: []string{"t0"},
	}
	if err := b.Thoughts().SaveThought(th); err != nil {
		t.Fatal(err)
	}
	if err := b.Thoughts().SaveTranscript("t1", []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "what is 2+2?"),
		datastore.NewEntry(datastore.RoleAssistant, "4"),
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "thoughts", "t1", "thought.md"))
	if err != nil {
		t.Fatalf("read thought.md: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"prompt: deploy the thing", "depends_on:", "has_transcript: true", "messages: 2", "what is 2+2?", "4"} {
		if !strings.Contains(text, want) {
			t.Errorf("thought.md does not contain %q:\n%s", want, text)
		}
	}
}

// TestEnumerationReadsNoBodies is the property that makes retention affordable,
// asserted at the file level as well as through the suite's read reporter.
func TestEnumerationReadsNoBodies(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.Thoughts().SaveThought(&thought.Thought{ID: "t1", Prompt: "p", Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	entries := make([]datastore.Entry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, datastore.NewEntry(datastore.RoleAssistant, strings.Repeat("body ", 100)))
	}
	if err := b.Thoughts().SaveTranscript("t1", entries); err != nil {
		t.Fatal(err)
	}

	b.ResetReads()
	if _, err := b.Thoughts().LoadThoughts(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Thoughts().LoadThought("t1"); err != nil {
		t.Fatal(err)
	}
	info, err := b.Thoughts().TranscriptInfo("t1")
	if err != nil {
		t.Fatal(err)
	}
	if info.MessageCount != len(entries) {
		t.Fatalf("TranscriptInfo.MessageCount = %d, want %d", info.MessageCount, len(entries))
	}
	if n := b.BodyReads(); n != 0 {
		t.Errorf("enumeration loaded %d transcript body/bodies: cold start must read frontmatter only", n)
	}

	if _, err := b.Thoughts().LoadTranscript("t1"); err != nil {
		t.Fatal(err)
	}
	if n := b.BodyReads(); n == 0 {
		t.Error("LoadTranscript did not report a body read")
	}
}

// TestRestartReadsBack is the durability payoff: state written by one process
// is read by the next.
func TestRestartReadsBack(t *testing.T) {
	root := t.TempDir()

	first, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	th := &thought.Thought{
		ID: "t1", Prompt: "survive", Status: thought.ThoughtComplete, Result: "done",
		Checkpoint: []byte("navicp1:[]"), Metadata: map[string]string{"origin": "self"},
	}
	if err := first.Thoughts().SaveThought(th); err != nil {
		t.Fatal(err)
	}
	if err := first.Thoughts().SaveTranscript("t1", []datastore.Entry{
		datastore.NewEntry(datastore.RoleUser, "remember me"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ToolState().ReplaceTodos("navi-a", []datastore.Todo{{Text: "durable todo", Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ToolState().UpsertNote("navi-a", datastore.Note{Title: "runbook", Body: "how to deploy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeUser, Subject: principal.MustParse("user:flintwillow"), Content: "prefers terse output",
		Tags: []string{"style"}, Importance: 0.7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := fs.New(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	got, err := second.Thoughts().LoadThought("t1")
	if err != nil {
		t.Fatalf("LoadThought after restart: %v", err)
	}
	if got.Result != "done" || string(got.Checkpoint) != "navicp1:[]" || got.Metadata["origin"] != "self" {
		t.Fatalf("thought did not survive the restart: %+v", got)
	}
	entries, err := second.Thoughts().LoadTranscript("t1")
	if err != nil || len(entries) != 1 || entries[0].Message != "remember me" {
		t.Fatalf("transcript after restart: %+v (%v)", entries, err)
	}
	if todos, err := second.ToolState().ListTodos("navi-a", "", 0); err != nil || len(todos) != 1 || todos[0].Text != "durable todo" {
		t.Fatalf("todos after restart: %+v (%v)", todos, err)
	}
	if notes, err := second.ToolState().ReadNotes("navi-a", datastore.NoteQuery{}); err != nil || len(notes) != 1 || notes[0].Body != "how to deploy" {
		t.Fatalf("notes after restart: %+v (%v)", notes, err)
	}
	if mem, err := second.Memory().Recall(datastore.RecallQuery{Scope: datastore.ScopeUser}); err != nil || len(mem) != 1 || mem[0].Content != "prefers terse output" {
		t.Fatalf("memory after restart: %+v (%v)", mem, err)
	}
}

// TestLegacyBareSubjectReadsAsNavi: a memory file written before the principal
// reference carries the navi's bare name; the backend reads it back as
// navi:<name> so the qualified query still finds it (ADR 0021 decision 4).
func TestLegacyBareSubjectReadsAsNavi(t *testing.T) {
	root := t.TempDir()

	first, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := first.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeUser, Subject: principal.MustParse("navi:legacy-navi"), Content: "prefers terse output",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewrite the stored subject to the pre-principal bare form.
	path := filepath.Join(root, "memory", "user", rec.ID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.ReplaceAll(string(data), "navi:legacy-navi", "legacy-navi")
	if legacy == string(data) {
		t.Fatalf("fixture did not carry the qualified subject: %s", data)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := fs.New(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	got, err := second.Memory().Recall(datastore.RecallQuery{Subject: principal.MustParse("navi:legacy-navi")})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Subject != principal.MustParse("navi:legacy-navi") {
		t.Fatalf("legacy row = %+v, want one record keyed navi:legacy-navi", got)
	}
}

// TestOneWriterPerRoot: a second process on the same root fails loudly instead
// of silently interleaving writes, and the root is reusable once released.
func TestOneWriterPerRoot(t *testing.T) {
	root := t.TempDir()

	holder, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fs.New(root)
	if err == nil {
		second.Close()
		holder.Close()
		t.Fatal("a second backend on the same root must fail")
	}
	if !strings.Contains(err.Error(), "another process") {
		t.Fatalf("error should explain the exclusivity: %v", err)
	}

	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := fs.New(root)
	if err != nil {
		t.Fatalf("root should be reusable after release: %v", err)
	}
	third.Close()
}

// TestTodoManifestAnchorsTheList: the manifest is the authoritative ordered id
// list, and a replaced item leaves no orphan file behind.
func TestTodoManifestAnchorsTheList(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	stored, err := b.ToolState().ReplaceTodos("navi-a", []datastore.Todo{
		{Text: "second", Priority: 5},
		{Text: "first", Priority: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "todos", "navi-a")
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.md"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, item := range stored {
		if !strings.Contains(string(raw), item.ID) {
			t.Errorf("manifest does not list %s:\n%s", item.ID, raw)
		}
		if _, err := os.Stat(filepath.Join(dir, item.ID+".md")); err != nil {
			t.Errorf("item file for %s missing: %v", item.ID, err)
		}
	}

	// Replacing the list leaves exactly the new items behind.
	kept := stored[1]
	if _, err := b.ToolState().ReplaceTodos("navi-a", []datastore.Todo{kept}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "manifest.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), stored[0].ID) {
		t.Errorf("manifest still lists the replaced item:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, stored[0].ID+".md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan item file survived the replace: %v", err)
	}
}

// TestFailedWriteKeepsThePreviousTranscript: a write that cannot land leaves
// the previous valid file in place (the atomicity promise).
func TestFailedWriteKeepsThePreviousTranscript(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions cannot be used to force the failure")
	}
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	original := []datastore.Entry{datastore.NewEntry(datastore.RoleUser, "keep me")}
	if err := b.Thoughts().SaveTranscript("t1", original); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, "thoughts", "t1")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	err = b.Thoughts().SaveTranscript("t1", []datastore.Entry{datastore.NewEntry(datastore.RoleUser, "never lands")})
	if err == nil {
		t.Skip("the filesystem allowed the write despite the read-only directory")
	}

	os.Chmod(dir, 0o755)
	got, err := b.Thoughts().LoadTranscript("t1")
	if err != nil {
		t.Fatalf("LoadTranscript after a failed write: %v", err)
	}
	if len(got) != 1 || got[0].Message != "keep me" {
		t.Fatalf("a failed write damaged the previous transcript: %+v", got)
	}
	// And no temp file was left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
