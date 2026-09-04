package agent

import (
	"context"
	"fmt"
	"strings"

	wincontext "github.com/flintwillow-software/opennavi/internal/context"
)

// Context window compaction (spec 03-session-context/
// 02-context-window-management.md §3.3): when the conversation
// approaches the token budget, older messages are replaced with an LLM
// summary. The compaction cut point preserves tool-call integrity —
// an assistant message carrying tool calls is never separated from its
// tool results.

// defaultCompactionBudget is used when MaxTokens is unset.
const defaultCompactionBudget = 8192

// compactionConfig normalizes the user-facing CompactionConfig.
type compactionSettings struct {
	budget    wincontext.ContextBudget
	keepTurns int
}

func resolveCompaction(cfg *CompactionConfig) *compactionSettings {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultCompactionBudget
	}
	budget := wincontext.DefaultBudget(maxTokens)
	if cfg.WarningThreshold > 0 {
		budget.WarningThreshold = cfg.WarningThreshold
	}
	if cfg.CompactionTrigger > 0 {
		budget.CompactionTrigger = cfg.CompactionTrigger
	}
	keepTurns := cfg.KeepTurns
	if keepTurns <= 0 {
		keepTurns = 5
	}
	return &compactionSettings{budget: budget, keepTurns: keepTurns}
}

// safeCutIndex returns the largest index i <= boundary such that
// splitting the message list at i keeps both halves tool-call
// coherent: no tool result in the suffix without its assistant call in
// the suffix too. Cutting between an assistant-with-tool-calls and its
// results produces malformed LLM context (dangling tool_calls or
// orphaned results), so we scan back to a safe point — the start of a
// user/assistant turn.
func safeCutIndex(messages []AgentMessage, boundary int) int {
	if boundary > len(messages) {
		boundary = len(messages)
	}
	for i := boundary; i > 0; i-- {
		if isSafeCut(messages, i) {
			return i
		}
	}
	return 0
}

func isSafeCut(messages []AgentMessage, i int) bool {
	if i <= 0 || i >= len(messages) {
		return i == len(messages) // cut at the very end is safe
	}
	// A tool result must never start the suffix: its assistant call
	// would be left behind.
	if messages[i].Role == RoleTool {
		return false
	}
	// If the previous message is an assistant with unanswered tool
	// calls, its results belong to it — but results FOLLOW the call in
	// the list, so the previous message's calls are only a problem if
	// they have no results at all before the cut. The scan below
	// handles the common case; being conservative here is cheap.
	return true
}

// maybeCompact runs compaction when the current state crosses the
// budget thresholds, replacing the older portion of the conversation
// with a system-role summary. Called between turns in the run loop.
func (a *Agent) maybeCompact(ctx context.Context) error {
	settings := a.compaction
	if settings == nil {
		return nil
	}

	transcript := transcriptFromMessages(a.State().Messages)
	currentTokens := wincontext.CountTranscript(wincontext.EstimateCounter{}, transcript)
	if !wincontext.ShouldCompact(settings.budget, currentTokens, len(transcript)) {
		return nil
	}

	// Keep the last ~KeepTurns turns; scan back to a tool-safe cut.
	keep := settings.keepTurns * 2
	boundary := len(transcript) - keep
	if boundary <= 0 {
		return nil // nothing old enough to compact
	}
	cut := safeCutIndex(a.State().Messages, boundary)
	if cut <= 0 {
		return nil
	}

	toCompact := transcript[:cut]
	compactor := &wincontext.Compactor{Summarizer: &providerSummarizer{a: a}}
	summary, err := compactor.Compact(ctx, toCompact)
	if err != nil {
		return err
	}

	// Replace [0, cut) with the summary; compacted history is not kept
	// in the live state (the session store retains flagged history).
	summaryMsg := AgentMessage{Role: RoleSystem, Content: summary}
	kept := append([]AgentMessage(nil), a.State().Messages[cut:]...)
	next := a.State().Clone()
	next.Messages = append([]AgentMessage{summaryMsg}, kept...)
	a.setState(next)
	return nil
}

// transcriptFromMessages maps agent messages onto the context package's
// transcript shape.
func transcriptFromMessages(messages []AgentMessage) []wincontext.TranscriptMessage {
	out := make([]wincontext.TranscriptMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, wincontext.TranscriptMessage{Role: m.Role.String(), Content: m.Content})
	}
	return out
}

// providerSummarizer adapts the agent's LLM provider onto the context
// package's Summarizer interface (spec §3.3 callLLMForSummary).
type providerSummarizer struct {
	a *Agent
}

func (p *providerSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	a := p.a
	messages := []Message{{Role: "user", Content: []Content{TextContent(prompt)}}}
	events, err := a.Provider.Stream(ctx, messages, nil, a.Config)
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}
	var summary strings.Builder
	for ev := range events {
		switch ev.Type {
		case EventContentDelta:
			summary.WriteString(ev.Content)
		case EventStop:
			return summary.String(), nil
		case EventError:
			return "", fmt.Errorf("summarize: %s", ev.Error)
		}
	}
	return summary.String(), nil
}
