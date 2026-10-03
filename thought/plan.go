package thought

import "sort"

// A plan of thoughts is a dependency-connected set of thoughts (ADR 0013
// decision 1: a task too large for one thought becomes a plan of
// thoughts). There is deliberately no plan object — the graph NAVI-378
// introduced *is* the plan, so these helpers read the shape out of a
// snapshot: what a plan is, where its output lands, and whether it
// finished (ADR 0016 closes it with a verification thought).

// Plan returns the dependency-connected component containing t: t, its
// transitive dependencies, and every thought transitively depending on
// any of them. Thoughts outside the component are not returned — an
// unrelated queued thought is not part of this task — and a standalone
// thought is its own one-member plan. Ids that the snapshot cannot
// resolve are skipped: an unknown dependency cannot join a plan here.
func Plan(t *Thought, all []*Thought) []*Thought {
	if t == nil {
		return nil
	}
	byID := make(map[string]*Thought, len(all))
	for _, o := range all {
		byID[o.ID] = o
	}
	start, ok := byID[t.ID]
	if !ok {
		start = t
		byID[t.ID] = t
	}
	dependents := make(map[string][]string, len(all))
	for _, o := range all {
		for _, dep := range o.DependsOn {
			dependents[dep] = append(dependents[dep], o.ID)
		}
	}

	seen := map[string]bool{start.ID: true}
	queue := []string{start.ID}
	out := []*Thought{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		next := append([]string(nil), dependents[id]...)
		if cur := byID[id]; cur != nil {
			next = append(next, cur.DependsOn...)
		}
		for _, nid := range next {
			if seen[nid] {
				continue
			}
			o, ok := byID[nid]
			if !ok {
				continue
			}
			seen[nid] = true
			out = append(out, o)
			queue = append(queue, nid)
		}
	}
	return out
}

// PlanSinks returns the ids of the plan members nothing else in the plan
// depends on — the stages where the plan's output lands. Sorted, so the
// result is deterministic. Empty when every member is depended on (which
// a graph with a cycle would look like).
func PlanSinks(members []*Thought) []string {
	inPlan := make(map[string]bool, len(members))
	for _, m := range members {
		inPlan[m.ID] = true
	}
	hasDependent := make(map[string]bool, len(members))
	for _, m := range members {
		for _, dep := range m.DependsOn {
			if inPlan[dep] {
				hasDependent[dep] = true
			}
		}
	}
	sinks := make([]string, 0, len(members))
	for _, m := range members {
		if !hasDependent[m.ID] {
			sinks = append(sinks, m.ID)
		}
	}
	sort.Strings(sinks)
	return sinks
}

// PlanSucceeded reports whether every member of the plan reached
// "complete". A plan with a failed, cancelled, paused, or still-running
// member has not succeeded — and only a succeeded plan is worth checking
// (ADR 0016): a dead dependency already fails its dependents, so a
// verification of an unfinished plan would verify nothing.
func PlanSucceeded(members []*Thought) bool {
	if len(members) == 0 {
		return false
	}
	for _, m := range members {
		if m.Status != ThoughtComplete {
			return false
		}
	}
	return true
}
