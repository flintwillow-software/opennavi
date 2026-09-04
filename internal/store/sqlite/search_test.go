package sqlite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/session"
)

// seedSearchSessions creates sessions with distinctive content.
func seedSearchSessions(t *testing.T, st *Store) (*session.Session, *session.Session) {
	t.Helper()
	deploy := session.NewSession("agent-1", "/w")
	deploy.Name = "deploy discussion"
	deploy.AppendEntry(session.NewEntry(session.RoleUser, "how do we deploy the kubernetes cluster"))
	deploy.AppendEntry(session.NewEntry(session.RoleAssistant, "use the deploy script via kubectl rollout"))
	if err := st.SaveSession(deploy); err != nil {
		t.Fatal(err)
	}

	garden := session.NewSession("agent-1", "/w")
	garden.Name = "garden notes"
	garden.AppendEntry(session.NewEntry(session.RoleUser, "tomato plants need watering daily"))
	if err := st.SaveSession(garden); err != nil {
		t.Fatal(err)
	}
	return deploy, garden
}

func TestSearchFindsAndRanks(t *testing.T) {
	st := openTestStore(t, Options{})
	deploy, garden := seedSearchSessions(t, st)

	results, err := st.Search("kubernetes deploy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (only deploy session matches)", len(results))
	}
	if results[0].ID != deploy.ID {
		t.Fatalf("matched %s, want %s", results[0].ID, deploy.ID)
	}

	// Each session matches a different term.
	results, err = st.Search("tomato", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != garden.ID {
		t.Fatalf("tomato results = %+v", results)
	}
}

func TestSearchRespectsLimit(t *testing.T) {
	st := openTestStore(t, Options{})
	for i := 0; i < 5; i++ {
		s := session.NewSession("a", "")
		s.AppendEntry(session.NewEntry(session.RoleUser, "shared keyword everywhere"))
		if err := st.SaveSession(s); err != nil {
			t.Fatal(err)
		}
	}
	results, err := st.Search("shared", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("limit results = %d, want 3", len(results))
	}
}

func TestSearchRejectsFTSSyntax(t *testing.T) {
	st := openTestStore(t, Options{})
	if _, err := st.Search("foo* OR bar:^baz", 10); err == nil {
		t.Fatal("FTS5 operator injection should be rejected")
	}
	if _, err := st.Search(`  "  `, 10); err == nil {
		t.Fatal("quote-only query should be rejected")
	}
	empty, err := st.Search("", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty query: %v, %d", err, len(empty))
	}
}

func TestSearchAfterResaveNoDuplicateOrStaleIndex(t *testing.T) {
	// Whole-row-replace SaveSession deletes and reinserts every message,
	// so the external-content delete triggers fire constantly. Verifies
	// the index stays consistent (no duplicates, no stale rows).
	st := openTestStore(t, Options{})
	s := session.NewSession("a", "")
	s.AppendEntry(session.NewEntry(session.RoleUser, "kubernetes rollout story"))

	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	// Resave twice more: delete+reinsert cycles.
	s.AppendEntry(session.NewEntry(session.RoleUser, "more kubernetes details"))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	// Change content: old phrase must vanish from the index.
	s.Entries[0].Message = "unrelated words now"
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	// "story" lived in entry 0, which was replaced: a stale index row
	// would still match it. "kubernetes" lives in entry 1, which was
	// reinserted twice: a duplicate index row would surface as extra
	// rows in a direct count.
	results, err := st.Search("rollout story", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("stale index rows for replaced content: %d results", len(results))
	}
	results, err = st.Search("kubernetes details", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("search after resave = %d results, want 1", len(results))
	}
	var indexRows int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'kubernetes'`).Scan(&indexRows); err != nil {
		t.Fatal(err)
	}
	if indexRows != 1 {
		t.Fatalf("index rows for 'kubernetes' = %d, want 1 (no duplicates after reinsert)", indexRows)
	}
}

func TestSearchExcludesDeletedSessions(t *testing.T) {
	st := openTestStore(t, Options{})
	s := session.NewSession("a", "")
	s.AppendEntry(session.NewEntry(session.RoleUser, "delete me from search"))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession(s.ID); err != nil {
		t.Fatal(err)
	}
	results, err := st.Search("delete", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("deleted session still searchable: %+v", results)
	}
}

func TestSearchEncryptedContentNotMatched(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(3)
	}
	cipher, err := NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	st := openTestStore(t, Options{Cipher: cipher})
	s := session.NewSession("a", "")
	s.AppendEntry(session.NewEntry(session.RoleUser, "classified kubernetes secrets"))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	results, err := st.Search("classified", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("encrypted content matched search: %+v", results)
	}
}

func TestImportLargeMessage(t *testing.T) {
	// Regression: the import scanner used the 64KB default token size,
	// but messages can exceed it (shell_exec output caps at 100KB,
	// file_read at 512KB) — large sessions exported fine but failed to
	// import with 'token too long'.
	st := openTestStore(t, Options{})
	s := session.NewSession("a", "")
	s.Name = "big output"
	big := strings.Repeat("x", 100*1024) + " terminus"
	s.AppendEntry(session.NewEntry(session.RoleTool, big))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "big.jsonl")
	if err := st.ExportJSONL(s, path); err != nil {
		t.Fatal(err)
	}
	st2 := openTestStore(t, Options{})
	imported, err := st2.ImportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Entries[0].Message != big {
		t.Fatalf("imported message truncated: %d bytes, want %d", len(imported.Entries[0].Message), len(big))
	}
}

func TestSearchApostropheTerms(t *testing.T) {
	st := openTestStore(t, Options{})
	s := session.NewSession("a", "")
	s.AppendEntry(session.NewEntry(session.RoleUser, "cannot find the dont entry"))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	// "don't"-style input must not FTS5-error; the apostrophe is
	// stripped and the remainder matches.
	results, err := st.Search("don't", 10)
	if err != nil {
		t.Fatalf("apostrophe query errored: %v", err)
	}
	_ = results
}

func TestTagsWithCommasSurviveListing(t *testing.T) {
	st := openTestStore(t, Options{})
	s := session.NewSession("a", "")
	s.Tags = []string{"one,two", "three"}
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListSessions(session.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Tags) != 2 || list[0].Tags[0] != "one,two" || list[0].Tags[1] != "three" {
		t.Fatalf("tags = %v, want [one,two three]", list[0].Tags)
	}
	// Tag filter with the comma tag works too.
	filtered, err := st.ListSessions(session.ListOptions{Tags: []string{"one,two"}})
	if err != nil || len(filtered) != 1 {
		t.Fatalf("comma-tag filter: %v, %d", err, len(filtered))
	}
}

func TestExportImportJSONLRoundTrip(t *testing.T) {
	st := openTestStore(t, Options{})
	s := testSession("", 2)
	s.Name = "roundtrip"
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "export.jsonl")
	if err := st.ExportJSONL(s, path); err != nil {
		t.Fatal(err)
	}

	// Import into a fresh store.
	st2 := openTestStore(t, Options{})
	imported, err := st2.ImportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID != s.ID || imported.Name != s.Name || imported.AgentID != s.AgentID {
		t.Fatalf("imported metadata: %+v", imported)
	}
	if len(imported.Entries) != len(s.Entries) {
		t.Fatalf("imported %d entries, want %d", len(imported.Entries), len(s.Entries))
	}
	for i := range s.Entries {
		w, g := s.Entries[i], imported.Entries[i]
		if g.Role != w.Role || g.Message != w.Message || g.ToolName != w.ToolName || g.ToolCallID != w.ToolCallID {
			t.Fatalf("entry %d: got %+v want %+v", i, g, w)
		}
		if !g.Time.Equal(w.Time) {
			t.Fatalf("entry %d time: %v vs %v", i, g.Time, w.Time)
		}
	}

	// Imported session is searchable in the new store.
	results, err := st2.Search("hello", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != s.ID {
		t.Fatalf("imported session not searchable: %+v", results)
	}
}

func TestImportJSONExistingIDRejected(t *testing.T) {
	st := openTestStore(t, Options{})
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.jsonl")
	if err := st.ExportJSONL(s, path); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportJSONL(path); err == nil {
		t.Fatal("import over an existing session ID should fail")
	}
}

func TestExportJSON(t *testing.T) {
	st := openTestStore(t, Options{})
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := st.ExportJSON(s, path); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
		AgentID   string `json:"agent_id"`
		CreatedAt string `json:"created_at"`
		Messages  []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
			ToolName  string `json:"tool_name"`
			ToolInput string `json:"tool_input"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SessionID != s.ID || doc.Name != s.Name || len(doc.Messages) != len(s.Entries) {
		t.Fatalf("json export = %+v", doc)
	}
	// Tool message carries tool_name per spec §07.2.
	found := false
	for _, m := range doc.Messages {
		if m.Role == "assistant" && m.ToolName == "file_read" {
			found = true
			if !strings.Contains(m.ToolInput, "path") {
				t.Fatalf("tool input missing: %q", m.ToolInput)
			}
		}
	}
	if !found {
		t.Fatal("tool metadata missing from JSON export")
	}
}

func TestImportEmptyAndBadFiles(t *testing.T) {
	st := openTestStore(t, Options{})
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(empty, nil, 0o644)
	if _, err := st.ImportJSONL(empty); err == nil {
		t.Fatal("empty file should fail")
	}

	bad := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(bad, []byte(`{"session_id":"x"}`+"\n"+`{not json}`+"\n"), 0o644)
	if _, err := st.ImportJSONL(bad); err == nil {
		t.Fatal("malformed line should fail")
	}

	missing := filepath.Join(dir, "missing.jsonl")
	if _, err := st.ImportJSONL(missing); err == nil {
		t.Fatal("missing file should fail")
	}
}
