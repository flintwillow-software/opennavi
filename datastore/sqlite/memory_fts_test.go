package sqlite_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/sqlite"
)

// openMem returns a store on its own database file.
func openMem(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "navi.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// remember is a terse Remember for tests.
func remember(t *testing.T, store *sqlite.Store, scope datastore.Scope, content string) datastore.MemoryRecord {
	t.Helper()
	rec, err := store.Memory().Remember(datastore.MemoryRecord{Scope: scope, Content: content})
	if err != nil {
		t.Fatalf("Remember(%q): %v", content, err)
	}
	return rec
}

// search runs a content query and returns the matched contents.
func search(t *testing.T, store *sqlite.Store, query string) []string {
	t.Helper()
	got, err := store.Memory().Recall(datastore.RecallQuery{Query: query})
	if err != nil {
		t.Fatalf("Recall(%q): %v", query, err)
	}
	out := make([]string, 0, len(got))
	for _, r := range got {
		out = append(out, r.Content)
	}
	return out
}

// TestRecallUsesTheFTSIndex is ADR 0040 decision 3: keyword search is an index
// lookup, not an in-Go substring scan.
//
// The observable proof that the index is in use is the TOKEN matching it
// implies — a full scan can only ever do substrings. The behaviour change is
// asserted directly in the two tests below; this one pins the index is
// actually consulted by exercising a multi-term AND, which is FTS5's own
// semantics and has no substring equivalent.
func TestRecallUsesTheFTSIndex(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the deploy script pins the upstream provider")
	remember(t, store, datastore.ScopeMemories, "the deploy script rolls back on failure")
	remember(t, store, datastore.ScopeMemories, "an unrelated note about providers")

	// Two terms, both must appear in the same entry (FTS5's implicit AND).
	got := search(t, store, "deploy provider")
	if len(got) != 1 {
		t.Fatalf("multi-term search = %v, want exactly the entry containing both terms", got)
	}
	if !strings.Contains(got[0], "upstream provider") {
		t.Fatalf("matched the wrong entry: %q", got[0])
	}
}

// TestRecallMatchesTokenPrefixes is the documented behaviour change (ADR 0040
// decision 3): a token match finds "configuration" for "config", which the
// previous substring scan did not.
func TestRecallMatchesTokenPrefixes(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the configuration lives in runtime.yaml")

	got := search(t, store, "config")
	if len(got) != 1 {
		t.Fatalf("token search for %q = %v, want the configuration entry", "config", got)
	}
}

// TestRecallDoesNotMatchMidWordFragments is the other half of the same change:
// a substring scan matched mid-word fragments, a token index does not.
func TestRecallDoesNotMatchMidWordFragments(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the configuration lives in runtime.yaml")

	// "onfig" is a fragment of "configuration". A substring scan would match
	// it; a token index must not. This is asserted rather than tolerated so
	// the behaviour cannot drift back silently.
	if got := search(t, store, "onfig"); len(got) != 0 {
		t.Fatalf("mid-word fragment matched %v, want no match (token matching, ADR 0040 decision 3)", got)
	}
}

// TestRecallIsCaseInsensitive: unicode61 lowercases, so the contract's
// case-insensitivity holds on the index path too.
func TestRecallIsCaseInsensitive(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "prefers terse output")

	if got := search(t, store, "TERSE"); len(got) != 1 {
		t.Fatalf("uppercase query = %v, want the record (matching must not be case-sensitive)", got)
	}
	if got := search(t, store, "TeRsE"); len(got) != 1 {
		t.Fatalf("mixed-case query = %v, want the record", got)
	}
}

// TestRecallSurvivesFTSOperatorsInTheQuery is the escaping guarantee: ordinary
// prose containing FTS5 syntax characters must search, not fail to parse.
//
// FTS5's MATCH argument is a query language, not free text — a bare `-`, `"`,
// `*`, `(`, `)`, `:`, or the keyword AND/OR/NOT is syntax. A navi searching
// for a sentence containing any of those must get a search.
func TestRecallSurvivesFTSOperatorsInTheQuery(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the flag is --dry-run and it is documented")
	remember(t, store, datastore.ScopeMemories, "AND is a keyword in many query languages")
	remember(t, store, datastore.ScopeMemories, "a note with (parentheses) in it")

	// Each of these would be a parse error if passed through unescaped.
	for _, q := range []string{
		"the flag is --dry-run",
		"AND is a keyword",
		"OR NOT",
		"a note with (parentheses)",
		`a "quoted" phrase`,
		"wild*card",
		"col:on",
		"^caret",
		"-leading-dash",
	} {
		got, err := store.Memory().Recall(datastore.RecallQuery{Query: q})
		if err != nil {
			t.Errorf("Recall(%q) errored: %v — operator characters must be escaped, not parsed", q, err)
			continue
		}
		// The assertion is that it did not error; whether it matched is the
		// token matching's business.
		_ = got
	}
}

// TestRecallEscapedQueryStillMatches pins that escaping preserves a real
// match rather than merely avoiding the parse error.
func TestRecallEscapedQueryStillMatches(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the flag is --dry-run and it is documented")

	got := search(t, store, "flag --dry-run")
	if len(got) != 1 {
		t.Fatalf("escaped query = %v, want the documented-flag entry", got)
	}
}

// TestRecallQuotedTermIsSearchedLiterally: a query whose term is wrapped in
// quotes searches for the word itself, not for a quotation mark. The escaped
// expression is `"""quoted"""*`, which is the literal token `quoted` with a
// prefix star — so it finds the entry, rather than looking for a `"` token.
func TestRecallQuotedTermIsSearchedLiterally(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "the word quoted appears here")

	got := search(t, store, `"quoted"`)
	if len(got) != 1 {
		t.Fatalf("quoted term = %v, want the entry containing it", got)
	}
}

// TestRecallKeepsOrderingWithFTS: the keyword path keeps the archive's own
// ordering (importance, then recency). The FTS rank is deliberately not used
// here — blending a rank in is the vector path's job (ADR 0040 decision 6).
func TestRecallKeepsOrderingWithFTS(t *testing.T) {
	store := openMem(t)
	low, err := store.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "deploy note low", Importance: 0.1,
	})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "deploy note high", Importance: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Memory().Recall(datastore.RecallQuery{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("matched %d, want 2", len(got))
	}
	if got[0].ID != high.ID || got[1].ID != low.ID {
		t.Fatalf("order = [%s %s], want most important first ([%s %s])", got[0].ID, got[1].ID, high.ID, low.ID)
	}
}

// TestRecallCombinesQueryWithScopeAndTags: the FTS JOIN must not disturb the
// other filters.
func TestRecallCombinesQueryWithScopeAndTags(t *testing.T) {
	store := openMem(t)
	if _, err := store.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeMemories, Content: "deploy pipeline", Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Memory().Remember(datastore.MemoryRecord{
		Scope: datastore.ScopeNavi, Content: "deploy pipeline", Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Memory().Recall(datastore.RecallQuery{
		Query: "deploy", Scope: datastore.ScopeNavi, Tags: []string{"ops"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope != datastore.ScopeNavi {
		t.Fatalf("combined filter = %+v, want the one navi-scoped entry", got)
	}
}

// TestRecallFindsEntryUpdatedIntoTheIndex: the FTS triggers fire on update, so
// a search finds an entry by its NEW content and not its old content. Without
// this the index would be stale and the whole change would be a regression.
func TestRecallFindsEntryUpdatedIntoTheIndex(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "an old topic about bananas")

	if _, err := store.Memory().Remember(datastore.MemoryRecord{
		ID: rec.ID, Scope: datastore.ScopeMemories, Content: "a new topic about oranges",
	}); err != nil {
		t.Fatal(err)
	}

	if got := search(t, store, "oranges"); len(got) != 1 {
		t.Fatalf("search for the new content = %v, want the updated entry (the FTS trigger must fire on update)", got)
	}
	if got := search(t, store, "bananas"); len(got) != 0 {
		t.Fatalf("search for the OLD content = %v, want no match (a stale index row would match)", got)
	}
}

// TestRecallFindsNothingAfterForget: the delete trigger keeps the index honest,
// so a forgotten entry cannot be resurrected by a search.
func TestRecallFindsNothingAfterForget(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "an ephemeral conclusion")

	if err := store.Memory().Forget(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got := search(t, store, "ephemeral"); len(got) != 0 {
		t.Fatalf("search after forget = %v, want no match (the delete trigger must clear the index)", got)
	}
}

// TestRecallByIDStillWorks: the ID path does not join the index, so it must be
// unaffected by the change.
func TestRecallByIDStillWorks(t *testing.T) {
	store := openMem(t)
	rec := remember(t, store, datastore.ScopeMemories, "read me by id")

	got, err := store.Memory().Recall(datastore.RecallQuery{ID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != rec.ID {
		t.Fatalf("recall by id = %+v, want the one record", got)
	}
}

// TestRecallEmptyQueryReturnsEverything: an empty query is "no constraint", so
// the FTS JOIN must not be added — otherwise an empty MATCH would error.
func TestRecallEmptyQueryReturnsEverything(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "one")
	remember(t, store, datastore.ScopeMemories, "two")

	got, err := store.Memory().Recall(datastore.RecallQuery{})
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("empty query = %d records, want 2", len(got))
	}
}

// TestRecallWhitespaceOnlyQueryIsNoConstraint: a query of only spaces has no
// terms, so it must behave as "no constraint" rather than as a MATCH on
// nothing.
func TestRecallWhitespaceOnlyQueryIsNoConstraint(t *testing.T) {
	store := openMem(t)
	remember(t, store, datastore.ScopeMemories, "a note")

	got, err := store.Memory().Recall(datastore.RecallQuery{Query: "   "})
	if err != nil {
		t.Fatalf("whitespace query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("whitespace query = %d records, want 1 (it is not a constraint)", len(got))
	}
}
