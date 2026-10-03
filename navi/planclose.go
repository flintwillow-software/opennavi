package navi

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// ── closing a plan (ADR 0016) ───────────────────────────────────────────
//
// A plan of thoughts (ADR 0013 decision 1) is not trustworthy until
// something checked what it produced. The first end-to-end multi-thought
// run showed the failure mode plainly: the model noticed an inaccuracy at
// the end of a stage and simply moved on. ADR 0016 splits the job —
// `core_thoughts` tells the model to end a multi-stage plan with a
// verification thought (which is where the task-specific check belongs:
// run the tests, read the file back, diff the output), and this file is
// the runtime's guarantee when it does not.
//
// The rule is mechanical rather than heuristic:
//
//   - the plan is the dependency-connected component of the completed
//     thought, so no plan object is needed (NAVI-378's graph is the plan);
//   - every member must be complete — a failed or cancelled member means
//     the plan did not succeed, and its dependents were already failed;
//   - a plan containing a verification is left alone, so a check never
//     spawns another check;
//   - the synthesized check depends on the plan's sinks, so it releases
//     only once the whole plan drained (parallel branches included);
//   - its id derives from those sinks, so a second completion event, a
//     replayed event, or a restart cannot queue a duplicate.

// verifyIDPrefix marks thoughts the navi synthesized to close a plan.
const verifyIDPrefix = "verify-"

// planClose is the closing-check listener: it batches completed-thought
// ids and sweeps them on its own goroutine.
//
// It must NOT run on the emitting goroutine: the queue emits some events
// while holding its own lock (Complete among them), so a listener that
// read a snapshot and enqueued synchronously from the event callback
// would deadlock. signal() only records and wakes.
type planClose struct {
	navi *Navi

	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
}

func newPlanClose(n *Navi) *planClose {
	return &planClose{navi: n, pending: make(map[string]bool), wake: make(chan struct{}, 1)}
}

// signal records a completed thought for the next sweep. It never blocks
// and never touches the queue.
func (p *planClose) signal(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	p.pending[id] = true
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default: // a sweep is already pending; it will see this id
	}
}

// run sweeps until the run's context is cancelled.
func (p *planClose) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.sweep()
		}
	}
}

// sweep closes every plan whose completion was signalled. Each candidate
// re-reads the snapshot, so a check queued earlier in the same batch is
// visible to the dedupe that follows it.
func (p *planClose) sweep() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pending = make(map[string]bool)
	p.mu.Unlock()

	sort.Strings(ids) // deterministic within a sweep
	for _, id := range ids {
		p.closeOne(id, p.navi.Thoughts.GetThoughts())
	}
}

// closeOne queues a verification for the plan the completed thought
// belongs to, unless the plan is not a plan, not succeeded, or already
// checked.
func (p *planClose) closeOne(id string, snapshot []*thought.Thought) {
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

	plan := thought.Plan(subject, snapshot)
	if len(plan) < 2 {
		return // a standalone thought is not a plan
	}
	for _, m := range plan {
		if m.IsVerification() {
			return // the plan already closes with a check
		}
	}
	if !thought.PlanSucceeded(plan) {
		return // still running, or the plan did not succeed
	}
	sinks := thought.PlanSinks(plan)
	if len(sinks) == 0 {
		return // every member is depended on: not a shape we can close
	}

	planID := planID(sinks)
	for _, t := range snapshot {
		if t.ID == verifyIDPrefix+planID {
			return // already queued, or already ran
		}
	}

	now := time.Now().UTC()
	check := &thought.Thought{
		ID:        verifyIDPrefix + planID,
		Prompt:    planClosePrompt(plan),
		Priority:  thought.SelfScheduledHighBound, // the conservative band (Enqueue clamps too)
		Status:    thought.ThoughtPending,
		DependsOn: sinks,
		Metadata: map[string]string{
			thought.MetadataOrigin: thought.OriginSelf,
			thought.MetadataRole:   thought.RoleVerification,
			thought.MetadataPlan:   planID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := p.navi.Thoughts.Enqueue(check); err != nil {
		// A duplicate id (another sweep or a restart won the race) or a
		// rejected edge is not worth failing the run over: the plan simply
		// closes without a synthesized check.
		p.navi.log.Debug("plan close: verification not queued", "plan", planID, "err", err)
	}
}

// planID derives a stable id for a plan from its (sorted) sinks, so the
// same drained plan can never be closed twice.
func planID(sinks []string) string {
	h := fnv.New32a()
	for _, s := range sinks {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// planClosePrompt names the plan's stages with what each reported, then
// asks for a concrete check. "Verify the result" alone invites a thought
// that re-reads the transcript and agrees with itself, so the ask is
// specific about what a check is.
func planClosePrompt(plan []*thought.Thought) string {
	const (
		maxStages = 8
		maxPrompt = 100
		maxResult = 240
	)
	stages := append([]*thought.Thought(nil), plan...)
	sort.SliceStable(stages, func(i, j int) bool {
		return stages[i].CreatedAt.Before(stages[j].CreatedAt)
	})

	var b strings.Builder
	b.WriteString("Close out this plan of thoughts. Its stages, in order:\n\n")
	for i, m := range stages {
		if i == maxStages {
			b.WriteString("- …and more stages not listed\n")
			break
		}
		fmt.Fprintf(&b, "- %s: %s\n", m.ID, oneLine(m.Prompt, maxPrompt))
		if r := strings.TrimSpace(m.Result); r != "" {
			fmt.Fprintf(&b, "  result: %s\n", oneLine(r, maxResult))
		}
	}
	b.WriteString("\nVerify what the plan actually produced: run the result if it is runnable " +
		"(a build, tests, a read-back), otherwise inspect it directly. Report what you verified, " +
		"what you could not verify, and any discrepancy you found. Do not start new implementation " +
		"work — this thought exists to check the plan's result, not to extend it.")
	return b.String()
}

// oneLine collapses whitespace so one stage stays one line in the prompt,
// and truncates at a rune boundary.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
