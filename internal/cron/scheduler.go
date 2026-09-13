package cron

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
)

// ScheduleDef is one named schedule. Schedule definitions are
// deployment-time runtime config (S000200 §04); the compiled navi
// definition declares only whether the cron interface exists.
type ScheduleDef struct {
	Name     string // unique schedule name
	Expr     string // 5-field cron expression
	Prompt   string // thought prompt / task template
	Priority int    // 1-10 (default 3: cron-triggered, spec §2.3)
	TZ       string // IANA timezone ("" = time.Local)
	// Disabled schedules are skipped by the tick loop. The definition
	// layer uses *bool (nil = enabled); the scheduler normalizes to a
	// flat flag.
	Disabled bool
}

// Enqueuer is what the scheduler feeds (agent-agnostic: implemented by
// the thought queue).
type Enqueuer interface {
	Enqueue(t *thought.Thought) error
}

// clockFunc returns the current time. Overridable in tests.
type clockFunc func() time.Time

// Scheduler fires named schedules on their cron expressions and
// enqueues thoughts on the queue (spec: cron-triggered tasks create
// thoughts with cron-defined priority).
type Scheduler struct {
	mu        sync.Mutex
	schedules map[string]*scheduledDef
	queue     Enqueuer
	clock     clockFunc
	wake      chan struct{}
	done      chan struct{}
	wg        sync.WaitGroup
	running   bool

	idSeq         uint64                                  // monotonic component for collision-proof thought IDs
	errorHandlers []func(error)                           // tick-loop error observers
	eventHandlers []func(thought.QueueEvent)              // queue event observers (RecordFire wiring)
	debugFn       func(msg string, fields map[string]any) // debug-level observer
}

type scheduledDef struct {
	def  ScheduleDef
	expr *CronExpr
	loc  *time.Location
}

// NewScheduler builds a scheduler over the given enqueuer.
func NewScheduler(q Enqueuer) *Scheduler {
	return &Scheduler{
		schedules: make(map[string]*scheduledDef),
		queue:     q,
		clock:     time.Now,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
}

// SetClock overrides the time source (tests).
func (s *Scheduler) SetClock(fn clockFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = fn
}

// OnError registers an observer for tick-loop errors (e.g. failed
// enqueues). Called synchronously from the tick loop.
func (s *Scheduler) OnError(fn func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorHandlers = append(s.errorHandlers, fn)
}

// emitError fans a tick-loop error out to registered observers.
func (s *Scheduler) emitError(err error) {
	s.mu.Lock()
	handlers := append([]func(error){}, s.errorHandlers...)
	s.mu.Unlock()
	for _, fn := range handlers {
		fn(err)
	}
}

// Add registers a schedule definition. Errors on invalid cron
// expressions, invalid timezones, duplicate names.
func (s *Scheduler) Add(def ScheduleDef) error {
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("cron: schedule name is required")
	}
	if strings.TrimSpace(def.Prompt) == "" {
		return fmt.Errorf("cron: schedule %q: prompt is required", def.Name)
	}
	if def.Priority == 0 {
		def.Priority = 3 // cron-triggered, spec §2.3
	}
	expr, err := Parse(def.Expr)
	if err != nil {
		return err
	}
	loc := time.Local
	if def.TZ != "" {
		loc, err = time.LoadLocation(def.TZ)
		if err != nil {
			return fmt.Errorf("cron: schedule %q: timezone %q: %w", def.Name, def.TZ, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.schedules[def.Name]; exists {
		return fmt.Errorf("cron: duplicate schedule %q", def.Name)
	}
	s.schedules[def.Name] = &scheduledDef{def: def, expr: expr, loc: loc}

	// Schedule changes take effect without waiting out the current
	// timer.
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Remove unregisters a schedule and signals the loop.
func (s *Scheduler) Remove(name string) {
	s.mu.Lock()
	delete(s.schedules, name)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start launches the tick loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	s.wg.Add(1)
	go s.loop()
}

// Stop halts the tick loop and waits for it to exit.
func (s *Scheduler) Stop() {
	close(s.done)
	s.wg.Wait()
}

// fireNow enqueues thoughts for every schedule whose expression
// matches the current time (in the schedule's location). Returns the
// first enqueue error (never silently swallowed — a swallowed enqueue
// error is a lost scheduled task with zero trace). Used by the tick
// loop and tests.
func (s *Scheduler) fireNow(now time.Time) error {
	s.mu.Lock()
	scheds := make([]*scheduledDef, 0, len(s.schedules))
	for _, sd := range s.schedules {
		scheds = append(scheds, sd)
	}
	s.mu.Unlock()

	for _, sd := range scheds {
		if sd.def.Disabled {
			continue
		}
		if !sd.expr.Matches(now.In(sd.loc)) {
			continue
		}
		// Collision-proof ID: wall-second + monotonic counter (fireNow
		// and MissedTick can hit the same wall second, exactly the
		// crash-recovery scenario).
		id := fmt.Sprintf("cron-%s-%s-%d", sd.def.Name, now.UTC().Format("20060102T150405"), atomic.AddUint64(&s.idSeq, 1))
		t := &thought.Thought{
			ID:        id,
			Prompt:    sd.def.Prompt,
			Priority:  sd.def.Priority,
			Status:    thought.ThoughtPending,
			CreatedAt: now.UTC(),
			UpdatedAt: now.UTC(),
			Metadata:  map[string]string{"schedule": sd.def.Name},
		}
		if err := s.queue.Enqueue(t); err != nil {
			return fmt.Errorf("cron: enqueue %s: %w", id, err)
		}
		s.dbg("tick fired", map[string]any{"schedule": sd.def.Name, "thought": id})
	}
	return nil
}

func (s *Scheduler) loop() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		var next time.Time
		now := s.clock()
		for _, sd := range s.schedules {
			if sd.def.Disabled {
				continue
			}
			if n := sd.expr.Next(now); n.Before(next) || next.IsZero() {
				next = n
			}
		}
		s.mu.Unlock()

		var wait time.Duration
		if next.IsZero() {
			wait = time.Minute // nothing scheduled; poll for additions
		} else {
			wait = next.Sub(s.clock())
			if wait < 0 {
				wait = 0
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-s.done:
			timer.Stop()
			return
		case <-timer.C:
			// Note: loop() calls Next() under s.mu (up to ~2.6M-iteration
			// scan for pathological exprs) — fine at MVP scale.
			if err := s.fireNow(s.clock()); err != nil {
				s.emitError(err)
			}
		case <-s.wake:
			timer.Stop() // schedules changed; recompute
		}
	}
}

// OnEvent registers an observer for queue events (e.g. RecordFire
// wiring in the daemon: started events advance the schedule state).
func (s *Scheduler) OnEvent(fn func(thought.QueueEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventHandlers = append(s.eventHandlers, fn)
}

// emitQueueEvent fans a queue event out to registered observers.
func (s *Scheduler) emitQueueEvent(ev thought.QueueEvent) {
	s.mu.Lock()
	handlers := append([]func(thought.QueueEvent){}, s.eventHandlers...)
	s.mu.Unlock()
	for _, fn := range handlers {
		fn(ev)
	}
}

// OnDebug registers an observer for debug-level scheduler events
// (tick fires, MissedTick coalescing). Called synchronously.
func (s *Scheduler) OnDebug(fn func(msg string, fields map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.debugFn = fn
}

// dbg fires a debug-level event if a debug observer is registered.
func (s *Scheduler) dbg(msg string, fields map[string]any) {
	if s.debugFn != nil {
		s.debugFn(msg, fields)
	}
}
