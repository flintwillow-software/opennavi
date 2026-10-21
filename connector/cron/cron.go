// Package cron implements the cron connector (ADR 0010): a
// connector.Connector that turns recurring schedule definitions into
// thoughts. Cron is a thought-system primitive — a recurring routine is a
// thought factory — exposed through the connector interface so it arrives
// through the same door as any other inbound transport.
package cron

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
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
	sched *scheduler
	// stateDB persists tick state (last_fired/fire_count) and enables
	// missed-tick coalescing. nil = in-memory only.
	stateDB *sql.DB
	// now overrides the scheduler clock (tests). nil = time.Now.
	now func() time.Time
}

// Option configures the connector.
type Option func(*Connector)

// WithStateStore persists cron tick state (last_fired/fire_count) in the
// shared SQLite connection and enables missed-tick coalescing: a schedule
// whose next-due passed while the navi was down enqueues one coalescing
// thought at Start instead of being skipped. The connector migrates its own
// schedule_state table (migration v6) at Start. Without this option the
// connector keeps state in memory only, and a tick missed across a restart
// is silently lost.
func WithStateStore(db *sql.DB) Option {
	return func(c *Connector) { c.stateDB = db }
}

// New builds the cron connector from its declared schedules.
func New(cfg Config, opts ...Option) *Connector {
	c := &Connector{cfg: cfg}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name identifies the connector in diagnostics and the health view
// (NAVI-329).
func (c *Connector) Name() string { return "scheduler" }

// Start binds the scheduler to the navi's thought system and launches the
// tick loop. Invalid cron expressions or timezones fail Start (and thus
// the navi's run) rather than silently dropping a schedule.
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("cron: thought system is required")
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	s := newScheduler(deps.ThoughtSystem)
	if c.now != nil {
		s.SetClock(c.now)
	}
	for _, def := range c.cfg.Schedules {
		if err := s.Add(scheduleSpec{
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
	if err := c.wireState(deps, s, now); err != nil {
		return err
	}
	s.Start()
	c.sched = s
	return nil
}

// wireState enables tick-state persistence and missed-tick coalescing when a
// state store was supplied. It runs before the scheduler starts: missed ticks
// are coalesced once at Start, and recordFire is wired to the queue's started
// event (the claim boundary), keyed by the schedule named in the thought's
// metadata — a key that never meets missedTick's coalescing rows, which carry
// `missed: true`. With no state store this is a no-op (in-memory only).
func (c *Connector) wireState(deps connector.Deps, s *scheduler, now func() time.Time) error {
	if c.stateDB == nil {
		return nil
	}
	if err := migrateState(c.stateDB); err != nil {
		return err
	}
	state := newStateStore(c.stateDB)

	deps.ThoughtSystem.OnEvent(func(ev thought.Event) {
		if ev.Kind != thought.EventStarted {
			return
		}
		got, err := deps.ThoughtSystem.GetThought(ev.ThoughtID)
		if err != nil {
			return
		}
		name := got.Metadata["schedule"]
		if name == "" {
			return
		}
		_ = state.recordFire(name, now().UTC())
	})

	for _, def := range c.cfg.Schedules {
		if def.Disabled {
			continue
		}
		last, err := state.lastFired(def.Name)
		if err != nil || last.IsZero() {
			continue
		}
		expr, perr := parseExpr(def.Expr)
		if perr != nil {
			continue // s.Add already validated the expression
		}
		if next := expr.Next(last); next.Before(now()) {
			if err := s.missedTick(def.Name); err != nil {
				return fmt.Errorf("cron: missed tick %q: %w", def.Name, err)
			}
		}
	}
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

// Compile-time check: the connector satisfies the public interface.
var _ connector.Connector = (*Connector)(nil)
