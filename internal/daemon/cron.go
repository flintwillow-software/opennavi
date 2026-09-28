package daemon

import (
	"database/sql"
	"time"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/thought"
)

// WireCron applies the #242 documented seam: MigrateState (tick-state
// table), Scheduler with runtime-config-derived schedule defs
// (.config/navi/), MissedTick coalescing for schedules whose
// next_due passed while down, and RecordFire wiring so every started
// thought advances the schedule's last_fired/fire_count.
//
// Returns the (unstarted) scheduler — the caller starts it and calls
// Stop on shutdown.
func WireCron(db *sql.DB, cc *config.CronConfig, q *thought.Queue) (*cron.Scheduler, error) {
	if err := cron.MigrateState(db); err != nil {
		return nil, err
	}
	state := cron.NewStateStore(db)
	sched := cron.NewScheduler(q)

	// RecordFire wiring: the queue's started event is the claim boundary
	// (worker picks up thought → running). Schedule mapping via
	// thought.Metadata["schedule"] (set at fire time by the cron
	// scheduler) — keying by thought ID never meets MissedTick, which
	// reads LastFired(schedule-name).
	q.OnEvent(func(ev thought.Event) {
		if ev.Kind != thought.EventStarted {
			return
		}
		got, gerr := q.GetThought(ev.ThoughtID)
		if gerr != nil {
			return
		}
		schedule := got.Metadata["schedule"]
		if schedule == "" {
			return
		}
		_ = state.RecordFire(schedule, time.Now().UTC())
	})

	if cc == nil {
		cc = &config.CronConfig{}
	}
	for _, sd := range cc.ToSchedulerDefs() {
		if err := sched.Add(sd); err != nil {
			return nil, err
		}
		// MissedTick coalescing: schedules whose next_due passed while
		// down enqueue one coalescing thought at startup.
		last, lerr := state.LastFired(sd.Name)
		if lerr != nil || last.IsZero() {
			continue
		}
		ce, perr := cron.Parse(sd.Expr)
		if perr != nil {
			continue
		}
		if next := ce.Next(last); next.Before(time.Now()) {
			if err := sched.MissedTick(sd.Name); err != nil {
				return nil, err
			}
		}
	}
	return sched, nil
}
