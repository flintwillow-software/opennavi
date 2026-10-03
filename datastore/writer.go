package datastore

import (
	"sync"
	"time"
)

// DefaultWriteInterval is the ordered writer's async drain cadence.
const DefaultWriteInterval = 250 * time.Millisecond

// Write is one persistence operation submitted to the ordered writer.
//
// Apply runs on the writer goroutine — or inline for a synchronous submit —
// against the active backend, never on a producer's goroutine, so a slow
// or stalled backend cannot block the producer.
type Write struct {
	// Aggregate names the aggregate the write belongs to (diagnostics and
	// capability checks).
	Aggregate Aggregate
	// Key identifies the write for high-water-mark coalescing: a later
	// write with the same key replaces an earlier pending one, so a burst
	// collapses into one write. Keys are unique per logical object
	// ("thoughts/<id>", "transcript/<id>", "toolstate/<navi>").
	Key string
	// Apply performs the write against the active backend.
	Apply func(Backend) error
}

// WriterConfig tunes the ordered writer.
type WriterConfig struct {
	// Interval is the async drain cadence: 0 selects the default, a
	// negative value selects synchronous write-through (Submit applies
	// inline; there is no goroutine).
	Interval time.Duration
	// OnError observes an asynchronous write failure. A failure never
	// blocks a producer; a synchronous Submit returns the error instead.
	OnError func(Write, error)
}

type pendingWrite struct {
	write Write
	done  chan error
}

// Writer is the single serialized, globally ordered persistence writer at
// the datastore level (ADR 0011 decisions 7 and 13). Producers submit
// writes; one goroutine drains them in submission order, coalescing
// pending writes by key. Global ordering is what makes the persisted state
// a prefix of what happened on a crash.
//
// Producers never block: Submit returns immediately. SubmitSync is the
// deliberate exception for durability-critical writes (park checkpoints,
// deletes) — it waits for its write to be applied, ordered after everything
// queued before it.
type Writer struct {
	mu    sync.Mutex
	order []string
	ops   map[string]pendingWrite

	backend Backend

	interval time.Duration
	async    bool
	onError  func(Write, error)

	wake chan struct{}

	// drainMu serializes drains: the ticker/wake goroutine and an explicit
	// Flush or stop-func drain must not apply writes concurrently, or order
	// would be violated.
	drainMu sync.Mutex

	// started is guarded by mu; stop is closed by the stop function.
	started bool
	stop    chan struct{}
}

// NewWriter builds an ordered writer. Bind a backend with SetBackend
// before it drains anything.
func NewWriter(cfg WriterConfig) *Writer {
	interval := cfg.Interval
	async := interval >= 0
	if async && interval == 0 {
		interval = DefaultWriteInterval
	}
	return &Writer{
		ops:      map[string]pendingWrite{},
		interval: interval,
		async:    async,
		onError:  cfg.OnError,
		wake:     make(chan struct{}, 1),
	}
}

// SetBackend points the writer at the active backend. Assembly calls it
// once, after Resolve and Open.
func (w *Writer) SetBackend(b Backend) {
	w.mu.Lock()
	w.backend = b
	w.mu.Unlock()
}

// Submit queues a write and returns immediately — it never touches the
// backend in async mode. In synchronous mode it applies inline. Queued
// writes drain on the writer's cadence once Start is called; before that
// (or in a test) an explicit Flush drains them.
func (w *Writer) Submit(op Write) {
	if !w.async {
		_ = w.applyNow(op)
		return
	}
	w.enqueue(pendingWrite{write: op})
}

// SubmitSync queues a write and waits for it to be applied, preserving
// global order: everything queued before it is applied first. It is the
// durability path — park checkpoints and deletes use it.
func (w *Writer) SubmitSync(op Write) error {
	if !w.async {
		return w.applyNow(op)
	}
	done := make(chan error, 1)
	w.enqueue(pendingWrite{write: op, done: done})
	if w.running() {
		w.signal()
	} else {
		w.drain()
	}
	return <-done
}

// Start launches the drain goroutine and returns a stop function that
// stops it and drains once more. Idempotent; a no-op in synchronous mode.
func (w *Writer) Start() func() {
	if !w.async {
		return func() {}
	}

	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return func() {}
	}
	w.started = true
	stop := make(chan struct{})
	w.stop = stop
	w.mu.Unlock()

	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-w.wake:
				w.drain()
			case <-ticker.C:
				w.drain()
			}
		}
	}()

	return func() {
		w.mu.Lock()
		if !w.started {
			w.mu.Unlock()
			return
		}
		w.started = false
		w.mu.Unlock()
		close(stop)
		w.drain()
	}
}

// Flush drains every pending write now and returns the first error. It is
// the synchronous seam for tests and shutdown; Start's stop already drains.
func (w *Writer) Flush() error { return w.drain() }

// Pending reports how many writes are queued (diagnostics/tests).
func (w *Writer) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.ops)
}

func (w *Writer) running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.async && w.started
}

func (w *Writer) signal() {
	select {
	case w.wake <- struct{}{}:
	default: // a wakeup is already pending
	}
}

func (w *Writer) enqueue(pw pendingWrite) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.ops[pw.write.Key]; dup {
		// High-water mark: the newer write supersedes the pending one, and
		// moves to the end so ordering reflects the latest submission.
		for i, k := range w.order {
			if k == pw.write.Key {
				w.order = append(w.order[:i], w.order[i+1:]...)
				break
			}
		}
	}
	w.ops[pw.write.Key] = pw
	w.order = append(w.order, pw.write.Key)
}

// drain swaps out the pending set and applies it in order, outside the
// lock. A failure is reported through OnError and to a synchronous waiter;
// it never stops the drain (later writes still land, preserving the prefix).
func (w *Writer) drain() error {
	w.drainMu.Lock()
	defer w.drainMu.Unlock()

	w.mu.Lock()
	order := w.order
	ops := w.ops
	backend := w.backend
	w.order = nil
	w.ops = map[string]pendingWrite{}
	w.mu.Unlock()

	var firstErr error
	for _, key := range order {
		pw := ops[key]
		err := ErrUnbound
		if backend != nil {
			err = pw.write.Apply(backend)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if w.onError != nil {
				w.onError(pw.write, err)
			}
			// An asynchronous write retries on the next drain rather than
			// being silently dropped; a synchronous caller gets the error and
			// decides. A nil backend is a pre-Bind misconfiguration, not a
			// transient failure, so it is not re-queued.
			if pw.done == nil && backend != nil {
				w.enqueue(pw)
			}
		}
		if pw.done != nil {
			pw.done <- err
		}
	}
	return firstErr
}

// applyNow applies one write inline (no queue) for the synchronous path.
func (w *Writer) applyNow(op Write) error {
	w.mu.Lock()
	backend := w.backend
	w.mu.Unlock()
	if backend == nil {
		return ErrUnbound
	}
	return op.Apply(backend)
}
