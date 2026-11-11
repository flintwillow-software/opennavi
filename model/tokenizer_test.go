package model

import "testing"

// TestHeuristicTokenizerCountsRunes pins the fallback's contract: the count is
// rune-based (ADR 0039 decision 3 — a byte count overestimates multibyte text),
// and empty text is zero.
func TestHeuristicTokenizerCountsRunes(t *testing.T) {
	tok := HeuristicTokenizer{}
	if got := tok.CountText(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	// Four ASCII characters is one token at the prose ratio.
	if got := tok.CountText("abcd"); got != 1 {
		t.Fatalf("abcd = %d, want 1", got)
	}
	// Four CJK runes is one token, not three-plus (byte length would be 12).
	if got := tok.CountText("日本語版"); got != 1 {
		t.Fatalf("日本語版 = %d, want 1", got)
	}
}

// TestHeuristicTokenizerSatisfiesContract is the compile-time check made
// runtime-visible, so a change to the interface fails here as well.
func TestHeuristicTokenizerSatisfiesContract(t *testing.T) {
	var _ Tokenizer = HeuristicTokenizer{}
}
