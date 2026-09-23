package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// ── Binary-run support (S010003 §06) ─────────────────────────────────
//
// A navi run without connectors drives the same thought queue
// in-process: the session's current thought occupies a foreground
// slot, all work enters the queue as durable thoughts, and interrupt
// semantics park-with-checkpoint (never kill-and-lose). This file
// holds the two seams that make a binary run work:
//
//   - inFlightAgents: the steering bridge's lookup — maps a running
//     thought id to its in-flight agent so RouteMessage can inject at
//     the turn boundary (S010312 running path, S010310).
//   - RunForeground: the binary-run lifecycle — pool up, wait for the
//     session to end AND the queue to drain, park in-flight work on
//     interrupt (never kill-and-lose), exit.

// inFlightAgents tracks running thoughts' agents for the steering
// bridge. The runner registers each thought's agent when its run
// starts and unregisters when the run returns; RouteMessage's running
// path invokes the router OUTSIDE q.mu, so lookups here never
// deadlock against the queue.
//
// Delivery window: a thought is steerable only while its agent is
// in-flight — a message routed between the queue's claim and the
// runner's registration (or after completion) is rejected by the
// bridge with a clear error; the delivery-ack event surfaces the
// rejection so the portal/session can retry.
type inFlightAgents struct {
	mu     sync.RWMutex
	agents map[string]*agent.Agent
}

func newInFlightAgents() *inFlightAgents {
	return &inFlightAgents{agents: make(map[string]*agent.Agent)}
}

// track registers the agent for a running thought and returns the
// unregister function (deferred by the runner).
func (t *inFlightAgents) track(thoughtID string, a *agent.Agent) (unregister func()) {
	t.mu.Lock()
	t.agents[thoughtID] = a
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		delete(t.agents, thoughtID)
		t.mu.Unlock()
	}
}

// steer injects a message into the thought's in-flight agent (agent
// steering queue, consumed at the next turn boundary — S010310).
// Satisfies thought.SteeringRouter.
func (t *inFlightAgents) steer(thoughtID, msg string) error {
	a, ok := t.lookup(thoughtID)
	if !ok {
		return fmt.Errorf("thought %s has no in-flight agent (already finished or not yet started)", thoughtID)
	}
	a.Steer(msg)
	return nil
}

// lookup returns the in-flight agent for a thought.
func (t *inFlightAgents) lookup(thoughtID string) (*agent.Agent, bool) {
	t.mu.RLock()
	a, ok := t.agents[thoughtID]
	t.mu.RUnlock()
	return a, ok
}

// steeringSnapshot pops the pending steering messages of a thought's
// in-flight agent (observability seam: proves the bridge injected into
// the agent's steering queue). Destructive — the agent loop consumes
// these at the next turn boundary; call only to verify.
func (t *inFlightAgents) steeringSnapshot(thoughtID string) []string {
	a, ok := t.lookup(thoughtID)
	if !ok {
		return nil
	}
	var out []string
	for {
		msg, ok := a.Steering.Next()
		if !ok {
			return out
		}
		out = append(out, msg)
	}
}

// RunForeground drives a binary run (S010003): the same queue, pool,
// and registry-governed tools as the daemon — no connectors. The
// session's current thought occupies a foreground slot (the runtime
// was assembled with RuntimeParams.Foreground). Exits when the session
// ends (sessionDone closed) and the queue has drained, or on ctx
// cancellation — in which case the in-flight foreground thought is
// parked with its checkpoint (never kill-and-lose; the pool's
// StopAndWait drain is the park path, and the runner's
// checkpoint-on-park machinery persists the conversation).
//
// The caller drives input (stdin loop in cmd/navi; tests enqueue
// directly) through Runtime.Queue and Runtime.Queue.RouteMessage.
func (d *Runtime) RunForeground(ctx context.Context, sessionDone <-chan struct{}) error {
	if d.Pool != nil {
		d.Pool.Start()
	}
	defer func() {
		// StopAndWait parks in-flight thoughts with checkpoints —
		// interrupt semantics: park, never kill-and-lose (S010003).
		if d.Pool != nil {
			d.Pool.StopAndWait()
		}
	}()

	// Drained = no claimable/paused work left (pending, running, or
	// paused lists empty — terminal thoughts stay for inspection).
	drained := func() bool {
		for _, t := range d.Queue.GetThoughts() {
			switch t.Status {
			case thought.ThoughtPending, thought.ThoughtRunning, thought.ThoughtPaused:
				return false
			}
		}
		return true
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if d.Logger != nil {
				d.Logger.Info("binary run interrupted — parking in-flight thoughts", nil)
			}
			// Interrupt semantics (S010003 §06): park, never
			// kill-and-lose. Cancel each running thought — the runner
			// snapshots its checkpoint and returns ctx.Err(); the worker
			// parks it via Pause (running → paused). StopAndWait in the
			// defer then drains the parked workers.
			for _, t := range d.Queue.GetThoughts() {
				if t.Status == thought.ThoughtRunning {
					d.Pool.Cancel(t.ID)
				}
			}
			return ctx.Err()
		case <-sessionDone:
			if drained() {
				if d.Logger != nil {
					d.Logger.Info("binary run complete — session ended, queue drained", nil)
				}
				return nil
			}
			// Session over, work still queued: keep processing until
			// the drain completes (a binary run finishes its work).
		case <-ticker.C:
			if d.sessionOver(sessionDone) && drained() {
				if d.Logger != nil {
					d.Logger.Info("binary run complete — session ended, queue drained", nil)
				}
				return nil
			}
		}
	}
}

// sessionOver reports whether the interactive session has ended.
func (d *Runtime) sessionOver(sessionDone <-chan struct{}) bool {
	select {
	case <-sessionDone:
		return true
	default:
		return false
	}
}

// steeringSnapshot pops the pending steering messages of a thought's
// in-flight agent (test/observability seam: proves the bridge
// injected into the agent's steering queue). Destructive — the agent
// loop consumes these at the next turn boundary.
func (d *Runtime) steeringSnapshot(thoughtID string) []string {
	if d.inflight == nil {
		return nil
	}
	return d.inflight.steeringSnapshot(thoughtID)
}
