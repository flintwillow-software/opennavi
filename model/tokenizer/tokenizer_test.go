package tokenizer

import (
	"testing"

	"github.com/flintwillow-software/opennavi/model"
)

// TestTiktokenCountsText pins the real counter: a non-empty string counts more
// than zero tokens, empty is zero, and the value is stable across calls (the
// encoding loads once).
func TestTiktokenCountsText(t *testing.T) {
	tok := NewTiktoken("")
	if got := tok.CountText(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	first := tok.CountText("the quick brown fox jumps over the lazy dog")
	if first <= 0 {
		t.Fatalf("count = %d, want > 0", first)
	}
	if second := tok.CountText("the quick brown fox jumps over the lazy dog"); second != first {
		t.Fatalf("count not stable: %d then %d", first, second)
	}
}

// TestTiktokenDegradesToHeuristicWhenEncodingUnknown pins ADR 0039's
// consequence: a navi that cannot load a BPE table must still get a number, so
// an unknown encoding answers with the heuristic rather than failing.
func TestTiktokenDegradesToHeuristicWhenEncodingUnknown(t *testing.T) {
	tok := NewTiktoken("definitely-not-a-real-encoding")
	text := "the quick brown fox jumps over the lazy dog"
	if got, want := tok.CountText(text), (model.HeuristicTokenizer{}).CountText(text); got != want {
		t.Fatalf("count = %d, want heuristic %d", got, want)
	}
}

// TestTiktokenSatisfiesContract is the compile-time check made
// runtime-visible.
func TestTiktokenSatisfiesContract(t *testing.T) {
	var _ model.Tokenizer = (*TiktokenTokenizer)(nil)
}
