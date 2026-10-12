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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/principal"
)

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
}

// Service is the always-loaded short-term memory tier.
type Service struct {
	memory   datastore.Memory
	submit   func(datastore.Write)
	subject  principal.Ref
	onChange func(datastore.MemoryRecord, Change)

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
		memory:   cfg.Memory,
		submit:   cfg.Submit,
		subject:  cfg.Subject,
		onChange: cfg.OnChange,
		records:  map[string]datastore.MemoryRecord{},
	}
	if err := s.hydrate(); err != nil {
		return nil, err
	}
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
func (s *Service) Write(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	if !loadable(rec.Scope) {
		return datastore.MemoryRecord{}, fmt.Errorf("memory: scope %q is not part of short-term memory (want user or session)", rec.Scope)
	}
	now := time.Now().UTC()

	s.mu.Lock()
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
