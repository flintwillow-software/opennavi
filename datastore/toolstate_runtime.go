package datastore

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// The RAM-first tool-state layer (ADR 0011 decisions 1, 6, 7, 9; the
// tool-state slice of NAVI-374).
//
// Tool state — todos and notes — is the source of truth in RAM. The bound
// backend's ToolState view is the persistence sink: mutations land in RAM
// under the lock (zero I/O), a flusher persists dirty navis outside the
// lock on a cadence, and a failed flush re-marks the navi and surfaces via
// the error callback rather than blocking a producer. This is the pattern
// thought.Queue uses for thoughts, applied to a datastore aggregate; it is
// deliberately backend-agnostic, so fs, sqlite, and inmem all serve it and
// no backend does per-call I/O on a tool dispatch.
//
// Hydration is lazy per navi identity: the first access loads that navi's
// state from the sink. NAVI-374 moves hydration to cold start and folds
// this per-aggregate flusher into the single ordered writer at the
// datastore level; the shape of this layer is what that work generalizes.

// DefaultToolStateFlushInterval is the flusher cadence default.
const DefaultToolStateFlushInterval = 250 * time.Millisecond

// ToolStateConfig tunes the RAM-first tool-state layer.
type ToolStateConfig struct {
	// FlushInterval is the flush cadence: 0 selects the default, a
	// negative value is synchronous write-through (no flusher — the same
	// mode thought.Config exposes).
	FlushInterval time.Duration
	// OnError observes a failed flush. Failures never block a producer;
	// they re-mark the navi dirty for the next tick.
	OnError func(error)
}

// toolStateRAM is the RAM-first ToolState the handle serves. Only the
// handle constructs it, over the active backend's own ToolState view.
type toolStateRAM struct {
	sink     ToolState
	sync     bool
	interval time.Duration
	onError  func(error)

	mu    sync.Mutex
	navis map[string]*ramNavi

	// loadMu serializes lazy hydration so two concurrent first-accesses
	// cannot race a stale snapshot over a fresh one. Hydration I/O runs
	// outside r.mu and st.mu.
	loadMu sync.Mutex

	flushMu sync.Mutex
	stop    chan struct{}
}

// ramNavi is one navi's in-RAM tool state, with its own lock so a slow
// read on one navi cannot block the map.
type ramNavi struct {
	mu sync.RWMutex

	loaded bool

	todos      []Todo
	todosDirty bool

	notes []Note
	// persistedNotes is the last state known to be in the sink, so a flush
	// writes only the difference. Note ids are RAM-assigned.
	persistedNotes map[string]Note
}

func newToolStateRAM(sink ToolState, cfg ToolStateConfig) *toolStateRAM {
	interval := cfg.FlushInterval
	sync := interval < 0
	if !sync && interval == 0 {
		interval = DefaultToolStateFlushInterval
	}
	return &toolStateRAM{
		sink:     sink,
		sync:     sync,
		interval: interval,
		onError:  cfg.OnError,
		navis:    map[string]*ramNavi{},
	}
}

// ── lifecycle ───────────────────────────────────────────────────────────

// startFlusher launches the dirty flusher (idempotent). Synchronous mode
// starts nothing — mutations are written through. The returned stop
// function stops the ticker and drains once more.
func (r *toolStateRAM) startFlusher() func() {
	if r.sync {
		return func() {}
	}

	r.flushMu.Lock()
	if r.stop != nil {
		r.flushMu.Unlock()
		return func() {}
	}
	done := make(chan struct{})
	r.stop = done
	r.flushMu.Unlock()

	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				r.flush()
			}
		}
	}()

	return func() {
		r.flushMu.Lock()
		select {
		case <-done:
		default:
			close(done)
		}
		r.stop = nil
		r.flushMu.Unlock()
		r.flush()
	}
}

// flush drains every navi with pending changes, persisting outside the
// locks. Failures re-mark the navi and go to OnError.
func (r *toolStateRAM) flush() {
	for id, st := range r.dirtyNavis() {
		if err := r.flushNavi(id, st); err != nil && r.onError != nil {
			r.onError(err)
		}
	}
}

func (r *toolStateRAM) dirtyNavis() map[string]*ramNavi {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]*ramNavi{}
	for id, st := range r.navis {
		st.mu.RLock()
		dirty := st.todosDirty || notesDifferLocked(st)
		st.mu.RUnlock()
		if dirty {
			out[id] = st
		}
	}
	return out
}

// flushNavi persists one navi: it snapshots under the per-navi lock and
// writes outside it. A failed write re-marks the navi for the next tick,
// so nothing is lost.
func (r *toolStateRAM) flushNavi(naviID string, st *ramNavi) error {
	st.mu.Lock()
	todos := cloneTodos(st.todos)
	todosDirty := st.todosDirty
	// Clear before writing: a mutation that lands while the write runs
	// re-sets the flag, so the newer state is not lost.
	st.todosDirty = false
	current := indexNotes(st.notes)
	previous := st.persistedNotes
	st.mu.Unlock()

	if todosDirty {
		if _, err := r.sink.ReplaceTodos(naviID, todos); err != nil {
			st.mu.Lock()
			st.todosDirty = true
			st.mu.Unlock()
			return err
		}
	}

	for id, n := range current {
		if prev, ok := previous[id]; ok && notesEqual(prev, n) {
			continue
		}
		if _, err := r.sink.UpsertNote(naviID, n); err != nil {
			return err
		}
	}
	for id := range previous {
		if _, ok := current[id]; ok {
			continue
		}
		if err := r.sink.RemoveNote(naviID, id); err != nil && !errors.Is(err, ErrNoteNotFound) {
			return err
		}
	}

	// persistedNotes records what this flush wrote; a change that landed
	// during the write is absent and so still reads as different.
	st.mu.Lock()
	st.persistedNotes = current
	st.mu.Unlock()
	return nil
}

// ── hydration ───────────────────────────────────────────────────────────

func (r *toolStateRAM) stateLocked(naviID string) *ramNavi {
	st := r.navis[naviID]
	if st == nil {
		st = &ramNavi{}
		r.navis[naviID] = st
	}
	return st
}

// ensure returns the navi's RAM state, hydrating it from the sink on
// first access.
func (r *toolStateRAM) ensure(naviID string) (*ramNavi, error) {
	r.mu.Lock()
	st := r.stateLocked(naviID)
	r.mu.Unlock()

	st.mu.RLock()
	loaded := st.loaded
	st.mu.RUnlock()
	if loaded {
		return st, nil
	}

	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	st.mu.RLock()
	loaded = st.loaded
	st.mu.RUnlock()
	if loaded {
		return st, nil
	}

	todos, err := r.sink.ListTodos(naviID, "", 0)
	if err != nil {
		return nil, err
	}
	notes, err := r.sink.ReadNotes(naviID, NoteQuery{})
	if err != nil {
		return nil, err
	}

	st.mu.Lock()
	if !st.loaded {
		st.todos = todos
		st.notes = notes
		st.persistedNotes = indexNotes(notes)
		st.loaded = true
	}
	st.mu.Unlock()
	return st, nil
}

// syncFlush writes a navi through immediately in synchronous mode.
func (r *toolStateRAM) syncFlush(naviID string) {
	if !r.sync {
		return
	}
	r.mu.Lock()
	st := r.navis[naviID]
	r.mu.Unlock()
	if st == nil {
		return
	}
	if err := r.flushNavi(naviID, st); err != nil && r.onError != nil {
		r.onError(err)
	}
}

// ── ToolState implementation ────────────────────────────────────────────

func (r *toolStateRAM) ListTodos(naviID string, status TodoStatus, limit int) ([]Todo, error) {
	st, err := r.ensure(naviID)
	if err != nil {
		return nil, err
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := filterTodos(st.todos, status, limit)
	if out == nil {
		out = []Todo{}
	}
	return out, nil
}

func (r *toolStateRAM) ReplaceTodos(naviID string, items []Todo) ([]Todo, error) {
	st, err := r.ensure(naviID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	next := make([]Todo, 0, len(items))
	for i, item := range items {
		if item.Status == "" {
			item.Status = TodoPending
		}
		if !item.Status.Valid() {
			return nil, todoStatusError(naviID, i, item.Status)
		}
		if item.ID == "" {
			item.ID = NewID()
		}
		item.NaviID = naviID
		item.CreatedAt, item.UpdatedAt = now, now
		next = append(next, item)
	}

	st.mu.Lock()
	st.todos = next
	st.todosDirty = true
	st.mu.Unlock()

	r.syncFlush(naviID)
	return cloneTodos(next), nil
}

func (r *toolStateRAM) UpdateTodos(naviID string, updates []TodoUpdate) ([]Todo, error) {
	if len(updates) == 0 {
		return []Todo{}, nil
	}
	st, err := r.ensure(naviID)
	if err != nil {
		return nil, err
	}

	st.mu.Lock()
	next := cloneTodos(st.todos)
	for i, u := range updates {
		if u.Status != nil && !u.Status.Valid() {
			st.mu.Unlock()
			return nil, todoStatusError(naviID, i, *u.Status)
		}
		idx := indexTodo(next, u.ID)
		if idx < 0 {
			st.mu.Unlock()
			return nil, todoNotFound(naviID, u.ID)
		}
		if u.Remove {
			next = append(next[:idx], next[idx+1:]...)
			continue
		}
		if u.Text != nil {
			next[idx].Text = *u.Text
		}
		if u.Status != nil {
			next[idx].Status = *u.Status
		}
		if u.Priority != nil {
			next[idx].Priority = *u.Priority
		}
		next[idx].UpdatedAt = time.Now().UTC()
	}
	st.todos = next
	st.todosDirty = true
	st.mu.Unlock()

	r.syncFlush(naviID)
	out := cloneTodos(next)
	sortTodos(out)
	return out, nil
}

func (r *toolStateRAM) ReadNotes(naviID string, q NoteQuery) ([]Note, error) {
	st, err := r.ensure(naviID)
	if err != nil {
		return nil, err
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := filterNotes(st.notes, q)
	if out == nil {
		out = []Note{}
	}
	return out, nil
}

func (r *toolStateRAM) UpsertNote(naviID string, n Note) (Note, error) {
	st, err := r.ensure(naviID)
	if err != nil {
		return Note{}, err
	}
	now := time.Now().UTC()

	st.mu.Lock()
	if n.ID == "" {
		n.ID = NewID()
		n.CreatedAt = now
	} else if idx := indexNote(st.notes, n.ID); idx >= 0 {
		n.CreatedAt = st.notes[idx].CreatedAt
	} else {
		n.CreatedAt = now
	}
	n.NaviID = naviID
	n.UpdatedAt = now
	n.Tags = append([]string(nil), n.Tags...)
	if idx := indexNote(st.notes, n.ID); idx >= 0 {
		st.notes[idx] = n
	} else {
		st.notes = append(st.notes, n)
	}
	st.mu.Unlock()

	r.syncFlush(naviID)
	return n, nil
}

func (r *toolStateRAM) RemoveNote(naviID, id string) error {
	st, err := r.ensure(naviID)
	if err != nil {
		return err
	}

	st.mu.Lock()
	idx := indexNote(st.notes, id)
	if idx < 0 {
		st.mu.Unlock()
		return noteNotFound(naviID, id)
	}
	st.notes = append(st.notes[:idx], st.notes[idx+1:]...)
	st.mu.Unlock()

	r.syncFlush(naviID)
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────

func todoStatusError(naviID string, index int, status TodoStatus) error {
	return fmt.Errorf("datastore: todos for %s: item %d has invalid status %q", naviID, index, status)
}

func todoNotFound(naviID, id string) error {
	return fmt.Errorf("datastore: todos for %s: %w (id %s)", naviID, ErrTodoNotFound, id)
}

func noteNotFound(naviID, id string) error {
	return fmt.Errorf("datastore: notes for %s: %w (id %s)", naviID, ErrNoteNotFound, id)
}

func filterTodos(items []Todo, status TodoStatus, limit int) []Todo {
	out := make([]Todo, 0, len(items))
	for _, t := range items {
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, t)
	}
	sortTodos(out)
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

func sortTodos(items []Todo) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

func filterNotes(items []Note, q NoteQuery) []Note {
	out := make([]Note, 0, len(items))
	for _, n := range items {
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
	return out
}

func indexTodo(items []Todo, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func indexNote(items []Note, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func indexNotes(items []Note) map[string]Note {
	out := make(map[string]Note, len(items))
	for _, n := range items {
		out[n.ID] = n
	}
	return out
}

func notesEqual(a, b Note) bool {
	if a.Title != b.Title || a.Body != b.Body || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	return equalTags(a.Tags, b.Tags)
}

func equalTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// notesDifferLocked reports whether the navi's notes differ from what the
// sink last received. Caller holds st.mu.
func notesDifferLocked(st *ramNavi) bool {
	current := indexNotes(st.notes)
	if len(current) != len(st.persistedNotes) {
		return true
	}
	for id, n := range current {
		if prev, ok := st.persistedNotes[id]; !ok || !notesEqual(prev, n) {
			return true
		}
	}
	return false
}

func cloneTodos(in []Todo) []Todo {
	if in == nil {
		return nil
	}
	out := make([]Todo, len(in))
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
