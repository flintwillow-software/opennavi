package thought

import (
	"context"
	"sync"
	"time"
)

// Runner executes one claimed thought to completion (or error). One
// thought ≈ one agent run: the agent-runtime adapter prompts the agent
// with the thought's prompt/context and returns. Runners must respect
// ctx cancellation.
//
// Checkpoint ownership: the runner owns the thought until it returns.
// On ctx cancellation (preemption or Pool.Cancel) the runner SHOULD
// snapshot its progress into t.Checkpoint before returning — the
// worker parks the thought via Pause with that checkpoint, so Resume
// can continue from it. The thought pointer Dequeue hands to the
// runner is the queue's live object; mutations to Checkpoint are
// visible to the worker and preserved by Pause.
type Runner interface {
	Run(ctx context.Context, t *Thought) error
}

// Pool is a bounded worker pool over a ThoughtQueue (spec: one worker
// per thought, isolated processing). Workers claim thoughts via
// Dequeue and run them through the Runner.
type Pool struct {
	queue   *ThoughtQueue
	runner  Runner
	workers int

	wg       sync.WaitGroup
	mu       sync.Mutex
	stop     bool
	stopOnce sync.Once
	wake     chan struct{}
	done     chan struct{} // closed by Stop to release idle workers
	started  bool
	cancels  map[string]context.CancelFunc
	cancelsM sync.Mutex
}

// NewPool creates a worker pool with the given number of workers. The
// pool's concurrency is also capped by the queue's MaxConcurrent slots.
func NewPool(q *ThoughtQueue, runner Runner, workers int) *Pool {
	if workers <= 0 {
		workers = 1
	}
	if workers > q.cfg.MaxConcurrent {
		workers = q.cfg.MaxConcurrent // slots bound concurrency
	}
	return &Pool{
		queue:   q,
		runner:  runner,
		workers: workers,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		cancels: make(map[string]context.CancelFunc),
	}
}

// Start launches the workers.
func (p *Pool) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	p.queue.OnEvent(func(ev QueueEvent) {
		switch ev.Kind {
		case EventEnqueued, EventResumed:
			p.wakeWorker()
		case EventPreempted:
			// Cooperative preemption: cancel the victim's run ctx. The
			// runner snapshots its checkpoint and returns; the worker
			// then parks the thought via Pause.
			p.cancelsM.Lock()
			cancel, ok := p.cancels[ev.ThoughtID]
			p.cancelsM.Unlock()
			if ok {
				cancel()
			}
		}
	})
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// wakeWorker nudges an idle worker.
func (p *Pool) wakeWorker() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Stop stops the pool after the in-flight thought finishes. New claims
// stop immediately.
func (p *Pool) Stop() {
	p.mu.Lock()
	p.stop = true
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.done) }) // broadcast once to all idle workers
}

// Wait blocks until all workers exit.
func (p *Pool) Wait() { p.wg.Wait() }

// StopAndWait stops the pool and waits for workers to exit.
func (p *Pool) StopAndWait() {
	p.Stop()
	p.Wait()
}

// Cancel interrupts the in-flight run of a thought (the runner's ctx
// is cancelled; the worker then parks the thought for resume).
func (p *Pool) Cancel(thoughtID string) {
	p.cancelsM.Lock()
	defer p.cancelsM.Unlock()
	if cancel, ok := p.cancels[thoughtID]; ok {
		cancel()
	}
}

func (p *Pool) worker(n int) {
	defer p.wg.Done()
	for {
		p.mu.Lock()
		stopped := p.stop
		p.mu.Unlock()
		if stopped {
			return
		}

		t, ok := p.queue.Dequeue()
		if !ok {
			// Nothing claimable: wait for a wake signal or stop.
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}

		runCtx, cancel := context.WithCancel(context.Background())
		p.cancelsM.Lock()
		p.cancels[t.ID] = cancel
		p.cancelsM.Unlock()

		err := p.runner.Run(runCtx, t)

		// Capture cancellation BEFORE deferring cancel() — otherwise
		// every failure looks like a cancellation.
		cancelled := runCtx.Err() != nil

		p.cancelsM.Lock()
		delete(p.cancels, t.ID)
		p.cancelsM.Unlock()
		cancel()

		if err != nil {
			if cancelled {
				// Cancelled (preemption or Pool.Cancel): park the
				// thought with the runner's checkpoint for resume.
				if perr := p.queue.Pause(t.ID, t.Checkpoint); perr != nil {
					p.emitPersistError(t.ID, perr)
				}
			} else {
				if ferr := p.queue.Fail(t.ID, err.Error()); ferr != nil {
					p.emitPersistError(t.ID, ferr)
				}
			}
		} else {
			if cerr := p.queue.Complete(t.ID, t.Result); cerr != nil {
				p.emitPersistError(t.ID, cerr)
			}
		}

		// Slot freed — a pending thought may now be claimable.
		p.wakeWorker()
	}
}

// emitPersistError surfaces persistence failures that would otherwise
// be swallowed (the thought's in-memory and stored state can diverge).
func (p *Pool) emitPersistError(thoughtID string, err error) {
	p.queue.emit([]QueueEvent{{Kind: EventPersistFailed, ThoughtID: thoughtID, Detail: err.Error(), At: time.Now().UTC()}})
}
