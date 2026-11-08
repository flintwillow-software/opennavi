package memory

import (
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/principal"
)

// wordCounter counts one token per whitespace-separated word, so a test can
// reason about budgets exactly without depending on a BPE table.
type wordCounter struct{}

func (wordCounter) CountText(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return len(strings.Fields(text))
}

// newBudgetedService builds a service with a word counter and the supplied
// per-scope budgets.
func newBudgetedService(t *testing.T, budgets map[datastore.Scope]int) (*Service, *inmem.Backend) {
	t.Helper()
	backend := inmem.New()
	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(w datastore.Write) { _ = w.Apply(backend) },
		Subject: principal.MustParse("navi:navi"),
		Budgets: budgets,
		Counter: wordCounter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, backend
}

// TestWriteRefusedOverBudget is ADR 0039 decision 1: a write that would push a
// scope over budget is refused with an actionable error, and the tier's
// existing contents are never destroyed.
func TestWriteRefusedOverBudget(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 5})

	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"}); err != nil {
		t.Fatalf("write within budget: %v", err)
	}

	// 3 + 3 = 6 > 5: refused.
	_, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "four five six"})
	if err == nil {
		t.Fatal("write over budget must be refused")
	}
	if !errors.Is(err, ErrOverBudget) {
		t.Fatalf("err = %v, want ErrOverBudget", err)
	}
	var obe *OverBudgetError
	if !errors.As(err, &obe) {
		t.Fatalf("err = %v, want *OverBudgetError", err)
	}
	if obe.Budget != 5 || obe.Used != 3 || obe.Adding != 3 {
		t.Fatalf("over-budget detail = %+v, want budget 5 used 3 adding 3", obe)
	}

	// Nothing was destroyed: the first note is still there.
	if got := svc.Records(); len(got) != 1 || got[0].Content != "one two three" {
		t.Fatalf("refused write must not disturb the tier, got %+v", got)
	}
}

// TestWriteAtBudgetAllowed pins the boundary: the rule is "over", not "at", so
// a write that lands exactly on the budget is allowed.
func TestWriteAtBudgetAllowed(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 4})
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three four"}); err != nil {
		t.Fatalf("write exactly at budget must be allowed: %v", err)
	}
}

// TestRemovalsNeverGated is ADR 0039 decision 1: removals and long-term
// transitions are never gated, so a navi over budget can always recover.
func TestRemovalsNeverGated(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 5})
	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "four five six"}); err == nil {
		t.Fatal("expected refusal while over budget")
	}
	// Removing is always allowed, and re-opens the budget.
	if err := svc.Remove(rec.ID); err != nil {
		t.Fatalf("remove must never be gated: %v", err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "four five six"}); err != nil {
		t.Fatalf("write after removing must be allowed: %v", err)
	}
}

// TestBudgetsArePerScope is ADR 0039 decision 2: a burst in one scope cannot
// consume the room reserved for another.
func TestBudgetsArePerScope(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{
		datastore.ScopeSession: 3,
		datastore.ScopeUser:    3,
	})
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"}); err != nil {
		t.Fatal(err)
	}
	// Session is full, but user is untouched and still writable.
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeUser, Content: "one two three"}); err != nil {
		t.Fatalf("user scope must have its own budget: %v", err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "four"}); err == nil {
		t.Fatal("session scope must still be full")
	}
}

// TestReplacementMeasuredAsDelta: rewriting a note in place is not penalised
// for the text it already occupies — only growth counts.
func TestReplacementMeasuredAsDelta(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 4})
	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"})
	if err != nil {
		t.Fatal(err)
	}
	// Replacing 3 words with 4 is a delta of 1; used (3) + 1 = 4 <= 4: allowed.
	if _, err := svc.Write(datastore.MemoryRecord{ID: rec.ID, Scope: datastore.ScopeSession, Content: "one two three four"}); err != nil {
		t.Fatalf("in-place growth within budget must be allowed: %v", err)
	}
	// Replacing 4 with 5 is a delta of 1; used (4) + 1 = 5 > 4: refused.
	if _, err := svc.Write(datastore.MemoryRecord{ID: rec.ID, Scope: datastore.ScopeSession, Content: "one two three four five"}); err == nil {
		t.Fatal("in-place growth over budget must be refused")
	}
}

// TestShrinkingReplacementAlwaysAllowed: a replacement that does not grow the
// scope is never refused, even at the limit.
func TestShrinkingReplacementAlwaysAllowed(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 3})
	rec, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{ID: rec.ID, Scope: datastore.ScopeSession, Content: "one"}); err != nil {
		t.Fatalf("shrinking replacement must be allowed: %v", err)
	}
}

// TestOverBudgetHookOnRefusedWrite is ADR 0039 decision 4: a refused write
// tells the runtime so it can schedule a cleanup.
func TestOverBudgetHookOnRefusedWrite(t *testing.T) {
	var got []datastore.Scope
	backend := inmem.New()
	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(w datastore.Write) { _ = w.Apply(backend) },
		Subject: principal.MustParse("navi:navi"),
		Budgets: map[datastore.Scope]int{datastore.ScopeSession: 2},
		Counter: wordCounter{},
		OnOverBudget: func(s datastore.Scope) {
			got = append(got, s)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"}); err == nil {
		t.Fatal("expected refusal")
	}
	if len(got) != 1 || got[0] != datastore.ScopeSession {
		t.Fatalf("over-budget hook = %v, want [session]", got)
	}
}

// TestOverBudgetHookOnHydrate is ADR 0039 decision 4: a persisted set that
// exceeds its budget is over budget from the start.
func TestOverBudgetHookOnHydrate(t *testing.T) {
	backend := inmem.New()
	// Seed an over-budget persisted set directly.
	if _, err := backend.Memory().Remember(datastore.MemoryRecord{
		Scope:   datastore.ScopeSession,
		Subject: principal.MustParse("navi:navi"),
		Content: "one two three four five",
	}); err != nil {
		t.Fatal(err)
	}
	var got []datastore.Scope
	_, err := New(Config{
		Memory:       backend.Memory(),
		Submit:       func(w datastore.Write) { _ = w.Apply(backend) },
		Subject:      principal.MustParse("navi:navi"),
		Budgets:      map[datastore.Scope]int{datastore.ScopeSession: 2},
		Counter:      wordCounter{},
		OnOverBudget: func(s datastore.Scope) { got = append(got, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != datastore.ScopeSession {
		t.Fatalf("hydrate over-budget hook = %v, want [session]", got)
	}
}

// TestUsageReportsFullness is ADR 0039 decision 4's "how full am I" affordance.
func TestUsageReportsFullness(t *testing.T) {
	svc, _ := newBudgetedService(t, map[datastore.Scope]int{datastore.ScopeSession: 10})
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "one two three"}); err != nil {
		t.Fatal(err)
	}
	used, budget := svc.Usage(datastore.ScopeSession)
	if used != 3 || budget != 10 {
		t.Fatalf("usage = (%d, %d), want (3, 10)", used, budget)
	}
}

// TestDefaultBudgetWhenNoneDeclared: a navi that declares no budget still gets
// a bound rather than an unbounded tier.
func TestDefaultBudgetWhenNoneDeclared(t *testing.T) {
	svc, _ := newBudgetedService(t, nil)
	_, budget := svc.Usage(datastore.ScopeSession)
	if budget != DefaultShortTermMemoryBudget {
		t.Fatalf("budget = %d, want default %d", budget, DefaultShortTermMemoryBudget)
	}
}

// TestHeuristicFallbackWithoutCounter: a navi that declares no tokenizer still
// gets a sane bound (ADR 0039 decision 3, heuristic fallback).
func TestHeuristicFallbackWithoutCounter(t *testing.T) {
	backend := inmem.New()
	svc, err := New(Config{
		Memory:  backend.Memory(),
		Submit:  func(w datastore.Write) { _ = w.Apply(backend) },
		Subject: principal.MustParse("navi:navi"),
		Budgets: map[datastore.Scope]int{datastore.ScopeSession: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	// ~4 runes per token: 16 chars is 4 tokens, which fits.
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "0123456789abcdef"}); err != nil {
		t.Fatalf("heuristic fallback must count: %v", err)
	}
	// Another 4 tokens overflows.
	if _, err := svc.Write(datastore.MemoryRecord{Scope: datastore.ScopeSession, Content: "0123456789abcdef"}); err == nil {
		t.Fatal("heuristic fallback must enforce the bound")
	}
}
