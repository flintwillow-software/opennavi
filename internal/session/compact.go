package session

import ()

// Context compaction: compacted entries are soft-deleted — flagged IsCompacted and
// never removed — with a system-role summary entry inserted at the cut
// point. Original history stays in the store for audit and search.
// Session state mutation (ApplyCompaction) is public on the store data
// model; the cut-point selection strategy stays internal here.

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
