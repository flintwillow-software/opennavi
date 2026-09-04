package session

import (
	"errors"
	"testing"
	"time"
)

func TestManagerCreateAndLoad(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s, err := m.CreateSession("agent-1", "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusActive || s.AgentID != "agent-1" || s.ID == "" {
		t.Fatalf("created session = %+v", s)
	}

	loaded, err := m.LoadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != s.ID || loaded.CreatedAt.IsZero() {
		t.Fatalf("loaded = %+v", loaded)
	}

	if _, err := m.LoadSession("missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing load error = %v", err)
	}
}

func TestManagerLifecycleTransitions(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")

	// Pause from active: ok.
	p, err := m.PauseSession(s.ID)
	if err != nil || p.Status != StatusPaused {
		t.Fatalf("pause: %v, %q", err, p.Status)
	}
	// Pause again: invalid (already paused).
	if _, err := m.PauseSession(s.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("double pause error = %v", err)
	}
	// Resume: ok.
	r, err := m.ResumeSession(s.ID)
	if err != nil || r.Status != StatusActive {
		t.Fatalf("resume: %v, %q", err, r.Status)
	}
	// Resume again: invalid (already active).
	if _, err := m.ResumeSession(s.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("double resume error = %v", err)
	}
	// Close: ok, from any non-closed status.
	c, err := m.CloseSession(s.ID)
	if err != nil || c.Status != StatusClosed {
		t.Fatalf("close: %v, %q", err, c.Status)
	}
	// Closed is terminal for every transition.
	if _, err := m.PauseSession(s.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatal("pause after close should fail")
	}
	if _, err := m.ResumeSession(s.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatal("resume after close should fail")
	}
	if _, err := m.CloseSession(s.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatal("close after close should fail")
	}
}

func TestManagerForkSession(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	parent, _ := m.CreateSession("a", "/w")
	parent.Name = "original"
	parent.Profile = "p1"

	for _, msg := range []string{"one", "two", "three"} {
		parent.AppendEntry(NewEntry(RoleUser, msg))
	}
	if err := m.SaveSession(parent); err != nil {
		t.Fatal(err)
	}

	// Fork at Current (entry 3).
	fork, err := m.ForkSession(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ParentID != parent.ID {
		t.Fatalf("fork parent = %q", fork.ParentID)
	}
	if fork.BranchID == "" || fork.BranchID == parent.BranchID {
		t.Fatalf("fork branch = %q", fork.BranchID)
	}
	if len(fork.Entries) != 3 || fork.Entries[2].Message != "three" {
		t.Fatalf("fork entries = %+v", fork.Entries)
	}
	if fork.Current != 2 {
		t.Fatalf("fork Current = %d", fork.Current)
	}
	if fork.Name != "original" || fork.Profile != "p1" {
		t.Fatalf("fork metadata not inherited: %+v", fork)
	}

	// Move parent's Current back and fork again: shorter copy.
	if err := parent.SetCurrent(0); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSession(parent); err != nil {
		t.Fatal(err)
	}
	fork2, err := m.ForkSession(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fork2.Entries) != 1 {
		t.Fatalf("fork2 entries = %d, want 1", len(fork2.Entries))
	}
	if fork2.ID == fork.ID {
		t.Fatal("two forks share an ID")
	}

	// Forking a session with no entries fails.
	empty, _ := m.CreateSession("a", "")
	if _, err := m.ForkSession(empty.ID); err == nil {
		t.Fatal("fork of empty session should fail")
	}
}

func TestManagerDeleteAndList(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s1, _ := m.CreateSession("a1", "")
	time.Sleep(2 * time.Millisecond)
	s2, _ := m.CreateSession("a2", "")

	list, err := m.ListSessions(ListOptions{})
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v, %d", err, len(list))
	}
	if list[0].ID != s2.ID {
		t.Fatalf("newest-first ordering violated: %v before %v", list[0].ID, list[1].ID)
	}

	if err := m.DeleteSession(s1.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = m.ListSessions(ListOptions{})
	if len(list) != 1 || list[0].ID != s2.ID {
		t.Fatalf("after delete: %+v", list)
	}
	if err := m.DeleteSession(s1.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("double delete error = %v", err)
	}
}

func TestManagerTagSession(t *testing.T) {
	m := NewSessionManager(NewMemoryStore())
	s, _ := m.CreateSession("a", "")

	if _, err := m.TagSession(s.ID, "work"); err != nil {
		t.Fatal(err)
	}
	// Duplicate tag is a no-op, not an error or a second entry.
	if _, err := m.TagSession(s.ID, "work"); err != nil {
		t.Fatal(err)
	}
	loaded, _ := m.LoadSession(s.ID)
	if len(loaded.Tags) != 1 || loaded.Tags[0] != "work" {
		t.Fatalf("tags = %v", loaded.Tags)
	}

	tagged, _ := m.ListSessions(ListOptions{Tags: []string{"work"}})
	if len(tagged) != 1 {
		t.Fatalf("tagged list = %d", len(tagged))
	}
}

func TestManagerNilStorePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSessionManager(nil) should panic")
		}
	}()
	NewSessionManager(nil)
}
