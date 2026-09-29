package session

import (
	"strings"
	"testing"
)

func TestSafeCompactionCutRespectsToolIntegrity(t *testing.T) {
	entries := []SessionEntry{
		{Role: RoleUser, Message: "u1"},
		{Role: RoleAssistant, ToolName: "f", ToolCallID: "tc_1_0"},
		{Role: RoleTool, Message: "r1", ToolCallID: "tc_1_0"},
		{Role: RoleUser, Message: "u2"},
		{Role: RoleAssistant, ToolName: "g", ToolCallID: "tc_2_0"},
		{Role: RoleTool, Message: "r2", ToolCallID: "tc_2_0"},
	}
	// Boundary 5 lands on a tool result: unsafe → walk back to 4.
	if got := SafeCompactionCut(entries, 5); got != 4 {
		t.Fatalf("SafeCompactionCut(5) = %d, want 4", got)
	}
	// Boundary 2 lands on a tool result: unsafe → 1.
	if got := SafeCompactionCut(entries, 2); got != 1 {
		t.Fatalf("SafeCompactionCut(2) = %d, want 1", got)
	}
	// Safe boundaries pass through unchanged.
	for _, b := range []int{3, 4, 6} {
		if got := SafeCompactionCut(entries, b); got != b {
			t.Fatalf("SafeCompactionCut(%d) = %d, want unchanged", b, got)
		}
	}
}

func TestApplyCompactionSoftDeletes(t *testing.T) {
	s := NewSession("a", "")
	s.AppendEntry(NewEntry(RoleUser, "old question"))
	s.AppendEntry(NewEntry(RoleAssistant, "old answer"))
	s.AppendEntry(NewEntry(RoleUser, "recent question"))
	s.AppendEntry(NewEntry(RoleAssistant, "recent answer"))

	if err := s.ApplyCompaction("summary of the old stuff", 2); err != nil {
		t.Fatal(err)
	}

	// Entries are never removed (soft delete).
	if len(s.Entries) != 5 {
		t.Fatalf("entries = %d, want 5 (2 flagged + summary + 2 kept)", len(s.Entries))
	}
	if !s.Entries[0].IsCompacted || !s.Entries[1].IsCompacted {
		t.Fatal("old entries not flagged compacted")
	}
	summary := s.Entries[2]
	if summary.Role != RoleSystem || summary.Message != "summary of the old stuff" {
		t.Fatalf("summary entry = %+v", summary)
	}
	if !summary.IsCompacted {
		t.Fatal("summary entry should carry is_compacted=1")
	}
	if s.Entries[3].IsCompacted || s.Entries[4].IsCompacted {
		t.Fatal("kept entries must not be flagged")
	}
	if s.Current != 2 {
		t.Fatalf("Current = %d, want 2 (at the summary)", s.Current)
	}
}

func TestApplyCompactionRejectsUnsafeCut(t *testing.T) {
	s := NewSession("a", "")
	s.AppendEntry(NewEntry(RoleUser, "q"))
	s.AppendEntry(NewEntry(RoleAssistant, "calling"))
	s.AppendEntry(NewEntry(RoleTool, "result"))

	// Cut at 3 splits... actually 3 is the end (safe). Cut at 2 lands on
	// the tool result — ApplyCompaction must reject it outright.
	if err := s.ApplyCompaction("summary", 2); err == nil {
		t.Fatal("unsafe cut (on tool result) should be rejected")
	} else if !strings.Contains(err.Error(), "tool-call") {
		t.Fatalf("error = %v", err)
	}
	// Entries unchanged after rejection.
	if len(s.Entries) != 3 || s.Entries[0].IsCompacted {
		t.Fatalf("entries mutated after failed compaction: %+v", s.Entries)
	}
}

func TestApplyCompactionRangeCheck(t *testing.T) {
	s := NewSession("a", "")
	s.AppendEntry(NewEntry(RoleUser, "q"))
	if err := s.ApplyCompaction("summary", 5); err == nil {
		t.Fatal("out-of-range cut should fail")
	}
	if err := s.ApplyCompaction("summary", -1); err == nil {
		t.Fatal("negative cut should fail")
	}
}

func TestRestoreSkipsCompactedEntriesKeepsSummary(t *testing.T) {
	s := NewSession("a", "")
	s.AppendEntry(NewEntry(RoleUser, "old question"))
	s.AppendEntry(NewEntry(RoleAssistant, "old answer"))
	if err := s.ApplyCompaction("summary of old", 2); err != nil {
		t.Fatal(err)
	}
	s.AppendEntry(NewEntry(RoleUser, "new question"))

	st := AgentStateFromEntries(s.GetPathToRoot())
	// Expected messages: system summary + new question user message.
	if len(st.Messages) != 2 {
		t.Fatalf("messages = %+v, want [system summary, user]", st.Messages)
	}
	if st.Messages[0].Role.String() != "system" || st.Messages[0].Content != "summary of old" {
		t.Fatalf("msg 0 = %+v", st.Messages[0])
	}
	if st.Messages[1].Content != "new question" {
		t.Fatalf("msg 1 = %+v", st.Messages[1])
	}
}
