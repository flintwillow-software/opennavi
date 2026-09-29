package cron

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/store/sqlite"
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

// testCronDB opens a temp SQLite DB with the schedule_state table migrated.
func testCronDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := sqlite.Open(t.TempDir()+"/cron.db", sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := migrateState(st.DB()); err != nil {
		t.Fatal(err)
	}
	return st.DB()
}

// noopRunner completes any claimed thought immediately.
type noopRunner struct{}

func (noopRunner) Run(context.Context, *thought.Thought) error { return nil }

// TestCronCoalescesMissedTick is the NAVI-358 gate: a schedule whose
// next-due passed while the navi was down enqueues one coalescing thought
// at Start instead of being skipped silently.
func TestCronCoalescesMissedTick(t *testing.T) {
	db := testCronDB(t)
	// Seed a last-fired far enough in the past that the next minute is
	// already overdue.
	if err := newStateStore(db).recordFire("every-minute", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	q := newQueue(t)
	c := New(
		Config{Schedules: []Schedule{{Name: "every-minute", Expr: "* * * * *", Prompt: "tick"}}},
		WithStateStore(db),
	)
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: q}); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	// missedTick runs synchronously before the scheduler starts.
	found := false
	for _, th := range q.GetThoughts() {
		if th.Metadata["schedule"] == "every-minute" && th.Metadata["missed"] == "true" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missed tick not coalesced into a thought: %+v", q.GetThoughts())
	}
}

// TestCronRecordsFireStateOnClaim wires recordFire to the queue's started
// event: a schedule thought reaching a worker advances last_fired.
func TestCronRecordsFireStateOnClaim(t *testing.T) {
	db := testCronDB(t)
	q := newQueue(t)
	// A schedule that will not fire during the test (Jan 1 midnight).
	c := New(
		Config{Schedules: []Schedule{{Name: "s", Expr: "0 0 1 1 *", Prompt: "tick"}}},
		WithStateStore(db),
	)
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: q}); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	pool := thought.NewPool(q, noopRunner{}, 1)
	pool.Start()
	defer pool.StopAndWait()

	now := time.Now().UTC()
	if err := q.Enqueue(&thought.Thought{
		ID:        "t1",
		Prompt:    "do work",
		Priority:  4,
		Status:    thought.ThoughtPending,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  map[string]string{"schedule": "s"},
	}); err != nil {
		t.Fatal(err)
	}

	state := newStateStore(db)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if last, err := state.lastFired("s"); err == nil && !last.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fire state never recorded for the claimed schedule thought")
}
