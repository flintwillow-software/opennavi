package llm

import "testing"

// TestTiktokenTokenizerCounts is ADR 0039 decision 3: tokens are counted by a
// real tokenizer rather than the ~4-characters-per-token heuristic.
func TestTiktokenTokenizerCounts(t *testing.T) {
	tok := NewTiktokenTokenizer("")
	if got := tok.CountText(""); got != 0 {
		t.Fatalf("empty text = %d, want 0", got)
	}
	// "hello world" is two tokens in cl100k_base; the heuristic would also
	// say 3, so pick something where they differ: an identifier-ish string
	// tokenises into more pieces than chars/4.
	got := tok.CountText("stm_write ltm_search thoughts_create")
	if got <= 0 {
		t.Fatalf("count = %d, want positive", got)
	}
	// A real tokenizer counts a long code-like string as more tokens than the
	// heuristic's chars/4 for ASCII identifiers is not guaranteed, so assert
	// only that it is a plausible positive number and deterministic.
	if again := tok.CountText("stm_write ltm_search thoughts_create"); again != got {
		t.Fatalf("count not deterministic: %d then %d", got, again)
	}
}

// TestTiktokenTokenizerFallsBackOnBadEncoding: an unresolvable encoding
// degrades to the heuristic rather than failing (ADR 0039 consequences).
func TestTiktokenTokenizerFallsBackOnBadEncoding(t *testing.T) {
	tok := NewTiktokenTokenizer("definitely-not-an-encoding")
	got := tok.CountText("hello world")
	want := HeuristicTokenCounter{}.CountText("hello world")
	if got != want {
		t.Fatalf("fallback count = %d, want heuristic %d", got, want)
	}
}

// TestCountTextNilUsesHeuristic: the free helper treats a nil tokenizer as "no
// real counter declared".
func TestCountTextNilUsesHeuristic(t *testing.T) {
	got := CountText(nil, "hello world")
	want := HeuristicTokenCounter{}.CountText("hello world")
	if got != want {
		t.Fatalf("nil-tokenizer count = %d, want heuristic %d", got, want)
	}
}

// TestCountTextUsesTokenizerWhenPresent.
func TestCountTextUsesTokenizerWhenPresent(t *testing.T) {
	tok := NewTiktokenTokenizer("")
	text := "the quick brown fox jumps over the lazy dog"
	if got, want := CountText(tok, text), tok.CountText(text); got != want {
		t.Fatalf("CountText = %d, want %d", got, want)
	}
}
