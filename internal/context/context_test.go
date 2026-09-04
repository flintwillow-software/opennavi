package context

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultBudget(t *testing.T) {
	b := DefaultBudget(1000)
	if b.MaxTokens != 1000 || b.WarningThreshold != 0.8 || b.CompactionTrigger != 0.9 {
		t.Fatalf("budget = %+v", b)
	}
}

func TestShouldCompactMatrix(t *testing.T) {
	b := DefaultBudget(1000) // trigger at 900, warn at 800

	cases := []struct {
		tokens, msgs int
		want         bool
	}{
		{100, 10, false},
		{799, 10, false},
		{899, 10, false}, // above warning but few messages: no compact
		{900, 10, true},  // at trigger: compact
		{850, 60, true},  // at warning (>=) with many messages: compact
		{800, 60, true},  // spec uses >= on the warning branch too
		{0, 10, false},
	}
	for _, c := range cases {
		if got := ShouldCompact(b, c.tokens, c.msgs); got != c.want {
			t.Errorf("ShouldCompact(tokens=%d, msgs=%d) = %v, want %v", c.tokens, c.msgs, got, c.want)
		}
	}

	// No budget: never compact.
	if ShouldCompact(ContextBudget{}, 100000, 1000) {
		t.Error("zero MaxTokens budget should never compact")
	}
}

func TestEstimateCounter(t *testing.T) {
	tc := EstimateCounter{}
	if got := tc.CountTokens(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := tc.CountTokens("abcd"); got != 1 {
		t.Fatalf("4 chars = %d, want 1", got)
	}
	if got := tc.CountTokens(strings.Repeat("a", 401)); got != 101 {
		t.Fatalf("401 chars = %d, want 101 (ceiling)", got)
	}
}

type cannedSummarizer struct {
	response string
	err      error
	got      string
}

func (c *cannedSummarizer) Summarize(_ context.Context, prompt string) (string, error) {
	c.got = prompt
	return c.response, c.err
}

func TestCompactorBuildsPromptAndTrims(t *testing.T) {
	cs := &cannedSummarizer{response: "  the summary  "}
	c := &Compactor{Summarizer: cs}
	transcript := []TranscriptMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	summary, err := c.Compact(context.Background(), transcript)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "the summary" {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(cs.got, "Key decisions") || !strings.Contains(cs.got, "user: hello") || !strings.Contains(cs.got, "assistant: hi there") {
		t.Fatalf("prompt missing spec sections: %q", cs.got)
	}
}

func TestCompactorEmptyTranscript(t *testing.T) {
	c := &Compactor{Summarizer: &cannedSummarizer{}}
	summary, err := c.Compact(context.Background(), nil)
	if err != nil || summary != "" {
		t.Fatalf("empty transcript: %q, %v", summary, err)
	}
}

func TestCompactorNilSummarizer(t *testing.T) {
	c := &Compactor{}
	if _, err := c.Compact(context.Background(), []TranscriptMessage{{Role: "user"}}); err == nil {
		t.Fatal("nil summarizer should error")
	}
}

func TestCompactorSummarizerError(t *testing.T) {
	cs := &cannedSummarizer{err: context.DeadlineExceeded}
	c := &Compactor{Summarizer: cs}
	if _, err := c.Compact(context.Background(), []TranscriptMessage{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("summarizer error should propagate")
	}
}
