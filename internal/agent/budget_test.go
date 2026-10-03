package agent

import (
	"strings"
	"testing"
)

func textTurn() [][]Event {
	return [][]Event{{
		{Type: EventMessageStart},
		{Type: EventContentDelta, Content: "ok"},
		{Type: EventMessageEnd},
		{Type: EventStop},
	}}
}

// TestBudgetNoticeInjectedOnce pins the deferral trigger: when a run
// approaches the thought's context budget, the loop tells the agent once
// (ADR 0013), because there is no compaction.
func TestBudgetNoticeInjectedOnce(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model", ContextBudget: 20, BudgetWarning: 0.5})
	a.Provider = &mockProvider{responses: textTurn()}

	if err := a.Prompt(strings.Repeat("word ", 200), nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	count := 0
	for _, m := range a.State().Messages {
		if strings.Contains(m.Content, "approaching its context budget") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("deferral notice injected %d times, want exactly 1", count)
	}
}

func TestBudgetNoticeDisabledWithoutABudget(t *testing.T) {
	a := NewAgent(AgentConfig{Provider: "mock", Model: "test-model"})
	a.Provider = &mockProvider{responses: textTurn()}

	if err := a.Prompt(strings.Repeat("word ", 200), nil); err != nil {
		t.Fatal(err)
	}
	a.WaitForIdle()

	for _, m := range a.State().Messages {
		if strings.Contains(m.Content, "approaching its context budget") {
			t.Fatal("a run with no budget must not inject a deferral notice")
		}
	}
}
