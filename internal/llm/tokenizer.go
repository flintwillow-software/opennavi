package llm

import (
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// Tokenizer is the narrow interface ADR 0039 decision 3 puts in front of
// token counting. It is deliberately one method wide: the runtime needs a
// number for the same text in three places — the short-term memory budgets,
// assembly truncation, and the agent's deferral notice — and the point of
// the interface is that all three ask the SAME counter, so they agree.
//
// A navi may supply a provider-specific implementation (an API that reports
// exact usage, a model-specific encoding); when it supplies none the
// runtime falls back to HeuristicTokenCounter, which is what shipped before
// this interface existed.
type Tokenizer interface {
	// CountText returns the token count for plain text.
	CountText(text string) int
}

// TiktokenTokenizer counts tokens with a real BPE tokenizer (tiktoken-go),
// which is the first non-stdlib dependency in this area (ADR 0039
// decision 3). It replaces the ~4-characters-per-token heuristic, which the
// ADR calls "defensible for a soft ceiling and wrong for a gate": it
// under-counts identifiers, code, and non-Latin text — exactly the content
// a knowledge note holds — so a heuristic-gated tier would drift over
// budget.
//
// The encoding is resolved lazily and once: constructing a tokenizer loads
// a BPE table, and a navi that never writes memory should not pay for it.
// When the encoding cannot be resolved the tokenizer degrades to the
// heuristic rather than failing, because a navi that cannot count tokens
// must still run (ADR 0039 consequences: "it must be optional enough that a
// navi without a tokenizer still runs").
type TiktokenTokenizer struct {
	// encoding is the tiktoken encoding name (e.g. "cl100k_base"). Empty
	// selects DefaultEncoding.
	encoding string

	once     sync.Once
	enc      *tiktoken.Tiktoken
	failed   bool
	fallback HeuristicTokenCounter
}

// DefaultEncoding is the tiktoken encoding used when a navi does not name
// one. cl100k_base is the encoding for the GPT-3.5/4 family and is the
// closest widely-available approximation for the OpenAI-compatible models a
// navi typically runs on; a navi on a different family should name its own.
const DefaultEncoding = "cl100k_base"

// NewTiktokenTokenizer returns a tokenizer for the named encoding, or the
// default when the name is empty. It does not load the encoding table until
// the first count (see TiktokenTokenizer).
func NewTiktokenTokenizer(encoding string) *TiktokenTokenizer {
	if strings.TrimSpace(encoding) == "" {
		encoding = DefaultEncoding
	}
	return &TiktokenTokenizer{encoding: encoding}
}

// CountText returns the token count for text. It never fails: when the
// encoding is unavailable the heuristic answers instead, so a caller always
// gets a number (ADR 0039 decision 3, "the heuristic remains the fallback
// when none is available").
func (t *TiktokenTokenizer) CountText(text string) int {
	if text == "" {
		return 0
	}
	t.once.Do(func() {
		enc, err := tiktoken.GetEncoding(t.encoding)
		if err != nil {
			t.failed = true
			return
		}
		t.enc = enc
	})
	if t.failed || t.enc == nil {
		return t.fallback.CountText(text)
	}
	return len(t.enc.Encode(text, nil, nil))
}

// CountText is the runtime's one way to size a string in tokens. It is a
// free function so the three call sites (memory budgets, assembly
// truncation, and the agent's deferral notice) share one counter: a nil
// tokenizer means "no real counter declared", which is the heuristic
// (ADR 0039 decision 3).
func CountText(tok Tokenizer, text string) int {
	if tok == nil {
		return HeuristicTokenCounter{}.CountText(text)
	}
	return tok.CountText(text)
}
