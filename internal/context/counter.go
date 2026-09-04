package context

// TokenCounter counts tokens for a piece of text. Exact counting is a
// provider/model concern (each model family has its own tokenizer);
// implementations plug in behind this interface.
type TokenCounter interface {
	CountTokens(text string) int
}

// EstimateCounter is the spec's rough heuristic counter: ~4 characters
// per token for English text. Good enough for compaction triggering;
// never use it for billing.
type EstimateCounter struct{}

func (EstimateCounter) CountTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// CountTranscript sums the counter over a transcript.
func CountTranscript(tc TokenCounter, transcript []TranscriptMessage) int {
	total := 0
	for _, m := range transcript {
		total += tc.CountTokens(m.Content)
	}
	return total
}
