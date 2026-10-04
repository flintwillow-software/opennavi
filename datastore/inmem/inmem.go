// Package inmem is the reference in-memory datastore backend: a minimal,
// non-durable implementation of every aggregate in the datastore
// contract. It exists so the contract is testable now, without waiting on
// the shipped durable backends (datastore/fs, NAVI-365; datastore/sqlite,
// NAVI-364). Nothing survives the process.
package inmem

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// Candidate returns the reference backend descriptor. It is compiled in with a
// navi via n.Datastores.Add(inmem.Candidate()). It is deliberately NOT marked
// default: `datastore/fs` is the first shipped backend and the compiled default
// (ADR 0011), and this backend is the non-durable reference for tests and for a
// deliberately volatile run (`datastore.backend: inmem`).
func Candidate() datastore.Candidate {
	return datastore.Candidate{
		Name:    "inmem",
		Default: false,
		Open:    func(datastore.Params) (datastore.Backend, error) { return New(), nil },
	}
}

// Backend is an empty in-memory backend.
type Backend struct {
	mu          sync.RWMutex
	thoughts    *thought.MemoryStore
	transcripts map[string][]datastore.Entry
	todos       map[string][]datastore.Todo
	notes       map[string][]datastore.Note
	memories    map[string]datastore.MemoryRecord
	// bodyReads counts transcript body loads, for the conformance suite's
	// metadata-without-bodies proof (it implements
	// conformance.ReadReporter). Metadata lookups deliberately do not
	// touch it.
	bodyReads int64
}

// New returns an empty in-memory backend. It implements
// datastore.Backend.
func New() *Backend {
	return &Backend{
		thoughts:    thought.NewMemoryStore(),
		transcripts: map[string][]datastore.Entry{},
		todos:       map[string][]datastore.Todo{},
		notes:       map[string][]datastore.Note{},
		memories:    map[string]datastore.MemoryRecord{},
	}
}

var _ datastore.Backend = (*Backend)(nil)

// ResetReads clears the transcript read counter (conformance.ReadReporter).
func (b *Backend) ResetReads() { atomic.StoreInt64(&b.bodyReads, 0) }

// BodyReads reports transcript body loads since the last ResetReads.
// TranscriptInfo and the thought-row methods must not count here: the
// point of the counter is that cold-start enumeration never reads a
// conversation (ADR 0011 decision 5).
func (b *Backend) BodyReads() int { return int(atomic.LoadInt64(&b.bodyReads)) }

// Thoughts returns the thought/transcript view.
func (b *Backend) Thoughts() datastore.Thoughts { return thoughts{b} }

// ToolState returns the stateful tool state view.
func (b *Backend) ToolState() datastore.ToolState { return toolState{b} }

// Memory returns the memory view.
func (b *Backend) Memory() datastore.Memory { return memory{b} }

// ── thought/transcript ──────────────────────────────────────────────────

type thoughts struct{ b *Backend }

func (t thoughts) SaveThought(th *thought.Thought) error { return t.b.thoughts.SaveThought(th) }

func (t thoughts) LoadThoughts() ([]*thought.Thought, error) { return t.b.thoughts.LoadThoughts() }

func (t thoughts) LoadThought(id string) (*thought.Thought, error) {
	return t.b.thoughts.LoadThought(id)
}

func (t thoughts) DeleteThought(id string) error { return t.b.thoughts.DeleteThought(id) }

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
	atomic.AddInt64(&t.b.bodyReads, 1)
	return cloneEntries(entries), nil
}

func (t thoughts) TranscriptInfo(thoughtID string) (datastore.TranscriptInfo, error) {
	t.b.mu.RLock()
	defer t.b.mu.RUnlock()
	entries, ok := t.b.transcripts[thoughtID]
	if !ok {
		return datastore.TranscriptInfo{}, datastore.ErrTranscriptNotFound
	}
	return datastore.TranscriptInfo{
		ThoughtID:    thoughtID,
		MessageCount: len(entries),
		UpdatedAt:    latestEntryTime(entries),
	}, nil
}

func (t thoughts) DeleteTranscript(thoughtID string) error {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	delete(t.b.transcripts, thoughtID)
	return nil
}

func cloneEntries(in []datastore.Entry) []datastore.Entry {
	if in == nil {
		return nil
	}
	out := make([]datastore.Entry, len(in))
	copy(out, in)
	for i := range out {
		if out[i].ToolInput != nil {
			out[i].ToolInput = append([]byte(nil), out[i].ToolInput...)
		}
	}
	return out
}

func latestEntryTime(entries []datastore.Entry) time.Time {
	var latest time.Time
	for _, e := range entries {
		if e.Time.After(latest) {
			latest = e.Time
		}
	}
	return latest
}

// ── tool state ──────────────────────────────────────────────────────────

type toolState struct{ b *Backend }

func (s toolState) ListTodos(naviID string, status datastore.TodoStatus, limit int) ([]datastore.Todo, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	var out []datastore.Todo
	for _, t := range s.b.todos[naviID] {
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, t)
	}
	sortTodos(out)
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	if out == nil {
		out = []datastore.Todo{}
	}
	return out, nil
}

func (s toolState) ReplaceTodos(naviID string, items []datastore.Todo) ([]datastore.Todo, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now().UTC()
	out := make([]datastore.Todo, 0, len(items))
	for i, item := range items {
		if item.Status == "" {
			item.Status = datastore.TodoPending
		}
		if !item.Status.Valid() {
			return nil, &invalidTodoStatusError{naviID: naviID, index: i, status: item.Status}
		}
		if item.ID == "" {
			item.ID = datastore.NewID()
		}
		item.NaviID = naviID
		item.CreatedAt, item.UpdatedAt = now, now
		out = append(out, item)
	}
	s.b.todos[naviID] = out
	return cloneTodos(out), nil
}

func (s toolState) UpdateTodos(naviID string, updates []datastore.TodoUpdate) ([]datastore.Todo, error) {
	if len(updates) == 0 {
		return []datastore.Todo{}, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	// Apply to a copy so a rejected batch leaves prior state intact.
	current := cloneTodos(s.b.todos[naviID])
	now := time.Now().UTC()
	for i, u := range updates {
		if u.Status != nil && !u.Status.Valid() {
			return nil, &invalidTodoStatusError{naviID: naviID, index: i, status: *u.Status}
		}
		idx := indexTodo(current, u.ID)
		if idx < 0 {
			return nil, notFoundTodo(naviID, u.ID)
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
	out := cloneTodos(current)
	sortTodos(out)
	return out, nil
}

func (s toolState) ReadNotes(naviID string, q datastore.NoteQuery) ([]datastore.Note, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	var out []datastore.Note
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
	if out == nil {
		out = []datastore.Note{}
	}
	return out, nil
}

func (s toolState) UpsertNote(naviID string, n datastore.Note) (datastore.Note, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now().UTC()
	if n.ID == "" {
		n.ID = datastore.NewID()
		n.CreatedAt = now
	} else if idx := indexNote(s.b.notes[naviID], n.ID); idx >= 0 {
		n.CreatedAt = s.b.notes[naviID][idx].CreatedAt
	} else {
		n.CreatedAt = now
	}
	n.NaviID = naviID
	n.UpdatedAt = now
	n.Tags = append([]string(nil), n.Tags...)

	notes := s.b.notes[naviID]
	if idx := indexNote(notes, n.ID); idx >= 0 {
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
	idx := indexNote(notes, id)
	if idx < 0 {
		return notFoundNote(naviID, id)
	}
	s.b.notes[naviID] = append(notes[:idx], notes[idx+1:]...)
	return nil
}

func sortTodos(items []datastore.Todo) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

func indexTodo(items []datastore.Todo, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func indexNote(items []datastore.Note, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func cloneTodos(in []datastore.Todo) []datastore.Todo {
	if in == nil {
		return nil
	}
	out := make([]datastore.Todo, len(in))
	copy(out, in)
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
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
	var out []datastore.MemoryRecord
	for _, rec := range m.b.memories {
		if q.Scope != "" && rec.Scope != q.Scope {
			continue
		}
		if q.Subject != "" && rec.Subject != q.Subject {
			continue
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(rec.Content), strings.ToLower(q.Query)) {
			continue
		}
		if !hasTags(rec.Tags, q.Tags) {
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
	if out == nil {
		out = []datastore.MemoryRecord{}
	}
	return out, nil
}

func (m memory) Forget(id string) error {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	if _, ok := m.b.memories[id]; !ok {
		return notFoundMemory(id)
	}
	delete(m.b.memories, id)
	return nil
}

func hasTags(have, want []string) bool {
	for _, w := range want {
		if !hasTag(have, w) {
			return false
		}
	}
	return true
}
