package connector

import "testing"

// TestResolvePriorityUnsetIsTheDefault is the regression that motivated the
// pointer: on the thought queue's scale urgent is 0, which is also an int's zero
// value, so an `int` field could not express "unset" without every undeclared
// hint landing in the urgent band — where it cancels whatever is running.
func TestResolvePriorityUnsetIsTheDefault(t *testing.T) {
	if got := ResolvePriority(nil); got != DefaultPriority {
		t.Fatalf("unset hint resolved to %d, want the default %d", got, DefaultPriority)
	}
	if DefaultPriority == 0 {
		t.Fatal("the default must not be the urgent band")
	}
}

// TestResolvePriorityDeliberateUrgentSurvives: a declared 0 is the urgent band
// and must reach the queue as 0, not be turned into the default.
func TestResolvePriorityDeliberateUrgentSurvives(t *testing.T) {
	if got := ResolvePriority(PriorityValue(0)); got != 0 {
		t.Fatalf("a deliberate urgent hint resolved to %d, want 0", got)
	}
}

func TestResolvePriorityUsesTheDeclaredValueVerbatim(t *testing.T) {
	for _, p := range []int{0, 1, 2, 3, 5, 10} {
		if got := ResolvePriority(PriorityValue(p)); got != p {
			t.Errorf("declared %d resolved to %d", p, got)
		}
	}
}

// TestEventPriorityMapsTheServicesScale pins the mapping: the services' priority
// names are not the thought queue's scale, so an explicit mapping is the whole
// point (a numeric pass-through would silently mean something else).
func TestEventPriorityMapsTheServicesScale(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"urgent", 0, true},
		{"high", 1, true},
		{"medium", 2, true},
		{"low", 3, true},
		{"no-priority", 0, false}, // no opinion: the caller falls back
		{"none", 0, false},
		{"", 0, false},
		{"nonsense", 0, false},
	}
	for _, c := range cases {
		got := EventPriority(c.name)
		if !c.ok {
			if got != nil {
				t.Errorf("EventPriority(%q) = %d, want nil (no opinion)", c.name, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("EventPriority(%q) = nil, want %d", c.name, c.want)
			continue
		}
		if *got != c.want {
			t.Errorf("EventPriority(%q) = %d, want %d", c.name, *got, c.want)
		}
	}
}

// TestHintForEventPriorityPrefersTheEvent: an event's own priority is the more
// specific signal (a subscription covers a whole project; an urgent issue is
// urgent on its own terms), and the subscription's hint is the fallback for an
// event that carries none.
func TestHintForEventPriorityPrefersTheEvent(t *testing.T) {
	subHint := PriorityValue(3) // "planned work" declared on the subscription

	// The event states urgent: it wins over the subscription's 3.
	if got := HintForEventPriority("urgent", subHint); got != 0 {
		t.Errorf("urgent event with a sub hint of 3 resolved to %d, want 0 (the event wins)", got)
	}
	// The event states nothing: the subscription's hint applies.
	if got := HintForEventPriority("no-priority", subHint); got != 3 {
		t.Errorf("unprioritised event resolved to %d, want the subscription's 3", got)
	}
	// Neither: the default.
	if got := HintForEventPriority("", nil); got != DefaultPriority {
		t.Errorf("no signal at all resolved to %d, want the default %d", got, DefaultPriority)
	}
	// An urgent event with no subscription hint still interrupts.
	if got := HintForEventPriority("urgent", nil); got != 0 {
		t.Errorf("urgent event with no sub hint resolved to %d, want 0", got)
	}
}

func TestValidPriority(t *testing.T) {
	for _, p := range []int{0, 1, 5, 10} {
		if !ValidPriority(p) {
			t.Errorf("ValidPriority(%d) = false, want true", p)
		}
	}
	for _, p := range []int{-1, 11, 100} {
		if ValidPriority(p) {
			t.Errorf("ValidPriority(%d) = true, want false", p)
		}
	}
}
