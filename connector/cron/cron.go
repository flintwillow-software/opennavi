// Package cron implements the cron connector (ADR 0010): a
// connector.Connector that turns recurring schedule definitions into
// thoughts. Cron is a thought-system primitive — a recurring routine is a
// thought factory — exposed through the connector interface so it arrives
// through the same door as any other inbound transport.
package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	internalcron "github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/thought"
)

// Schedule is one recurring routine: a 5-field cron expression and the
// prompt it enqueues.
type Schedule struct {
	// Name uniquely identifies the schedule.
	Name string
	// Expr is a 5-field cron expression.
	Expr string
	// Prompt is the thought prompt the schedule enqueues.
	Prompt string
	// Priority is the enqueued thought's priority (1-10; 0 = 3).
	Priority int
	// TZ is the IANA timezone ("" = time.Local).
	TZ string
	// Disabled schedules are registered but never fire.
	Disabled bool
}

// Config configures the connector. Schedules are compiled in (the navi
// declares them); runtime config may narrow them later.
type Config struct {
	Schedules []Schedule
}

// Connector brings scheduled work into a navi: Start launches a scheduler
// bound to the navi's thought system, Stop halts it.
type Connector struct {
	cfg   Config
	sched *internalcron.Scheduler
	// now overrides the scheduler clock (tests). nil = time.Now.
	now func() time.Time
}

// New builds the cron connector from its declared schedules.
func New(cfg Config) *Connector { return &Connector{cfg: cfg} }

// Start binds the scheduler to the navi's thought system and launches the
// tick loop. Invalid cron expressions or timezones fail Start (and thus
// the navi's run) rather than silently dropping a schedule.
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("cron: thought system is required")
	}
	s := internalcron.NewScheduler(enqueuer{ts: deps.ThoughtSystem})
	if c.now != nil {
		s.SetClock(c.now)
	}
	for _, def := range c.cfg.Schedules {
		if err := s.Add(internalcron.ScheduleDef{
			Name:     def.Name,
			Expr:     def.Expr,
			Prompt:   def.Prompt,
			Priority: def.Priority,
			TZ:       def.TZ,
			Disabled: def.Disabled,
		}); err != nil {
			return fmt.Errorf("cron: %w", err)
		}
	}
	s.OnError(func(err error) {
		if deps.Log != nil {
			deps.Log.Error("cron: enqueue failed", "err", err)
		}
	})
	s.Start()
	c.sched = s
	return nil
}

// Stop halts the tick loop. Safe to call more than once.
func (c *Connector) Stop() error {
	if c.sched != nil {
		c.sched.Stop()
		c.sched = nil
	}
	return nil
}

// enqueuer adapts the component-facing thought system onto the
// scheduler's Enqueue interface.
type enqueuer struct{ ts thought.ThoughtSystem }

func (e enqueuer) Enqueue(t *thought.Thought) error { return e.ts.Enqueue(t) }

// Compile-time check: the connector satisfies the public interface.
var _ connector.Connector = (*Connector)(nil)
