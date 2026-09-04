package sqlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/internal/session"
)

func testSession(id string, n int) *session.Session {
	s := session.NewSession("agent-1", "/tmp/work")
	if id != "" {
		s.ID = id
	}
	s.Name = "test session"
	s.Profile = "p1"
	s.AppendEntry(session.NewEntry(session.RoleUser, "hello"))
	s.AppendEntry(func() session.SessionEntry {
		e := session.NewEntry(session.RoleAssistant, "calling tool")
		e.ToolCallID = "tc_1_0"
		e.ToolName = "file_read"
		e.ToolInput = json.RawMessage(`{"path":"x.txt"}`)
		return e
	}())
	s.AppendEntry(func() session.SessionEntry {
		e := session.NewEntry(session.RoleTool, "file contents")
		e.ToolCallID = "tc_1_0"
		return e
	}())
	for i := 0; i < n; i++ {
		s.AppendEntry(session.NewEntry(session.RoleUser, fmt.Sprintf("extra %d", i)))
	}
	return s
}

func openTestStore(t *testing.T, opts Options) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sessions.db"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSaveLoadRoundTrip(t *testing.T) {
	st := openTestStore(t, Options{})
	want := testSession("", 2)
	if err := st.SaveSession(want); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadSession(want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.AgentID != want.AgentID || got.Name != want.Name ||
		got.Profile != want.Profile || got.WorkDir != want.WorkDir || got.Status != session.StatusActive {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if len(got.Entries) != len(want.Entries) {
		t.Fatalf("entries: got %d, want %d", len(got.Entries), len(want.Entries))
	}
	for i := range want.Entries {
		w, g := want.Entries[i], got.Entries[i]
		if g.ID != w.ID || g.Role != w.Role || g.Message != w.Message ||
			g.ToolCallID != w.ToolCallID || g.ToolName != w.ToolName {
			t.Fatalf("entry %d mismatch:\n got %+v\nwant %+v", i, g, w)
		}
		if string(g.ToolInput) != string(w.ToolInput) {
			t.Fatalf("entry %d tool input: %s vs %s", i, g.ToolInput, w.ToolInput)
		}
	}
	// LoadSession positions Current at the last entry.
	if got.Current != len(want.Entries)-1 {
		t.Fatalf("Current = %d", got.Current)
	}
}

func TestCurrentPositionPersists(t *testing.T) {
	// Regression: Current was not persisted, so a session rewound via
	// SetCurrent silently forked from the tip after a store round-trip.
	st := openTestStore(t, Options{})
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(0); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != 0 {
		t.Fatalf("Current = %d, want 0 (stored position lost)", got.Current)
	}

	// Out-of-range stored position clamps to the derived value rather
	// than erroring (entries may have changed since the save).
	s.Current = 99
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	got, err = st.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != len(got.Entries)-1 {
		t.Fatalf("out-of-range Current = %d, want clamped to %d", got.Current, len(got.Entries)-1)
	}

	// Empty sessions store NULL and load with Current == -1.
	empty := session.NewSession("a", "")
	if err := st.SaveSession(empty); err != nil {
		t.Fatal(err)
	}
	emptyGot, err := st.LoadSession(empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if emptyGot.Current != -1 {
		t.Fatalf("empty session Current = %d, want -1", emptyGot.Current)
	}
}

func TestSaveSessionWholeRowReplace(t *testing.T) {
	st := openTestStore(t, Options{})
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	// Simulate the manager's read-modify-write: the in-memory session
	// gains an entry and loses a tag, then the whole session is saved.
	s.Tags = []string{"old"}
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	s.AppendEntry(session.NewEntry(session.RoleUser, "later"))
	s.Tags = []string{"new"}
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	got, _ := st.LoadSession(s.ID)
	if len(got.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (whole-row replace, not append)", len(got.Entries))
	}
	if len(got.Tags) != 1 || got.Tags[0] != "new" {
		t.Fatalf("tags = %v, want [new]", got.Tags)
	}
}

func TestLoadMissingAndDelete(t *testing.T) {
	st := openTestStore(t, Options{})
	if _, err := st.LoadSession("nope"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("load error = %v", err)
	}
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession(s.ID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("double delete error = %v", err)
	}
	if _, err := st.LoadSession(s.ID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("load after delete error = %v", err)
	}
}

func TestListSessionsFiltersAndOrder(t *testing.T) {
	st := openTestStore(t, Options{})

	mk := func(agentID, profile string, status session.Status, tags []string, age time.Duration) *session.Session {
		s := session.NewSession(agentID, "")
		s.Profile = profile
		s.Status = status
		s.Tags = tags
		s.AppendEntry(session.NewEntry(session.RoleUser, "m"))
		s.UpdatedAt = time.Now().Add(-age)
		if err := st.SaveSession(s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	mk("a1", "p1", session.StatusActive, []string{"work"}, 1*time.Hour)
	mk("a1", "p1", session.StatusPaused, nil, 2*time.Hour)
	third := mk("a2", "p2", session.StatusActive, []string{"work", "urgent"}, 3*time.Hour)

	all, err := st.ListSessions(session.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d, want 3", len(all))
	}
	if all[0].MessageCount != 1 {
		t.Fatalf("message_count = %d", all[0].MessageCount)
	}

	byAgent, _ := st.ListSessions(session.ListOptions{AgentID: "a1"})
	if len(byAgent) != 2 {
		t.Fatalf("agent filter: %d", len(byAgent))
	}
	paused, _ := st.ListSessions(session.ListOptions{Status: session.StatusPaused})
	if len(paused) != 1 {
		t.Fatalf("status filter: %d", len(paused))
	}
	tagged, _ := st.ListSessions(session.ListOptions{Tags: []string{"urgent"}})
	if len(tagged) != 1 || tagged[0].ID != third.ID {
		t.Fatalf("tag filter: %+v", tagged)
	}
	since, _ := st.ListSessions(session.ListOptions{Since: time.Now().Add(-90 * time.Minute)})
	if len(since) != 1 {
		t.Fatalf("since filter: %d", len(since))
	}
	limited, _ := st.ListSessions(session.ListOptions{Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("limit: %d", len(limited))
	}
	offset, _ := st.ListSessions(session.ListOptions{Offset: 2})
	if len(offset) != 1 || offset[0].ID != third.ID {
		t.Fatalf("offset: %+v", offset)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	for i := 0; i < 2; i++ {
		st, err := Open(path, Options{})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Schema survives reopen (covered by the successful second Open).
}

func TestEncryptedRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cipher, err := NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	st := openTestStore(t, Options{Cipher: cipher})

	s := testSession("", 1)
	s.AppendEntry(session.NewEntry(session.RoleUser, "secret payload"))
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.Entries {
		if got.Entries[i].Message != s.Entries[i].Message {
			t.Fatalf("entry %d: %q vs %q", i, got.Entries[i].Message, s.Entries[i].Message)
		}
		if string(got.Entries[i].ToolInput) != string(s.Entries[i].ToolInput) {
			t.Fatalf("entry %d tool input mismatch", i)
		}
	}
}

func TestEncryptedAtRest(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(7)
	}
	cipher, err := NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	st, err := Open(path, Options{Cipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	s := testSession("", 0)
	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// The db file must not contain the plaintext message content.
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 {
		t.Fatal("empty db file")
	}
	if containsAny(string(blob), "hello", "file contents", `{"path":"x.txt"}`) {
		t.Fatal("plaintext found in db file despite encryption")
	}
}

func containsAny(blob string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(blob, sub) {
			return true
		}
	}
	return false
}

func TestKeyFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("NAVI_SESSION_KEY", "")
		key, err := KeyFromEnv()
		if err != nil || key != nil {
			t.Fatalf("key = %v, err = %v; want nil, nil", key, err)
		}
	})
	t.Run("hex", func(t *testing.T) {
		t.Setenv("NAVI_SESSION_KEY", fmt.Sprintf("%064x", 0xAB))
		key, err := KeyFromEnv()
		if err != nil || len(key) != 32 {
			t.Fatalf("key len = %d, err = %v", len(key), err)
		}
	})
	t.Run("base64", func(t *testing.T) {
		t.Setenv("NAVI_SESSION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes
		key, err := KeyFromEnv()
		if err != nil || len(key) != 32 {
			t.Fatalf("key len = %d, err = %v", len(key), err)
		}
	})
	t.Run("wrong-length", func(t *testing.T) {
		t.Setenv("NAVI_SESSION_KEY", "dG9vLXNob3J0")
		if _, err := KeyFromEnv(); err == nil {
			t.Fatal("short key should error")
		}
	})
}

func TestCipherFromEnvDefaultsToNoop(t *testing.T) {
	t.Setenv("NAVI_SESSION_KEY", "")
	c, err := CipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(NoopCipher); !ok {
		t.Fatalf("cipher = %T, want NoopCipher", c)
	}
}

func TestConcurrentSaveDifferentSessions(t *testing.T) {
	st := openTestStore(t, Options{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := session.NewSession(fmt.Sprintf("agent-%d", i), "")
			s.AppendEntry(session.NewEntry(session.RoleUser, fmt.Sprintf("msg %d", i)))
			if err := st.SaveSession(s); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	list, err := st.ListSessions(session.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 8 {
		t.Fatalf("sessions = %d, want 8", len(list))
	}
}

func TestOpenRejectsBadPath(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "no", "such", "dir", "x.db"), Options{})
	if err == nil {
		st.Close()
		t.Fatal("open into missing directory should fail")
	}
}
