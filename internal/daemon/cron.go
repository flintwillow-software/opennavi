package daemon

import (
	"database/sql"
	"time"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// WireCron applies the #242 documented seam: MigrateState (tick-state
// table), Scheduler with definition-derived schedule defs (navi.yaml
// source of truth), MissedTick coalescing for schedules whose
// next_due passed while down, and RecordFire wiring so every started
// thought advances the schedule's last_fired/fire_count.
//
// Returns the (unstarted) scheduler — the caller starts it and calls
// Stop on shutdown.
func WireCron(db *sql.DB, defs config.DefinitionCronConfig, q *thought.ThoughtQueue) (*cron.Scheduler, error) {
	if err := cron.MigrateState(db); err != nil {
		return nil, err
	}
	state := cron.NewStateStore(db)
	sched := cron.NewScheduler(q)

	// RecordFire wiring: the queue's started event is the claim
	// boundary (spec §3.2 worker picks up thought → running).
	q.OnEvent(func(ev thought.QueueEvent) {
		if ev.Kind == thought.EventStarted {
			_ = state.RecordFire(ev.ThoughtID, time.Now().UTC())
		}
	})

	for _, sd := range defs.ToSchedulerDefs() {
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
