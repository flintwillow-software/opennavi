package navi

import (
	"fmt"
	"sync"

	"github.com/flintwillow-software/opennavi/agent"
)

// inFlightAgents tracks a running thought's agent for the steering bridge
// (S010310): RouteMessage's running path resolves the thought's agent and
// injects the message at the turn boundary. The runner registers each
// thought's agent on run start and unregisters it when the run returns, so
// a message routed outside the run window rejects rather than vanishing.
//
// This is the navi-path counterpart of the daemon's tracker; it exists so
// the composition root keeps the same routing semantics as the reference
// runtime.
type inFlightAgents struct {
	mu     sync.RWMutex
	agents map[string]*agent.Agent
}

func newInFlightAgents() *inFlightAgents {
	return &inFlightAgents{agents: make(map[string]*agent.Agent)}
}

// track registers a's agent for thoughtID and returns the unregister
// function the runner defers.
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

// steer injects msg into the running thought's agent. It is the
// SteeringRouter the queue calls; an unknown thought means the run has
// finished (or has not started), a clear error rather than a silent drop.
func (t *inFlightAgents) steer(thoughtID, msg string) error {
	t.mu.RLock()
	a, ok := t.agents[thoughtID]
	t.mu.RUnlock()
	if !ok {
		return fmt.Errorf("thought %s has no in-flight agent (already finished or not yet started)", thoughtID)
	}
	a.Steer(msg)
	return nil
}
