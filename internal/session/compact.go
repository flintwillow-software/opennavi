package session

import (
	"fmt"
	"time"
)

// Context compaction (spec 03-session-context/01-session-management.md
// §06): compacted entries are soft-deleted — flagged IsCompacted and
// never removed — with a system-role summary entry inserted at the cut
// point. Original history stays in the store for audit and search.

// SafeCompactionCut returns the largest index i <= boundary such that
// splitting the entry list at i keeps tool-call coherence: the suffix
// never starts with a tool result whose assistant call was cut away.
// Mirrors the agent-side rule (internal/agent SafeCutIndex).
func SafeCompactionCut(entries []SessionEntry, boundary int) int {
	if boundary > len(entries) {
		boundary = len(entries)
	}
	safe := func(i int) bool {
		if i <= 0 || i >= len(entries) {
			return i == len(entries)
		}
		return entries[i].Role != RoleTool
	}
	for i := boundary; i > 0; i-- {
		if safe(i) {
			return i
		}
	}
	return 0
}

// ApplyCompaction flags entries [0, cutIndex) as compacted and inserts
// a system-role summary entry at the cut point. The cut index must be
// tool-call safe (see SafeCompactionCut); an unsafe cut is rejected.
func (s *Session) ApplyCompaction(summary string, cutIndex int) error {
	if cutIndex < 0 || cutIndex > len(s.Entries) {
		return fmt.Errorf("session %s: compaction cut %d out of range", s.ID, cutIndex)
	}
	if cutIndex < len(s.Entries) && s.Entries[cutIndex].Role == RoleTool {
		return fmt.Errorf("session %s: compaction cut %d splits a tool-call sequence", s.ID, cutIndex)
	}
	for i := 0; i < cutIndex; i++ {
		s.Entries[i].IsCompacted = true
	}
	summaryEntry := NewEntry(RoleSystem, summary)
	summaryEntry.IsCompacted = true // summary replaces compacted originals
	entries := make([]SessionEntry, 0, len(s.Entries)+1)
	entries = append(entries, s.Entries[:cutIndex]...)
	entries = append(entries, summaryEntry)
	entries = append(entries, s.Entries[cutIndex:]...)
	s.Entries = entries
	s.Current = cutIndex
	s.UpdatedAt = time.Now().UTC()
	return nil
}
