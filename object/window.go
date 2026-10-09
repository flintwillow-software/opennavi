package object

import (
	"sync"
	"time"
)

// DeltaWindow is a bounded, cursor-ed window of live deltas. It is the storage
// behind Surface.Watch: producers Push, a consumer reads with ReadAfter, and a
// slow consumer degrades itself (drop-oldest, with a reported gap) rather than
// slowing a producer (ADR 0017 decision 6). ReadAfter is non-destructive, so
// several portals may read the same surface with independent cursors.
//
// It is exported because both the in-process surface (navi) and the remote
// binding (portal/gateway) need the same discipline; one implementation keeps
// the two from drifting.
type DeltaWindow struct {
	mu   sync.Mutex
	ring []deltaEntry
	cap  int
	seq  int           // next sequence to assign (1-based on first push)
	wake chan struct{} // closed (and replaced) on Push; nil until Notify
}

// deltaEntry pairs a delta with the window sequence it was assigned. Delta
// itself carries no cursor, so the window tracks it.
type deltaEntry struct {
	seq   int
	delta Delta
}

// NewDeltaWindow builds a bounded window. A capacity <= 0 uses a small
// default (512), mirroring the observation mailbox.
func NewDeltaWindow(capacity int) *DeltaWindow {
	if capacity <= 0 {
		capacity = 512
	}
	return &DeltaWindow{cap: capacity}
}

// Push appends a delta, assigning its sequence. When the window is full the
// OLDEST delta is dropped, so memory stays bounded. A zero timestamp is
// stamped here.
func (w *DeltaWindow) Push(d Delta) {
	if d.At.IsZero() {
		d.At = time.Now().UTC()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	w.ring = append(w.ring, deltaEntry{seq: w.seq, delta: d})
	if len(w.ring) > w.cap {
		w.ring = w.ring[1:]
	}
	if w.wake != nil {
		close(w.wake)
		w.wake = nil
	}
}

// ReadAfter returns the deltas after the given cursor plus the next cursor to
// use, and whether deltas before the cursor were dropped (lost=true: the
// consumer fell behind the bounded window and must expect a gap).
func (w *DeltaWindow) ReadAfter(after int) ([]Delta, int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.ring) == 0 {
		return []Delta{}, after, false
	}
	first := w.ring[0].seq
	lost := after < first-1
	start := len(w.ring)
	for i, e := range w.ring {
		if e.seq >= after {
			start = i
			break
		}
	}
	out := make([]Delta, 0, len(w.ring)-start)
	for _, e := range w.ring[start:] {
		out = append(out, e.delta)
	}
	if len(out) > 0 {
		return out, w.ring[len(w.ring)-1].seq + 1, lost
	}
	return out, after, lost
}

// Notify returns a channel that is closed when a delta arrives, so a consumer
// can be woken instead of polling. Capture the channel BEFORE ReadAfter: a
// push that lands between the two closes the captured channel, so no wakeup
// is lost.
func (w *DeltaWindow) Notify() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wake == nil {
		w.wake = make(chan struct{})
	}
	return w.wake
}

// Notifier is an optional Surface capability: a surface that can wake a
// consumer when new deltas land. A stream binding uses it instead of polling
// Watch on a timer (ADR 0019 decision 5).
type Notifier interface {
	// Notify returns a channel closed when new deltas may be available.
	Notify() <-chan struct{}
}
