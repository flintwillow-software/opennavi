package daemon

import (
	"github.com/flintwillow-software/opennavi/internal/cron"
	"github.com/flintwillow-software/opennavi/internal/ops/logging"
	"github.com/flintwillow-software/opennavi/thought"
)

// WireLogging connects the structured logger to the thought queue and
// cron scheduler event fanouts (per-thought claim/complete/error/park
// lines, scheduler debug at debug level, shutdown info).
func WireLogging(logger *logging.Logger, q *thought.Queue, sched *cron.Scheduler) {
	q.OnEvent(func(ev thought.Event) {
		enrich := func(extra map[string]any) map[string]any {
			m := map[string]any{"thought": ev.ThoughtID}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		switch ev.Kind {
		case thought.EventStarted:
			logger.Info("thought claimed", enrich(nil))
		case thought.EventCompleted:
			logger.Info("thought complete", enrich(nil))
		case thought.EventFailed:
			logger.Error("thought failed", enrich(map[string]any{"error": ev.Detail}))
		case thought.EventPaused:
			logger.Info("thought parked", enrich(nil))
		case thought.EventResumed:
			logger.Info("thought resumed", enrich(nil))
		case thought.EventCancelled:
			logger.Warn("thought cancelled", enrich(nil))
		case thought.EventPreempted:
			logger.Info("thought preempted", enrich(map[string]any{"detail": ev.Detail}))
		case thought.EventPersistFailed:
			logger.Error("persist failed", enrich(map[string]any{"error": ev.Detail}))
		}
	})
	if sched != nil {
		sched.OnError(func(err error) {
			logger.Error("scheduler error", map[string]any{"error": err.Error()})
		})
		sched.OnDebug(func(msg string, fields map[string]any) {
			logger.Debug(msg, fields)
		})
	}
}
