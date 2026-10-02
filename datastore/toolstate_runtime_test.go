package datastore

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSink is a counting ToolState persistence target, so tests can assert
// that RAM-first reads and writes do not touch the backend until a flush.
type fakeSink struct {
	mu    sync.Mutex
	todos map[string][]Todo
	notes map[string][]Note

	reads  int
	writes int

	failReplace error
}

func newFakeSink() *fakeSink {
	return &fakeSink{todos: map[string][]Todo{}, notes: map[string][]Note{}}
}

func (f *fakeSink) ListTodos(naviID string, status TodoStatus, limit int) ([]Todo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	var out []Todo
	for _, t := range f.todos[naviID] {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	if out == nil {
		out = []Todo{}
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeSink) ReplaceTodos(naviID string, items []Todo) ([]Todo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.failReplace != nil {
		return nil, f.failReplace
	}
	f.todos[naviID] = cloneTodos(items)
	return cloneTodos(items), nil
}

func (f *fakeSink) UpdateTodos(naviID string, updates []TodoUpdate) ([]Todo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	return cloneTodos(f.todos[naviID]), nil
}

func (f *fakeSink) ReadNotes(naviID string, q NoteQuery) ([]Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	out := append([]Note(nil), f.notes[naviID]...)
	if out == nil {
		out = []Note{}
	}
	return out, nil
}

func (f *fakeSink) UpsertNote(naviID string, n Note) (Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	notes := f.notes[naviID]
	for i := range notes {
		if notes[i].ID == n.ID {
			notes[i] = n
			f.notes[naviID] = notes
			return n, nil
		}
	}
	f.notes[naviID] = append(notes, n)
	return n, nil
}

func (f *fakeSink) RemoveNote(naviID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	notes := f.notes[naviID]
	for i := range notes {
		if notes[i].ID == id {
			f.notes[naviID] = append(notes[:i], notes[i+1:]...)
			return nil
		}
	}
	return ErrNoteNotFound
}

func (f *fakeSink) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *fakeSink) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func TestToolStateRAMHydratesOnceAndReadsStayInRAM(t *testing.T) {
	sink := newFakeSink()
	sink.todos["navi-a"] = []Todo{{ID: "t1", NaviID: "navi-a", Text: "seeded", Status: TodoPending}}

	ram := newToolStateRAM(sink, ToolStateConfig{})

	got, err := ram.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "seeded" {
		t.Fatalf("hydration = %+v", got)
	}
	afterHydrate := sink.readCount()
	if afterHydrate != 2 {
		t.Fatalf("hydration reads = %d, want 2 (todos + notes)", afterHydrate)
	}

	// Mutate the sink behind the layer: RAM is authoritative, so the read
	// does not change and no further backend read happens.
	sink.todos["navi-a"] = append(sink.todos["navi-a"], Todo{ID: "t2", NaviID: "navi-a", Text: "behind"})
	again, err := ram.ListTodos("navi-a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("RAM must stay authoritative after hydration, got %+v", again)
	}
	if sink.readCount() != afterHydrate {
		t.Fatalf("a hydrated read must not touch the backend: reads = %d", sink.readCount())
	}
}

func TestToolStateRAMDefersWritesUntilFlush(t *testing.T) {
	sink := newFakeSink()
	ram := newToolStateRAM(sink, ToolStateConfig{})

	stored, err := ram.ReplaceTodos("navi-a", []Todo{{Text: "one", Priority: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ram.UpsertNote("navi-a", Note{Title: "n", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if sink.writeCount() != 0 {
		t.Fatalf("mutations must not write through, writes = %d", sink.writeCount())
	}

	ram.flush()
	if sink.writeCount() == 0 {
		t.Fatal("flush must persist the pending state")
	}
	if got := sink.todos["navi-a"]; len(got) != 1 || got[0].ID != stored[0].ID {
		t.Fatalf("flushed todos = %+v, want the RAM list", got)
	}
	if got := sink.notes["navi-a"]; len(got) != 1 {
		t.Fatalf("flushed notes = %+v", got)
	}

	// A second flush with no changes writes nothing more.
	before := sink.writeCount()
	ram.flush()
	if sink.writeCount() != before {
		t.Fatalf("a clean flush must be a no-op: writes %d -> %d", before, sink.writeCount())
	}
}

func TestToolStateRAMSynchronousMode(t *testing.T) {
	sink := newFakeSink()
	ram := newToolStateRAM(sink, ToolStateConfig{FlushInterval: -1})

	if _, err := ram.ReplaceTodos("navi-a", []Todo{{Text: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	if sink.writeCount() == 0 {
		t.Fatal("synchronous mode must write through immediately")
	}
	if got := sink.todos["navi-a"]; len(got) != 1 {
		t.Fatalf("sink todos = %+v", got)
	}
}

func TestToolStateRAMFailureRemarksDirtyAndRetries(t *testing.T) {
	sink := newFakeSink()
	sink.failReplace = errors.New("disk on fire")
	var seen []error
	ram := newToolStateRAM(sink, ToolStateConfig{OnError: func(err error) { seen = append(seen, err) }})

	if _, err := ram.ReplaceTodos("navi-a", []Todo{{Text: "must survive"}}); err != nil {
		t.Fatal(err)
	}
	ram.flush()
	if len(seen) != 1 {
		t.Fatalf("flush failure must reach OnError, got %v", seen)
	}

	// The navi stays dirty; a later successful flush persists it.
	sink.failReplace = nil
	ram.flush()
	if got := sink.todos["navi-a"]; len(got) != 1 || got[0].Text != "must survive" {
		t.Fatalf("retry did not persist: %+v", got)
	}
	if len(seen) != 1 {
		t.Fatalf("a successful retry must not report another failure: %v", seen)
	}
}

func TestToolStateRAMFlushesNoteRemoval(t *testing.T) {
	sink := newFakeSink()
	ram := newToolStateRAM(sink, ToolStateConfig{})

	note, err := ram.UpsertNote("navi-a", Note{Title: "temp", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	ram.flush()
	if len(sink.notes["navi-a"]) != 1 {
		t.Fatalf("note not flushed: %+v", sink.notes["navi-a"])
	}

	if err := ram.RemoveNote("navi-a", note.ID); err != nil {
		t.Fatal(err)
	}
	ram.flush()
	if len(sink.notes["navi-a"]) != 0 {
		t.Fatalf("removal not flushed: %+v", sink.notes["navi-a"])
	}
}

// fakeBackend is a Backend whose only real aggregate is tool state, so the
// handle's RAM-first wrapping can be tested end to end.
type fakeBackend struct{ sink *fakeSink }

func (b fakeBackend) Thoughts() Thoughts   { return nil }
func (b fakeBackend) ToolState() ToolState { return b.sink }
func (b fakeBackend) Memory() Memory       { return nil }

func TestHandleServesRAMFirstToolState(t *testing.T) {
	sink := newFakeSink()
	h := NewDatastore()
	h.Bind(fakeBackend{sink: sink})

	stored, err := h.Stateful().ReplaceTodos("navi-a", []Todo{{Text: "ram first"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID == "" {
		t.Fatalf("ReplaceTodos = %+v", stored)
	}
	if sink.writeCount() != 0 {
		t.Fatalf("the handle's tool-state view must not write through, writes = %d", sink.writeCount())
	}

	h.FlushToolState()
	if got := sink.todos["navi-a"]; len(got) != 1 {
		t.Fatalf("FlushToolState did not persist: %+v", got)
	}

	// The flusher is startable and stoppable; stopping drains once more.
	stop := h.StartToolStateFlusher()
	if _, err := h.Stateful().UpsertNote("navi-a", Note{Title: "flushed by ticker"}); err != nil {
		t.Fatal(err)
	}
	stop()
	if len(sink.notes["navi-a"]) != 1 {
		t.Fatalf("stop must drain pending writes: %+v", sink.notes["navi-a"])
	}
}

func TestHandleFlusherIsNoopBeforeBind(t *testing.T) {
	h := NewDatastore()
	stop := h.StartToolStateFlusher()
	stop() // must not panic
	h.FlushToolState()
}

func TestToolStateRAMFlusherTicks(t *testing.T) {
	sink := newFakeSink()
	ram := newToolStateRAM(sink, ToolStateConfig{FlushInterval: 5 * time.Millisecond})
	stop := ram.startFlusher()
	defer stop()

	if _, err := ram.ReplaceTodos("navi-a", []Todo{{Text: "tick"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for sink.writeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.writeCount() == 0 {
		t.Fatal("the flusher never ticked")
	}
}
