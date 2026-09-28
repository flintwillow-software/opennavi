package store

import (
	"fmt"
	"time"
)

// ApplyCompaction flags entries [0, cutIndex) as compacted and inserts
// a system-role summary entry at the cut point. The cut index must be
// tool-call safe (never split a tool result from its assistant call);
// an unsafe cut is rejected. Compacted entries are soft-deleted —
// flagged, never removed — with the original history preserved in the
// store for audit and search. The cut-point selection
// strategy itself (SafeCompactionCut) stays internal to the runtime.
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
