// Package context implements context window management: token budgeting,
// compaction trigger logic, and LLM-based conversation summarization.
// It is agent-agnostic — the agent runtime (internal/agent) adapts its
// own types onto these primitives.
package context

// ContextBudget governs when compaction triggers.
type ContextBudget struct {
	// MaxTokens is the hard token limit for the context window.
	MaxTokens int
	// WarningThreshold is the fraction of MaxTokens at which warnings
	// fire (default 0.8).
	WarningThreshold float64
	// CompactionTrigger is the fraction of MaxTokens at which
	// compaction fires (default 0.9).
	CompactionTrigger float64
}

// DefaultBudget returns a budget with the spec defaults for the given
// hard limit.
func DefaultBudget(maxTokens int) ContextBudget {
	return ContextBudget{
		MaxTokens:         maxTokens,
		WarningThreshold:  0.8,
		CompactionTrigger: 0.9,
	}
}

// ShouldCompact reports whether compaction should fire for the current
// token usage and message count: at or above the
// compaction trigger, or above the warning threshold with a large
// history (more than ~25 turns / 50 messages).
func ShouldCompact(budget ContextBudget, currentTokens, messageCount int) bool {
	if budget.MaxTokens <= 0 {
		return false
	}
	if currentTokens >= int(float64(budget.MaxTokens)*budget.CompactionTrigger) {
		return true
	}
	if currentTokens >= int(float64(budget.MaxTokens)*budget.WarningThreshold) && messageCount > 50 {
		return true
	}
	return false
}
