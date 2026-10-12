// Package externalbackend is a synthetic out-of-repo datastore backend. It
// exists to prove one property NAVI-376 asserts in the contract, not in
// prose: the shared conformance suite is importable and runnable by a
// backend that lives in its own module and imports nothing but the public
// contract.
//
// It is deliberately an independent, plain implementation — a map behind
// one mutex, no cleverness, no import of `datastore/inmem` — because the
// point is that the suite is the contract's, not a helper only the in-repo
// backends can use. If the suite demanded knowledge of an in-repo
// implementation, this module would not compile or would not pass.
package externalbackend

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// Backend is a non-durable map-backed datastore backend.
type Backend struct {
	mu          sync.RWMutex
	thoughts    map[string]*thought.Thought
	transcripts map[string][]datastore.Entry
	todos       map[string][]datastore.Todo
	notes       map[string][]datastore.Note
	memories    map[string]datastore.MemoryRecord
}

// New returns an empty backend.
func New() *Backend {
	return &Backend{
		thoughts:    map[string]*thought.Thought{},
		transcripts: map[string][]datastore.Entry{},
		todos:       map[string][]datastore.Todo{},
		notes:       map[string][]datastore.Note{},
		memories:    map[string]datastore.MemoryRecord{},
	}
}

// Candidate returns the backend's descriptor, the way a real out-of-repo
// backend would publish one for a navi to declare.
func Candidate() datastore.Candidate {
	return datastore.Candidate{
		Name:    "external-backend",
		Default: false,
		Open:    func(datastore.Params) (datastore.Backend, error) { return New(), nil },
	}
}

var _ datastore.Backend = (*Backend)(nil)

func (b *Backend) Thoughts() datastore.Thoughts   { return thoughts{b} }
func (b *Backend) ToolState() datastore.ToolState { return toolState{b} }
func (b *Backend) Memory() datastore.Memory       { return memory{b} }

// ── thought/transcript ──────────────────────────────────────────────────

type thoughts struct{ b *Backend }

func (t thoughts) SaveThought(th *thought.Thought) error {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	t.b.thoughts[th.ID] = cloneThought(th)
	return nil
}

func (t thoughts) LoadThoughts() ([]*thought.Thought, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	out := make([]*thought.Thought, 0, len(t.b.thoughts))
	for _, th := range t.b.thoughts {
		out = append(out, cloneThought(th))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (t thoughts) LoadThought(id string) (*thought.Thought, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	th, ok := t.b.thoughts[id]
	if !ok {
		return nil, thought.ErrThoughtNotFound
	}
	return cloneThought(th), nil
}

func (t thoughts) DeleteThought(id string) error {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	if _, ok := t.b.thoughts[id]; !ok {
		return thought.ErrThoughtNotFound
	}
	delete(t.b.thoughts, id)
	return nil
}

func (t thoughts) SaveTranscript(thoughtID string, entries []datastore.Entry) error {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	t.b.transcripts[thoughtID] = cloneEntries(entries)
	return nil
}

func (t thoughts) LoadTranscript(thoughtID string) ([]datastore.Entry, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	entries, ok := t.b.transcripts[thoughtID]
	if !ok {
		return nil, datastore.ErrTranscriptNotFound
	}
	return cloneEntries(entries), nil
}

func (t thoughts) TranscriptInfos() ([]datastore.TranscriptInfo, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	out := make([]datastore.TranscriptInfo, 0, len(t.b.transcripts))
	for id, entries := range t.b.transcripts {
		info := datastore.TranscriptInfo{ThoughtID: id, MessageCount: len(entries)}
		for _, e := range entries {
			if e.Time.After(info.UpdatedAt) {
				info.UpdatedAt = e.Time
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func (t thoughts) TranscriptInfo(thoughtID string) (datastore.TranscriptInfo, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	entries, ok := t.b.transcripts[thoughtID]
	if !ok {
		return datastore.TranscriptInfo{}, datastore.ErrTranscriptNotFound
	}
	// Metadata projection only: the entries are already in memory here, but
	// a real backend would read the count (and a stored updated-at) without
	// loading bodies — which is what conformance.ReadReporter exists to
	// check.
	info := datastore.TranscriptInfo{ThoughtID: thoughtID, MessageCount: len(entries)}
	for _, e := range entries {
		if e.Time.After(info.UpdatedAt) {
			info.UpdatedAt = e.Time
		}
	}
	return info, nil
}

func (t thoughts) DeleteTranscript(thoughtID string) error {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	delete(t.b.transcripts, thoughtID)
	return nil
}

func cloneThought(th *thought.Thought) *thought.Thought {
	out := *th
	if th.Metadata != nil {
		out.Metadata = make(map[string]string, len(th.Metadata))
		for k, v := range th.Metadata {
			out.Metadata[k] = v
		}
	}
	out.Skills = append([]string(nil), th.Skills...)
	out.DependsOn = append([]string(nil), th.DependsOn...)
	out.Checkpoint = append([]byte(nil), th.Checkpoint...)
	return &out
}

func cloneEntries(in []datastore.Entry) []datastore.Entry {
	out := make([]datastore.Entry, len(in))
	copy(out, in)
	for i := range out {
		out[i].ToolInput = append([]byte(nil), out[i].ToolInput...)
	}
	return out
}

// ── tool state ──────────────────────────────────────────────────────────

type toolState struct{ b *Backend }

func (s toolState) ListTodos(naviID string, status datastore.TodoStatus, limit int) ([]datastore.Todo, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := []datastore.Todo{}
	for _, it := range s.b.todos[naviID] {
		if status != "" && it.Status != status {
			continue
		}
		out = append(out, it)
	}
	sortTodos(out)
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (s toolState) ReplaceTodos(naviID string, items []datastore.Todo) ([]datastore.Todo, error) {
	now := time.Now().UTC()
	out := make([]datastore.Todo, 0, len(items))
	for i, it := range items {
		if it.Status == "" {
			it.Status = datastore.TodoPending
		}
		if !it.Status.Valid() {
			return nil, fmt.Errorf("external-backend: todos for %s: item %d has invalid status %q", naviID, i, it.Status)
		}
		if it.ID == "" {
			it.ID = datastore.NewID()
		}
		it.NaviID = naviID
		it.CreatedAt, it.UpdatedAt = now, now
		out = append(out, it)
	}
	s.b.mu.Lock()
	s.b.todos[naviID] = out
	s.b.mu.Unlock()
	return append([]datastore.Todo(nil), out...), nil
}

func (s toolState) UpdateTodos(naviID string, updates []datastore.TodoUpdate) ([]datastore.Todo, error) {
	if len(updates) == 0 {
		return []datastore.Todo{}, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	// Work on a copy so a rejected batch leaves the stored list intact.
	current := append([]datastore.Todo(nil), s.b.todos[naviID]...)
	now := time.Now().UTC()
	for i, u := range updates {
		if u.Status != nil && !u.Status.Valid() {
			return nil, fmt.Errorf("external-backend: todos for %s: item %d has invalid status %q", naviID, i, *u.Status)
		}
		idx := -1
		for j := range current {
			if current[j].ID == u.ID {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("external-backend: todos for %s: %w (id %s)", naviID, datastore.ErrTodoNotFound, u.ID)
		}
		if u.Remove {
			current = append(current[:idx], current[idx+1:]...)
			continue
		}
		if u.Text != nil {
			current[idx].Text = *u.Text
		}
		if u.Status != nil {
			current[idx].Status = *u.Status
		}
		if u.Priority != nil {
			current[idx].Priority = *u.Priority
		}
		current[idx].UpdatedAt = now
	}
	s.b.todos[naviID] = current
	out := append([]datastore.Todo(nil), current...)
	sortTodos(out)
	return out, nil
}

func (s toolState) ReadNotes(naviID string, q datastore.NoteQuery) ([]datastore.Note, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := []datastore.Note{}
	for _, n := range s.b.notes[naviID] {
		if q.ID != "" && n.ID != q.ID {
			continue
		}
		if q.Tag != "" && !hasTag(n.Tags, q.Tag) {
			continue
		}
		if q.Query != "" && !strings.Contains(n.Title, q.Query) && !strings.Contains(n.Body, q.Query) {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s toolState) UpsertNote(naviID string, n datastore.Note) (datastore.Note, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now().UTC()
	notes := s.b.notes[naviID]
	idx := -1
	for j := range notes {
		if notes[j].ID == n.ID && n.ID != "" {
			idx = j
			break
		}
	}
	if n.ID == "" {
		n.ID = datastore.NewID()
		n.CreatedAt = now
	} else if idx >= 0 {
		n.CreatedAt = notes[idx].CreatedAt
	} else {
		n.CreatedAt = now
	}
	n.NaviID = naviID
	n.UpdatedAt = now
	n.Tags = append([]string(nil), n.Tags...)
	if idx >= 0 {
		notes[idx] = n
	} else {
		notes = append(notes, n)
	}
	s.b.notes[naviID] = notes
	return n, nil
}

func (s toolState) RemoveNote(naviID, id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	notes := s.b.notes[naviID]
	for j := range notes {
		if notes[j].ID == id {
			s.b.notes[naviID] = append(notes[:j], notes[j+1:]...)
			return nil
		}
	}
	return fmt.Errorf("external-backend: notes for %s: %w (id %s)", naviID, datastore.ErrNoteNotFound, id)
}

func sortTodos(items []datastore.Todo) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// hasAllTags reports whether every wanted tag is present: recall requires
// all of them, not any.
func hasAllTags(have, want []string) bool {
	for _, w := range want {
		if !hasTag(have, w) {
			return false
		}
	}
	return true
}

// ── memory ──────────────────────────────────────────────────────────────

type memory struct{ b *Backend }

func (m memory) Remember(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	now := time.Now().UTC()
	if rec.ID == "" {
		rec.ID = datastore.NewID()
		rec.CreatedAt = now
	} else if existing, ok := m.b.memories[rec.ID]; ok {
		rec.CreatedAt = existing.CreatedAt
	} else {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	rec.Tags = append([]string(nil), rec.Tags...)
	m.b.memories[rec.ID] = rec
	return rec, nil
}

func (m memory) Recall(q datastore.RecallQuery) ([]datastore.MemoryRecord, error) {
	m.b.mu.RLock()
	defer m.b.mu.RUnlock()
	out := []datastore.MemoryRecord{}
	for _, rec := range m.b.memories {
		if q.Scope != "" && rec.Scope != q.Scope {
			continue
		}
		if !q.Subject.IsZero() && rec.Subject != q.Subject {
			continue
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(rec.Content), strings.ToLower(q.Query)) {
			continue
		}
		if !hasAllTags(rec.Tags, q.Tags) {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Importance != out[j].Importance {
			return out[i].Importance > out[j].Importance
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

func (m memory) Forget(id string) error {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	if _, ok := m.b.memories[id]; !ok {
		return fmt.Errorf("external-backend: memory %s: %w", id, datastore.ErrMemoryNotFound)
	}
	delete(m.b.memories, id)
	return nil
}
