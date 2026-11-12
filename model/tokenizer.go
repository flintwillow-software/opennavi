package model

// Tokenizer counts tokens for a piece of text, so the runtime can size
// context consistently (ADR 0039 decision 3).
//
// It is a **contract**, the way Embedder and Provider are: a navi author
// supplies one at construction (`navi.WithTokenizer`), and an author cannot
// supply a type from an internal package — which is why this lives here
// rather than beside its implementations. The package stays stdlib-only
// (ADR 0004), so the interface drags nothing into a component's dependency
// closure.
//
// The runtime asks the SAME counter in three places — the short-term memory
// budgets, assembly truncation, and the agent's deferral notice — because all
// three must agree on how full a thought is. That is the whole point of the
// interface being one method wide.
//
// A navi that declares no tokenizer gets the heuristic, which is what shipped
// before this interface existed and is a supported configuration rather than a
// degraded one.
type Tokenizer interface {
	// CountText returns the token count for plain text.
	CountText(text string) int
}
