package thought

import (
	"slices"
	"testing"
)

// think builds a pending thought with dependencies for the graph tests.
func think(id string, status ThoughtStatus, deps ...string) *Thought {
	return &Thought{ID: id, Status: status, DependsOn: deps}
}

// TestPlanIsTheDependencyComponent: a plan is the connected graph around
// a thought — its dependencies, its dependents, and their transitive
// edges — and nothing unrelated.
func TestPlanIsTheDependencyComponent(t *testing.T) {
	t1 := think("t1", ThoughtComplete)
	t2 := think("t2", ThoughtComplete, "t1")
	t3 := think("t3", ThoughtRunning, "t2")
	side := think("side", ThoughtPending, "t2")
	unrelated := think("other", ThoughtPending)
	all := []*Thought{t1, t2, t3, side, unrelated}

	got := idsOf(Plan(t3, all))
	if want := []string{"t3", "t2", "t1", "side"}; !sameSet(got, want) {
		t.Fatalf("Plan(t3) = %v, want the component %v (and not %v)", got, want, unrelated.ID)
	}

	// Entering from any member yields the same component.
	fromRoot := idsOf(Plan(t1, all))
	if !sameSet(fromRoot, got) {
		t.Fatalf("Plan(t1) = %v, want the same component as Plan(t3) = %v", fromRoot, got)
	}

	// A standalone thought is its own one-member plan, and an unknown id
	// cannot join one.
	if n := len(Plan(unrelated, all)); n != 1 {
		t.Fatalf("standalone plan has %d members, want 1", n)
	}
	ghost := think("ghost", ThoughtPending, "nowhere")
	if got := idsOf(Plan(ghost, all)); len(got) != 1 || got[0] != "ghost" {
		t.Fatalf("Plan(unknown dependency) = %v, want just the ghost", got)
	}
}

// TestPlanSinksAreWhereOutputLands: a sink is a member nothing else in the
// plan depends on — the stage(s) a closing check must wait for.
func TestPlanSinksAreWhereOutputLands(t *testing.T) {
	// A chain has one sink (the last stage).
	chain := []*Thought{
		think("a", ThoughtComplete),
		think("b", ThoughtComplete, "a"),
		think("c", ThoughtComplete, "b"),
	}
	if got := PlanSinks(chain); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("chain sinks = %v, want [c]", got)
	}

	// A fork has two, sorted for determinism: a → b, a → c.
	fork := []*Thought{
		think("a", ThoughtComplete),
		think("c", ThoughtComplete, "a"),
		think("b", ThoughtComplete, "a"),
	}
	if got := PlanSinks(fork); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("fork sinks = %v, want [b c]", got)
	}

	// A join (b and c both feed d) has one: d.
	join := []*Thought{
		think("a", ThoughtComplete),
		think("b", ThoughtComplete, "a"),
		think("c", ThoughtComplete, "a"),
		think("d", ThoughtComplete, "b", "c"),
	}
	if got := PlanSinks(join); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("join sinks = %v, want [d]", got)
	}
}

// TestPlanSucceeded: only an all-complete plan is worth closing.
func TestPlanSucceeded(t *testing.T) {
	done := []*Thought{think("a", ThoughtComplete), think("b", ThoughtComplete, "a")}
	if !PlanSucceeded(done) {
		t.Fatal("an all-complete plan must report success")
	}
	for _, bad := range []ThoughtStatus{ThoughtPending, ThoughtRunning, ThoughtPaused, ThoughtFailed, ThoughtCancelled} {
		members := []*Thought{think("a", ThoughtComplete), think("b", bad, "a")}
		if PlanSucceeded(members) {
			t.Fatalf("plan with a %s member reported success", bad)
		}
	}
	if PlanSucceeded(nil) {
		t.Fatal("an empty plan is not a succeeded plan")
	}
}

func idsOf(ts []*Thought) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
