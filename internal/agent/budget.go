package agent

// defaultBudgetWarning is the fraction of a thought's context budget at
// which the deferral notice fires.
const defaultBudgetWarning = 0.8

// budgetNotice is injected once when a run approaches its context budget.
// There is no compaction (ADR 0013): the way to keep working is to defer into
// a follow-up thought, carrying the handoff in short-term memory.
const budgetNotice = "System notice: this thought is approaching its context budget. " +
	"Finish a coherent unit of work now. Write what the next thought needs into " +
	"short-term memory with stm_write, and queue follow-up thoughts with " +
	"thoughts_create, declaring the skills each should load. Do not try to hold " +
	"the whole task in this thought."

func resolveBudgetWarning(f float64) float64 {
	if f <= 0 {
		return defaultBudgetWarning
	}
	return f
}

// estimateMessagesTokens sizes the transcript with the navi's declared token
// counter when one is present, so the deferral notice, assembly truncation,
// and the memory budgets all agree on how full a thought is (ADR 0039
// decision 3). Without a counter it falls back to the ~4-characters-per-token
// heuristic that shipped before.
func estimateMessagesTokens(messages []AgentMessage, count func(string) int) int {
	total := 0
	for _, m := range messages {
		if count != nil {
			total += count(m.Content)
			continue
		}
		total += (len(m.Content) + 3) / 4
	}
	return total
}
