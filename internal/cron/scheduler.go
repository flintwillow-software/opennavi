package cron

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/internal/thought"
)

// ScheduleDef is one named schedule (developer-authored, navi.yaml
// source of truth — see #239 plan amendment).
type ScheduleDef struct {
	Name     string // unique schedule name
	Expr     string // 5-field cron expression
	Prompt   string // thought prompt / task template
	Priority int    // 1-10 (default 3: cron-triggered, spec §2.3)
	TZ       string // IANA timezone ("" = time.Local)
	Enabled  bool
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
}

type scheduledDef struct {
	def  ScheduleDef
	expr *CronExpr
	loc  *time.Location
	next time.Time
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
	return nil
}

// Remove unregisters a schedule.
func (s *Scheduler) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.schedules, name)
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
// matches the current time. Used by the tick loop and tests.
func (s *Scheduler) fireNow(now time.Time) {
	s.mu.Lock()
	scheds := make([]*scheduledDef, 0, len(s.schedules))
	for _, sd := range s.schedules {
		scheds = append(scheds, sd)
	}
	s.mu.Unlock()

	for _, sd := range scheds {
		if !sd.def.Enabled {
			continue
		}
		if !sd.expr.Matches(now.In(sd.loc)) {
			continue
		}
		t := &thought.Thought{
			ID:        fmt.Sprintf("cron-%s-%s", sd.def.Name, now.UTC().Format("20060102T150405")),
			Prompt:    sd.def.Prompt,
			Priority:  sd.def.Priority,
			Status:    thought.ThoughtPending,
			CreatedAt: now.UTC(),
			UpdatedAt: now.UTC(),
			Metadata:  map[string]string{"schedule": sd.def.Name},
		}
		if err := s.queue.Enqueue(t); err != nil {
			continue
		}
	}
}

func (s *Scheduler) loop() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		var next time.Time
		now := s.clock()
		for _, sd := range s.schedules {
			if !sd.def.Enabled {
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
			s.fireNow(s.clock())
		case <-s.wake:
			timer.Stop() // schedules changed; recompute
		}
	}
}
