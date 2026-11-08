package navi

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// ── short-term memory cleanup (ADR 0039 decisions 4-8) ──────────────────
//
// The always-injected short-term tier is bounded, and the bound is enforced
// at the write: a write that would push a scope over budget is refused, while
// removals and long-term transitions are never gated (decision 1). That
// leaves the navi needing to know WHEN to clean up. This file is the
// mechanism that tells it.
//
// Two triggers, one mechanism (decision 8):
//
//   - OVER BUDGET — a refused write, or a hydrate of a persisted set that
//     exceeds its budget, schedules a cleanup whose brief is "bring the tier
//     back under budget" (decision 4).
//   - PLAN CLOSED — when the plan-close mechanism (ADR 0016) determines a
//     plan genuinely drained, a cleanup is queued alongside the closing check,
//     depending on the plan's sinks, whose brief is "move what this chain
//     produced into long-term memory and remove the working state it no longer
//     needs" (decision 7).
//
// Both queue a self-scheduled thought at p1 (the steering band: cancelling a
// running thought to do bookkeeping is backwards) and both are recorded in
// metadata so the transcript says why they exist. A cleanup thought that
// leaves the tier still over budget is a logged warning, not another thought:
// the no-recursion rule (decision 5) is what makes scheduling-as-enforcement
// safe.

// cleanupIDPrefix marks thoughts the navi synthesized to clean up memory.
const cleanupIDPrefix = "cleanup-"

// cleanup is the runtime's short-term memory cleanup scheduler. It batches
// over-budget notifications and completed thoughts and sweeps them on its own
// goroutine — never on the emitting one, because the queue emits some events
// (Complete among them) while holding its own lock, so a listener that
// enqueued synchronously would deadlock.
type cleanup struct {
	navi *Navi

	mu sync.Mutex
	// overBudget holds scopes found over budget since the last sweep.
	overBudget map[datastore.Scope]bool
	// completed holds thought ids completed since the last sweep, for the
	// plan-closed trigger.
	completed map[string]bool
	wake      chan struct{}
}

func newCleanup(n *Navi) *cleanup {
	return &cleanup{
		navi:       n,
		overBudget: map[datastore.Scope]bool{},
		completed:  map[string]bool{},
		wake:       make(chan struct{}, 1),
	}
}

// signalOverBudget records a scope found over budget. It never blocks and
// never touches the queue (it runs from the memory service, which may be on
// the write path).
func (c *cleanup) signalOverBudget(scope datastore.Scope) {
	c.mu.Lock()
	c.overBudget[scope] = true
	c.mu.Unlock()
	c.wakeup()
}

// signalCompleted records a completed thought for the plan-closed trigger.
func (c *cleanup) signalCompleted(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	c.completed[id] = true
	c.mu.Unlock()
	c.wakeup()
}

func (c *cleanup) wakeup() {
	select {
	case c.wake <- struct{}{}:
	default: // a sweep is already pending; it will see this work
	}
}

// run sweeps until the run's context is cancelled.
func (c *cleanup) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			c.sweep()
		}
	}
}

func (c *cleanup) sweep() {
	c.mu.Lock()
	scopes := make([]datastore.Scope, 0, len(c.overBudget))
	for s := range c.overBudget {
		scopes = append(scopes, s)
	}
	ids := make([]string, 0, len(c.completed))
	for id := range c.completed {
		ids = append(ids, id)
	}
	c.overBudget = map[datastore.Scope]bool{}
	c.completed = map[string]bool{}
	c.mu.Unlock()

	sort.Slice(scopes, func(i, j int) bool { return scopes[i] < scopes[j] })
	sort.Strings(ids) // deterministic within a sweep

	snapshot := c.navi.Thoughts.GetThoughts()
	for _, scope := range scopes {
		c.scheduleOverBudget(scope, snapshot)
	}
	for _, id := range ids {
		c.schedulePlanClosed(id, snapshot)
	}
}

// scheduleOverBudget queues a cleanup for a scope that is over budget, unless
// one is already queued for it. The thought's brief is to REDUCE the tier
// (decision 8): clear what is known stale, prefer removal and long-term
// transition over rewriting — rewriting is not reducing, and its additions
// count against the budget like any other write.
func (c *cleanup) scheduleOverBudget(scope datastore.Scope, snapshot []*thought.Thought) {
	id := cleanupIDPrefix + "over-" + string(scope)
	if cleanupQueued(snapshot, id) {
		return
	}
	used, budget := c.navi.shortMemory.Usage(scope)
	prompt := fmt.Sprintf(
		"Short-term memory is over budget and new notes are being refused.\n\n"+
			"The %s scope holds %d tokens against a budget of %d. Bring it back under budget.\n\n"+
			"Remove notes that are no longer true or no longer needed, and move what matters but is "+
			"too large or too situational to carry in every thought into long-term memory with ltm_write "+
			"(leave a short pointer if the tier should still know it exists). Prefer REMOVING and MOVING "+
			"over rewriting: a rewritten note still counts against the budget, and its additions can be "+
			"refused just like any other write. Do not schedule another cleanup — this one either brings "+
			"the tier under budget or records why it could not.",
		scope, used, budget)

	c.enqueue(scope, id, thought.CleanupOverBudget, prompt, nil, snapshot)
}

// schedulePlanClosed queues a cleanup when a plan drained, depending on the
// plan's sinks (decision 7). Its brief is different from the over-budget one:
// the chain's residue, not the whole tier. It hangs off plan-close rather than
// the verification thought because a verification can fail and schedule more
// work, so a cleanup depending on it would run against an unfinished plan.
func (c *cleanup) schedulePlanClosed(id string, snapshot []*thought.Thought) {
	var subject *thought.Thought
	for _, t := range snapshot {
		if t.ID == id {
			subject = t
			break
		}
	}
	if subject == nil || subject.Status != thought.ThoughtComplete {
		return
	}
	// A cleanup is not a plan: never chain a cleanup off a cleanup, and never
	// close a verification with a cleanup.
	if subject.IsMemoryCleanup() || subject.IsVerification() {
		return
	}

	plan := thought.Plan(subject, snapshot)
	stages := make([]*thought.Thought, 0, len(plan))
	for _, m := range plan {
		if !m.IsVerification() && !m.IsMemoryCleanup() {
			stages = append(stages, m)
		}
	}
	if len(stages) < 2 {
		return // a standalone thought is not a plan
	}
	if !thought.PlanSucceeded(stages) {
		return // still running, or the plan did not succeed
	}
	sinks := thought.PlanSinks(stages)
	if len(sinks) == 0 {
		return
	}

	planID := planID(sinks)
	cleanupID := cleanupIDPrefix + "plan-" + planID
	if cleanupQueued(snapshot, cleanupID) {
		return
	}

	prompt := "This plan of thoughts has drained. Its working state does not need to outlive it.\n\n" +
		"Review the short-term memory this chain wrote as it went: move what it produced and is worth " +
		"keeping into long-term memory with ltm_write, and remove the working state that produced it. " +
		"Notes that are still needed by work in flight stay where they are. Do not schedule another " +
		"cleanup — this one either tidies the chain's residue or records why it could not."

	c.enqueue(datastore.ScopeSession, cleanupID, thought.CleanupPlanClosed, prompt, sinks, snapshot)
}

// cleanupQueued reports whether a cleanup with this id is already in the
// snapshot (pending or running), so a sweep cannot duplicate it.
func cleanupQueued(snapshot []*thought.Thought, id string) bool {
	for _, t := range snapshot {
		if t.ID == id {
			return true
		}
	}
	return false
}

// enqueue builds and queues a cleanup thought. It sets DependsOn for the
// plan-closed trigger so the cleanup runs after the plan's sinks; the
// over-budget trigger has no dependency (it must run promptly).
func (c *cleanup) enqueue(scope datastore.Scope, id, trigger, prompt string, dependsOn []string, snapshot []*thought.Thought) {
	if c.navi.Thoughts == nil {
		return
	}
	now := time.Now().UTC()
	ct := &thought.Thought{
		ID:        id,
		Prompt:    prompt,
		Priority:  thought.SelfScheduledDefault, // p1: housekeeping steers, it does not cancel
		Status:    thought.ThoughtPending,
		DependsOn: dependsOn,
		Skills:    []string{"core_memory"},
		Metadata: map[string]string{
			thought.MetadataOrigin:         thought.OriginSelf,
			thought.MetadataRole:           thought.RoleMemoryCleanup,
			thought.MetadataCleanupScope:   string(scope),
			thought.MetadataCleanupTrigger: trigger,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.navi.Thoughts.Enqueue(ct); err != nil {
		// A duplicate id (another sweep won the race) or a rejected edge is
		// not worth failing the run over; the tier simply stays over budget
		// until the next trigger.
		c.navi.log.Debug("memory cleanup: thought not queued", "id", id, "err", err)
		return
	}
	c.navi.log.Info("memory cleanup scheduled", "scope", scope, "trigger", trigger, "id", id)
}
