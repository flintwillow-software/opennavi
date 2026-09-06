package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return New(sqlOpen(t, t.TempDir()+"/memory.db"))
}

func TestPutGetUserDocument(t *testing.T) {
	s := testStore(t)
	err := s.Put(Entry{
		Scope: ScopeUser, Subject: "nicholas",
		Content: "# User: nicholas\n\n## Preferences\n- Direct, no fluff",
		Source:  "user_memory", Tags: []string{"prefs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ScopeUser, "nicholas")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != ScopeUser || !strings.Contains(got.Content, "Direct, no fluff") {
		t.Fatalf("got = %+v", got)
	}
	// Whole-document replace.
	if err := s.Put(Entry{Scope: ScopeUser, Subject: "nicholas", Content: "replaced"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ScopeUser, "nicholas")
	if got.Content != "replaced" {
		t.Fatalf("content = %q, want whole-document replace", got.Content)
	}
}

func TestPutRejectsMemoriesScope(t *testing.T) {
	s := testStore(t)
	if err := s.Put(Entry{Scope: ScopeMemories, Subject: "x", Content: "c"}); err == nil {
		t.Fatal("Put with memories scope should error (use AddEntry)")
	}
}

func TestPutRequiresSubject(t *testing.T) {
	s := testStore(t)
	if err := s.Put(Entry{Scope: ScopeUser, Content: "c"}); err == nil {
		t.Fatal("missing subject should error")
	}
}

func TestAddEntryAndGet(t *testing.T) {
	s := testStore(t)
	e, err := s.AddEntry(Entry{
		Scope: ScopeMemories, Subject: "go-version",
		Content: "Project uses Go 1.22 with sqlc.", Source: "auto_store",
		Tags: []string{"go", "testing"}, Importance: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.Importance != 0.9 {
		t.Fatalf("entry = %+v", e)
	}
	got, err := s.Get(ScopeMemories, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "Project uses Go 1.22 with sqlc." || got.Source != "auto_store" {
		t.Fatalf("got = %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("tags = %v", got.Tags)
	}
}

func TestAddEntryRejectsOtherScopes(t *testing.T) {
	s := testStore(t)
	if _, err := s.AddEntry(Entry{Scope: ScopeUser, Content: "c"}); err == nil {
		t.Fatal("AddEntry with user scope should error")
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Content: "   "}); err == nil {
		t.Fatal("empty content should error")
	}
}

func TestListSubjects(t *testing.T) {
	s := testStore(t)
	for _, subj := range []string{"zeta", "alpha", "mike"} {
		if err := s.Put(Entry{Scope: ScopeNavi, Subject: subj, Content: "notes about " + subj}); err != nil {
			t.Fatal(err)
		}
	}
	subjects, err := s.ListSubjects(ScopeNavi)
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 3 || subjects[0] != "alpha" {
		t.Fatalf("subjects = %v", subjects)
	}
	// Memories scope is not a document scope.
	if _, err := s.ListSubjects(ScopeMemories); err == nil {
		t.Fatal("ListSubjects(memories) should error")
	}
}

func TestSearchFTS(t *testing.T) {
	s := testStore(t)
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "go-setup",
		Content: "The project uses Go 1.22 and sqlc for database queries."}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "staging-ssh",
		Content: "The staging server needs SSH port 2222, not 22."}); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search("sqlc database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "go-setup" {
		t.Fatalf("results = %+v", results)
	}

	results, err = s.Search("2222", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "staging-ssh" {
		t.Fatalf("port search = %+v", results)
	}
}

func TestSearchUserNaviTiersExcluded(t *testing.T) {
	// Only the memories tier is searchable; user/navi documents are
	// always-in-context, not search targets.
	s := testStore(t)
	if err := s.Put(Entry{Scope: ScopeUser, Subject: "u1", Content: "kubernetes deployment notes"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.Search("kubernetes", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("user scope leaked into search: %+v", results)
	}
}

func TestSearchSyntaxRejected(t *testing.T) {
	s := testStore(t)
	if _, err := s.Search("foo* OR bar:^baz", 10); err == nil {
		t.Fatal("FTS5 operator injection should be rejected")
	}
	empty, err := s.Search("", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty query: %v, %d", err, len(empty))
	}
}

func TestPruneExpired(t *testing.T) {
	s := testStore(t)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "expired", Content: "old", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "alive", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: "future", Content: "later", ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}

	n, err := s.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	// The expired entry is gone; the others remain.
	if _, err := s.Get(ScopeMemories, "expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired entry still present: %v", err)
	}
	alive, err := s.Get(ScopeMemories, "alive")
	if err != nil || alive.Content != "new" {
		t.Fatalf("alive entry affected: %v, %+v", err, alive)
	}
	futureEntry, _ := s.Get(ScopeMemories, "future")
	if futureEntry.Content != "later" {
		t.Fatalf("future entry lost: %+v", futureEntry)
	}
}

func TestDelete(t *testing.T) {
	s := testStore(t)
	if err := s.Put(Entry{Scope: ScopeNavi, Subject: "project-manager", Content: "notes"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ScopeNavi, "project-manager"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ScopeNavi, "project-manager"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete error = %v", err)
	}
}

func TestExportMarkdownRoundTrip(t *testing.T) {
	s := testStore(t)
	if err := s.Put(Entry{
		Scope: ScopeUser, Subject: "nicholas",
		Content: "## Preferences\n- Direct, no fluff",
		Tags:    []string{"prefs"}, Source: "user_memory",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEntry(Entry{
		Scope: ScopeMemories, Subject: "go-setup",
		Content: "Project uses Go 1.22.", Source: "auto_store", Tags: []string{"go"},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	memDir := filepath.Join(dir, "memories")

	paths, err := s.ExportMarkdown(ScopeUser, userDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "nicholas.md") {
		t.Fatalf("paths = %v", paths)
	}
	if _, err := s.ExportMarkdown(ScopeMemories, memDir); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(filepath.Join(userDir, "nicholas.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "Direct, no fluff") || !strings.Contains(string(blob), "# User: nicholas") {
		t.Fatalf("user export = %q", blob)
	}
	entries, err := os.ReadDir(memDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("memories export files = %v, %v", entries, err)
	}
	if !strings.HasSuffix(entries[0].Name(), ".md") {
		t.Fatalf("memories export file = %q", entries[0].Name())
	}
}

func TestSearchQueryApostrophe(t *testing.T) {
	s := testStore(t)
	if _, err := s.Search("don't", 10); err != nil {
		t.Fatalf("apostrophe query errored: %v", err)
	}
}
