package cron

import (
	"context"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

func newQueue(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent:  4,
		SlotExhaustion: thought.PolicyQueue,
	}, thought.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// TestStartFiresSchedules is the connector gate: a declared schedule
// reaches the navi's thought system as a thought.
func TestStartFiresSchedules(t *testing.T) {
	q := newQueue(t)
	c := New(Config{Schedules: []Schedule{
		{Name: "every-minute", Expr: "* * * * *", Prompt: "tick"},
	}})
	// Pin the clock just before the next minute so the first tick is
	// imminent instead of up to a minute away.
	c.now = func() time.Time {
		return time.Date(2030, 1, 1, 12, 0, 59, 900_000_000, time.UTC)
	}

	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: q}); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if thoughts := q.GetThoughts(); len(thoughts) > 0 {
			if thoughts[0].Prompt != "tick" {
				t.Fatalf("prompt = %q, want tick", thoughts[0].Prompt)
			}
			if thoughts[0].Metadata["schedule"] != "every-minute" {
				t.Fatalf("schedule metadata = %v", thoughts[0].Metadata)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("schedule never fired")
}

func TestStartRejectsInvalidSchedule(t *testing.T) {
	c := New(Config{Schedules: []Schedule{
		{Name: "bad", Expr: "not a cron", Prompt: "x"},
	}})
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: newQueue(t)}); err == nil {
		t.Fatal("an invalid cron expression must fail Start")
	}
}

func TestStartRequiresThoughtSystem(t *testing.T) {
	c := New(Config{})
	if err := c.Start(context.Background(), connector.Deps{}); err == nil {
		t.Fatal("Start without a thought system must fail")
	}
}

func TestStopIsIdempotent(t *testing.T) {
	c := New(Config{Schedules: []Schedule{{Name: "s", Expr: "* * * * *", Prompt: "x"}}})
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: newQueue(t)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
