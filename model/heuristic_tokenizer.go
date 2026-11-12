package model

import "unicode/utf8"

// HeuristicTokenizer estimates tokens at roughly four characters per token
// for ordinary text, counted in RUNES rather than bytes (ADR 0039 decision 3).
// It is the fallback a navi gets when it declares no tokenizer, and it is
// stdlib-only, so it lives beside the contract in model rather than in the
// implementation package.
//
// ADR 0039 calls this "defensible for a soft ceiling and wrong for a gate": it
// under-counts identifiers, code, and non-Latin text. Declaring a real
// tokenizer (model/tokenizer.TiktokenTokenizer) is how a navi closes that gap.
type HeuristicTokenizer struct{}

// heuristicCharsPerToken is the prose ratio the heuristic uses. Structured,
// JSON-shaped tool output compresses better and is counted separately by the
// message-level counter, not by CountText.
const heuristicCharsPerToken = 4

// CountText returns the estimated token count for text.
func (HeuristicTokenizer) CountText(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + heuristicCharsPerToken - 1) / heuristicCharsPerToken
}

// Compile-time check: the heuristic is a Tokenizer.
var _ Tokenizer = HeuristicTokenizer{}
