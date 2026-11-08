// Package memory is the navi's short-term memory: the always-loaded tier of
// user and session notes (docs/architecture/memory.md, ADR 0013).
//
// It is navi-internal by design: it is the navi's own working state, not a
// capability an extension supplies, so it is not a public component package
// and an out-of-repo module cannot reach it. The runtime injects the notes
// into every thought's system prompt and exposes the built-in memory_write
// tool for deliberate updates.
//
// The always-loaded set is held in RAM: hydrated once at assembly, injected
// per thought without a datastore read on the run path, and written back
// through the datastore's ordered writer. Writes land in RAM immediately, so
// a note written during one thought is visible to the next without waiting
// for a flush.
package memory

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/principal"
)

// DefaultShortTermMemoryBudget is the per-scope token budget a short-term
// scope gets when the navi declares none (ADR 0039 decision 2). It is
// deliberately small: the tier is injected into every thought, so it is a
// context cost paid by all future work. 2000 tokens is roughly 5% of the
// default 16k thought budget — room for the notes that matter, not a
// transcript.
const DefaultShortTermMemoryBudget = 2000

// ErrOverBudget is returned by Write when an addition would push a scope over
// its token budget (ADR 0039 decision 1). Removals and long-term transitions
// are never gated, so a navi that is over budget can always bring itself back
// under by removing — it simply cannot grow further until it does. The error
// is loud and actionable rather than a silent eviction: nothing the navi kept
// is ever destroyed to make room.
var ErrOverBudget = errors.New("memory: short-term scope is over its token budget")

// TokenCounter is the narrow token-counting seam ADR 0039 decision 3 puts in
// front of the budget gate. It is satisfied by llm.Tokenizer; the memory
// package does not depend on llm so the tier stays usable without a
// tokenizer.
type TokenCounter interface {
	CountText(text string) int
}

// heuristicCounter is the fallback when the navi declares no tokenizer (ADR
// 0039 decision 3: "the heuristic remains the fallback when none is
// available"). It mirrors llm.HeuristicTokenCounter's prose ratio (~4 runes
// per token) so a navi without a real counter still gets a sane bound rather
// than none.
type heuristicCounter struct{}

func (heuristicCounter) CountText(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + 3) / 4
}

// countText sizes text with the declared counter, or the heuristic fallback.
func (s *Service) countText(text string) int {
	if s.counter == nil {
		return heuristicCounter{}.CountText(text)
	}
	return s.counter.CountText(text)
}

// OverBudgetError names the scope, its budget, and the size the write would
// have produced, so the refusal is actionable (ADR 0039 decision 1: "a
// refused write with an actionable error").
type OverBudgetError struct {
	Scope  datastore.Scope
	Budget int
	Used   int
	Adding int
}

func (e *OverBudgetError) Error() string {
	return fmt.Sprintf(
		"memory: %s scope is over its %d-token budget (using %d, this write adds %d) — remove or move notes to long-term memory with ltm_write before adding more; removals are never blocked",
		e.Scope, e.Budget, e.Used, e.Adding)
}

// Is reports whether err is an over-budget refusal, so a caller can
// distinguish "the tier is full" from a real failure.
func (e *OverBudgetError) Is(target error) bool { return target == ErrOverBudget }

// alwaysLoaded is the tier in front of the navi at every thought: notes
// about the human, and the working state of the current interaction. The
// navi and archive scopes are searched on demand and belong to long-term
// memory (NAVI-350), not here.
var alwaysLoaded = []datastore.Scope{datastore.ScopeUser, datastore.ScopeSession}

// Change classifies a memory mutation reported to Config.OnChange.
type Change string

const (
	// ChangeCreated is a record that did not exist before.
	ChangeCreated Change = "create"
	// ChangeUpdated is a record that replaced an existing one.
	ChangeUpdated Change = "update"
	// ChangeRemoved is a record that was deleted.
	ChangeRemoved Change = "delete"
)

// Config builds a Service.
type Config struct {
	// Memory is the datastore memory view, used to hydrate at construction
	// and as the persistence sink behind the ordered writer.
	Memory datastore.Memory
	// Submit stages a write on the datastore's ordered writer.
	Submit func(datastore.Write)
	// Subject keys the records: the principal that owns them. The navi's
	// own service principal, navi:<name>, keys the always-loaded scopes for
	// the MVP's single-user deployment (ADR 0021 decision 10).
	Subject principal.Ref
	// OnChange, when set, observes a memory mutation AFTER it has been
	// persisted, so a live view (the typed-object surface, ADR 0017) never
	// reports a record its own reads cannot yet see. It runs on the ordered
	// writer's goroutine and must be cheap and must not re-enter the
	// service.
	OnChange func(rec datastore.MemoryRecord, change Change)

	// Budgets declares the per-scope token budget (ADR 0039 decision 2). A
	// scope absent from the map uses DefaultShortTermMemoryBudget. Only the
	// always-loaded scopes are budgeted; a long-term scope is not gated.
	Budgets map[datastore.Scope]int

	// Counter counts tokens for the budget gate. Nil means the heuristic
	// fallback (ADR 0039 decision 3), so the tier still works without a
	// tokenizer.
	Counter TokenCounter

	// OnOverBudget, when set, is called when a scope is found over budget —
	// on a refused write, or on hydrate of a persisted set that exceeds it.
	// The runtime uses it to schedule a cleanup thought (ADR 0039 decision
	// 4). It must not block.
	OnOverBudget func(scope datastore.Scope)
}

// Service is the always-loaded short-term memory tier.
type Service struct {
	memory       datastore.Memory
	submit       func(datastore.Write)
	subject      principal.Ref
	onChange     func(datastore.MemoryRecord, Change)
	budgets      map[datastore.Scope]int
	counter      TokenCounter
	onOverBudget func(datastore.Scope)

	mu      sync.RWMutex
	records map[string]datastore.MemoryRecord
}

// New builds the service and hydrates the always-loaded set from the
// datastore (cold start).
func New(cfg Config) (*Service, error) {
	if cfg.Memory == nil {
		return nil, fmt.Errorf("memory: a datastore memory view is required")
	}
	if cfg.Submit == nil {
		return nil, fmt.Errorf("memory: a write submitter is required")
	}
	if cfg.Subject.IsZero() {
		return nil, fmt.Errorf("memory: a subject is required")
	}
	s := &Service{
		memory:       cfg.Memory,
		submit:       cfg.Submit,
		subject:      cfg.Subject,
		onChange:     cfg.OnChange,
		budgets:      cfg.Budgets,
		counter:      cfg.Counter,
		onOverBudget: cfg.OnOverBudget,
		records:      map[string]datastore.MemoryRecord{},
	}
	if err := s.hydrate(); err != nil {
		return nil, err
	}
	// A persisted set that exceeds its budget is over budget from the start
	// (ADR 0039 decision 4): the runtime schedules a cleanup rather than
	// blocking, and the navi can still remove and move freely.
	s.reportOverBudget()
	return s, nil
}

// hydrate loads the always-loaded scopes into RAM.
func (s *Service) hydrate() error {
	for _, scope := range alwaysLoaded {
		records, err := s.memory.Recall(datastore.RecallQuery{Scope: scope, Subject: s.subject})
		if err != nil {
			return fmt.Errorf("memory: hydrate %s scope: %w", scope, err)
		}
		for _, r := range records {
			s.records[r.ID] = r
		}
	}
	return nil
}

// Inject renders the always-loaded block for a thought's system prompt. It
// reads RAM only — never the datastore — so injection never puts a backend
// read on the run path. It returns "" when there is nothing to inject.
func (s *Service) Inject() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sections []string
	for _, scope := range alwaysLoaded {
		records := s.byScope(scope)
		if len(records) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("### ")
		b.WriteString(string(scope))
		b.WriteString("\n")
		for _, r := range records {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(r.Content))
			b.WriteString("\n")
		}
		sections = append(sections, strings.TrimRight(b.String(), "\n"))
	}
	if len(sections) == 0 {
		return ""
	}

	var out strings.Builder
	out.WriteString("## Short-term memory\n")
	out.WriteString("Notes you keep in context, injected for every thought. They are yours to maintain: ")
	out.WriteString("use memory_write to add, replace, or remove an entry. Keep them deliberate — only what is worth carrying forward.\n\n")
	out.WriteString(strings.Join(sections, "\n\n"))
	return out.String()
}

// Write stores a record. It lands in RAM immediately (visible to the next
// thought) and is staged on the ordered writer for persistence.
//
// The write is gated at the door, not the tier (ADR 0039 decision 1): a write
// that would push the scope OVER its token budget is refused with
// ErrOverBudget, and the tier's existing contents are never destroyed. A
// REPLACEMENT of an existing note is measured as the delta, so rewriting a
// note in place is not penalised for the text it already occupies — only the
// growth counts. Removals (Remove) and long-term transitions (ltm_write, the
// separate LongTerm service) are never gated, so a navi that is over budget
// can always bring itself back under.
func (s *Service) Write(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	if !loadable(rec.Scope) {
		return datastore.MemoryRecord{}, fmt.Errorf("memory: scope %q is not part of short-term memory (want user or session)", rec.Scope)
	}
	now := time.Now().UTC()

	s.mu.Lock()
	// Measure the addition: the new note's size, minus what an in-place
	// replacement already occupies. Computed under the lock so two concurrent
	// writes cannot both pass the gate and then overshoot together.
	var replacing int
	if rec.ID != "" {
		if prev, ok := s.records[rec.ID]; ok {
			replacing = s.countText(prev.Content)
		}
	}
	adding := s.countText(rec.Content) - replacing
	if adding < 0 {
		adding = 0 // a shrinking replacement is not an addition
	}
	if err := s.checkBudgetLocked(rec.Scope, adding); err != nil {
		s.mu.Unlock()
		// A refused write is one of the two over-budget triggers (ADR 0039
		// decision 4): tell the runtime so it can schedule a cleanup. Call
		// out after releasing the lock.
		if s.onOverBudget != nil {
			s.onOverBudget(rec.Scope)
		}
		return datastore.MemoryRecord{}, err
	}
	change := ChangeUpdated
	if rec.ID == "" {
		rec.ID = datastore.NewID()
		rec.CreatedAt = now
		change = ChangeCreated
	} else if prev, ok := s.records[rec.ID]; ok {
		rec.CreatedAt = prev.CreatedAt
	} else {
		rec.CreatedAt = now
		change = ChangeCreated
	}
	rec.Subject = s.subject
	rec.UpdatedAt = now
	rec.Tags = append([]string(nil), rec.Tags...)
	s.records[rec.ID] = rec
	s.mu.Unlock()

	s.stageWrite(rec, change)
	return rec, nil
}

// Remove deletes a record by id.
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	rec, ok := s.records[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("memory: %w (id %s)", datastore.ErrMemoryNotFound, id)
	}
	delete(s.records, id)
	s.mu.Unlock()

	s.submit(datastore.Write{
		Aggregate: datastore.AggregateMemory,
		Key:       "memory/" + id,
		Apply: func(b datastore.Backend) error {
			if err := b.Memory().Forget(id); err != nil {
				return err
			}
			s.notify(rec, ChangeRemoved)
			return nil
		},
	})
	return nil
}

// Records returns a snapshot of the always-loaded set (tests, diagnostics,
// and a future portal view).
func (s *Service) Records() []datastore.MemoryRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]datastore.MemoryRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Service) stageWrite(rec datastore.MemoryRecord, change Change) {
	clone := rec
	s.submit(datastore.Write{
		Aggregate: datastore.AggregateMemory,
		Key:       "memory/" + clone.ID,
		Apply: func(b datastore.Backend) error {
			if _, err := b.Memory().Remember(clone); err != nil {
				return err
			}
			s.notify(clone, change)
			return nil
		},
	})
}

// notify reports a persisted mutation to the configured observer.
func (s *Service) notify(rec datastore.MemoryRecord, change Change) {
	if s.onChange != nil {
		s.onChange(rec, change)
	}
}

func (s *Service) byScope(scope datastore.Scope) []datastore.MemoryRecord {
	out := make([]datastore.MemoryRecord, 0, len(s.records))
	for _, r := range s.records {
		if r.Scope == scope {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func loadable(scope datastore.Scope) bool {
	for _, s := range alwaysLoaded {
		if s == scope {
			return true
		}
	}
	return false
}

// budget returns the token budget for a scope, or the default when none was
// declared (ADR 0039 decision 2).
func (s *Service) budget(scope datastore.Scope) int {
	if n, ok := s.budgets[scope]; ok && n > 0 {
		return n
	}
	return DefaultShortTermMemoryBudget
}

// checkBudgetLocked refuses an addition that would push scope over budget. It
// must be called with s.mu held. The rule is "over", not "at": a write that
// lands exactly on the budget is allowed, so the bound is inclusive and the
// navi is not refused a note that fits (ADR 0039 decision 1).
func (s *Service) checkBudgetLocked(scope datastore.Scope, adding int) error {
	used := s.scopeTokensLocked(scope)
	budget := s.budget(scope)
	if used+adding <= budget {
		return nil
	}
	return &OverBudgetError{Scope: scope, Budget: budget, Used: used, Adding: adding}
}

// scopeTokensLocked sums the token count of every note in a scope. Must be
// called with s.mu held.
func (s *Service) scopeTokensLocked(scope datastore.Scope) int {
	total := 0
	for _, r := range s.records {
		if r.Scope == scope {
			total += s.countText(r.Content)
		}
	}
	return total
}

// Usage reports the token usage and budget for a scope, so a navi can tell
// how full it is before hitting the limit (ADR 0039 decision 4's "how full am
// I" affordance).
func (s *Service) Usage(scope datastore.Scope) (used, budget int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scopeTokensLocked(scope), s.budget(scope)
}

// SetOverBudgetHook installs (or replaces) the over-budget observer after
// construction, so the runtime can wire the cleanup scheduler without the
// memory service depending on the navi. It is safe to call before serving
// begins; it is not safe to call concurrently with a write.
func (s *Service) SetOverBudgetHook(fn func(datastore.Scope)) {
	s.mu.Lock()
	s.onOverBudget = fn
	s.mu.Unlock()
}

// reportOverBudget calls OnOverBudget for every always-loaded scope that is
// already over budget. The runtime schedules a cleanup from it (ADR 0039
// decision 4). It reads under the lock and calls out after releasing it.
func (s *Service) reportOverBudget() {
	if s.onOverBudget == nil {
		return
	}
	s.mu.RLock()
	over := make([]datastore.Scope, 0, len(alwaysLoaded))
	for _, scope := range alwaysLoaded {
		if s.scopeTokensLocked(scope) > s.budget(scope) {
			over = append(over, scope)
		}
	}
	s.mu.RUnlock()
	for _, scope := range over {
		s.onOverBudget(scope)
	}
}
