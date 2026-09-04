package context

import (
	"context"
	"fmt"
	"strings"
)

// TranscriptMessage is one message of the conversation transcript to
// summarize. Role is the LLM-facing string ("user", "assistant",
// "tool", "system").
type TranscriptMessage struct {
	Role    string
	Content string
}

// Summarizer produces a summary for a compaction prompt. The agent
// runtime adapts its LLM provider onto this; tests use canned
// implementations.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// BuildCompactionPrompt builds the summarization prompt for the given
// transcript (spec §3.3): preserve key decisions and outcomes, important
// context and constraints, and action items or next steps.
func BuildCompactionPrompt(transcript []TranscriptMessage) string {
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation, preserving:\n")
	sb.WriteString("- Key decisions and outcomes\n")
	sb.WriteString("- Important context and constraints\n")
	sb.WriteString("- Action items or next steps\n")
	sb.WriteString("\nThe summary should be concise but comprehensive enough to continue the conversation.\n\n")
	for _, m := range transcript {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// Compactor summarizes conversation transcripts via a Summarizer.
type Compactor struct {
	Summarizer Summarizer
}

// Compact summarizes the transcript and returns the summary text.
func (c *Compactor) Compact(ctx context.Context, transcript []TranscriptMessage) (string, error) {
	if len(transcript) == 0 {
		return "", nil
	}
	if c == nil || c.Summarizer == nil {
		return "", fmt.Errorf("context: compactor has no summarizer")
	}
	prompt := BuildCompactionPrompt(transcript)
	summary, err := c.Summarizer.Summarize(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("context: compaction failed: %w", err)
	}
	return strings.TrimSpace(summary), nil
}
