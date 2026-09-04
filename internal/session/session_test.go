package session

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAppendEntryFillsIDAndTimestamp(t *testing.T) {
	s := NewSession("agent-1", "/tmp/work")
	if s.Status != StatusActive {
		t.Fatalf("new session status = %q, want active", s.Status)
	}
	if s.Current != -1 {
		t.Fatalf("new session Current = %d, want -1", s.Current)
	}

	s.AppendEntry(SessionEntry{Role: RoleUser, Message: "hi"})
	if s.Current != 0 || len(s.Entries) != 1 {
		t.Fatalf("after append: Current=%d, entries=%d", s.Current, len(s.Entries))
	}
	e := s.Entries[0]
	if e.ID == "" || e.Time.IsZero() {
		t.Fatalf("entry not filled in: %+v", e)
	}
	if !s.UpdatedAt.After(s.CreatedAt.Add(-time.Second)) {
		t.Fatalf("UpdatedAt not refreshed: %v", s.UpdatedAt)
	}
}

func TestGetEntryAndLabel(t *testing.T) {
	s := NewSession("a", "w")
	s.AppendEntry(NewEntry(RoleUser, "hello"))
	id := s.Entries[0].ID

	if _, err := s.GetEntry(id); err != nil {
		t.Fatal(err)
	}
	if err := s.LabelEntry(id, "greeting"); err != nil {
		t.Fatal(err)
	}
	if s.Entries[0].Label != "greeting" {
		t.Fatalf("label = %q", s.Entries[0].Label)
	}
	if _, err := s.GetEntry("missing"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing entry error = %v", err)
	}
}

func TestSetCurrentBounds(t *testing.T) {
	s := NewSession("a", "w")
	if err := s.SetCurrent(0); err == nil {
		t.Fatal("SetCurrent on empty session should fail")
	}
	s.AppendEntry(NewEntry(RoleUser, "one"))
	s.AppendEntry(NewEntry(RoleAssistant, "two"))
	if err := s.SetCurrent(1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(5); err == nil {
		t.Fatal("out-of-range SetCurrent should fail")
	}
}

func TestGetPathToRootRespectsCurrent(t *testing.T) {
	s := NewSession("a", "w")
	for _, m := range []string{"1", "2", "3"} {
		s.AppendEntry(NewEntry(RoleUser, m))
	}
	if err := s.SetCurrent(1); err != nil {
		t.Fatal(err)
	}
	path := s.GetPathToRoot()
	if len(path) != 2 || path[0].Message != "1" || path[1].Message != "2" {
		t.Fatalf("path = %v, want entries 1,2", path)
	}
}

func TestCloneIsDeep(t *testing.T) {
	s := NewSession("a", "w")
	s.Tags = []string{"x"}
	s.AppendEntry(NewEntry(RoleUser, "orig"))

	c := s.Clone()
	c.Tags[0] = "changed"
	c.Entries[0].Message = "mutated"

	if s.Tags[0] != "x" || s.Entries[0].Message != "orig" {
		t.Fatal("Clone is not deep: original mutated through copy")
	}
}

func TestNewUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewUUID()
		if len(id) != 36 {
			t.Fatalf("uuid %q has length %d", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate uuid %q", id)
		}
		seen[id] = true
	}
}

// ── MemoryStore ──────────────────────────────────────────────────────────

func TestMemoryStoreRoundTrip(t *testing.T) {
	st := NewMemoryStore()
	s := NewSession("agent-1", "/tmp")
	s.AppendEntry(NewEntry(RoleUser, "hello"))
	s.AppendEntry(NewEntry(RoleAssistant, "world"))

	if err := st.SaveSession(s); err != nil {
		t.Fatal(err)
	}

	// Mutate the original after saving: stored copy must be unaffected.
	s.Entries[0].Message = "mutated"
	s.AppendEntry(NewEntry(RoleUser, "extra"))

	loaded, err := st.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 || loaded.Entries[0].Message != "hello" {
		t.Fatalf("stored session mutated through caller: %+v", loaded.Entries)
	}

	// Mutate the loaded copy: store must be unaffected.
	loaded.Entries[1].Message = "mutated"
	again, _ := st.LoadSession(s.ID)
	if again.Entries[1].Message != "world" {
		t.Fatal("store mutated through loaded copy")
	}
}

func TestMemoryStoreLoadMissing(t *testing.T) {
	st := NewMemoryStore()
	if _, err := st.LoadSession("nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
	if err := st.DeleteSession("nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("delete error = %v, want ErrSessionNotFound", err)
	}
}

func TestMemoryStoreListFilters(t *testing.T) {
	st := NewMemoryStore()

	mk := func(agentID, profile string, status Status, tags []string, age time.Duration) *Session {
		s := NewSession(agentID, "")
		s.Profile = profile
		s.Status = status
		s.Tags = tags
		s.UpdatedAt = time.Now().Add(-age)
		if err := st.SaveSession(s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	mk("a1", "p1", StatusActive, []string{"work"}, 1*time.Hour)
	mk("a1", "p1", StatusPaused, nil, 2*time.Hour)
	mk("a2", "p2", StatusActive, []string{"work", "urgent"}, 3*time.Hour)
	old := mk("a1", "p1", StatusActive, nil, 48*time.Hour)

	// All sessions: newest first.
	all, err := st.ListSessions(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d sessions, want 4", len(all))
	}
	if all[0].MessageCount != 0 || all[0].Status != StatusActive {
		t.Fatalf("ordering wrong: %+v", all[0])
	}

	// Agent filter.
	byAgent, _ := st.ListSessions(ListOptions{AgentID: "a1"})
	if len(byAgent) != 3 {
		t.Fatalf("agent filter: got %d, want 3", len(byAgent))
	}

	// Status filter.
	paused, _ := st.ListSessions(ListOptions{Status: StatusPaused})
	if len(paused) != 1 {
		t.Fatalf("status filter: got %d, want 1", len(paused))
	}

	// Tag filter.
	tagged, _ := st.ListSessions(ListOptions{Tags: []string{"urgent"}})
	if len(tagged) != 1 || tagged[0].AgentID != "a2" {
		t.Fatalf("tag filter: %+v", tagged)
	}

	// Since filter.
	recent, _ := st.ListSessions(ListOptions{Since: time.Now().Add(-24 * time.Hour)})
	if len(recent) != 3 {
		t.Fatalf("since filter: got %d, want 3", len(recent))
	}

	// Limit/offset.
	limited, _ := st.ListSessions(ListOptions{Limit: 2})
	if len(limited) != 2 {
		t.Fatalf("limit: got %d, want 2", len(limited))
	}
	offset, _ := st.ListSessions(ListOptions{Offset: 3})
	if len(offset) != 1 || offset[0].ID != old.ID {
		t.Fatalf("offset: %+v", offset)
	}
}

func TestSessionEntryToolMetadata(t *testing.T) {
	e := NewEntry(RoleAssistant, "calling tool")
	e.ToolCallID = "tc_1_0"
	e.ToolName = "file_read"
	e.ToolInput = json.RawMessage(`{"path":"x.txt"}`)

	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back SessionEntry
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if back.ToolName != "file_read" || back.ToolCallID != "tc_1_0" || string(back.ToolInput) != `{"path":"x.txt"}` {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}
