package daemon

import (
	"testing"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

func TestWireCronRecordFire(t *testing.T) {
	// Regression: the WireCron RecordFire handler re-enters the queue
	// via GetThought on EventStarted. Previously emit ran under q.mu,
	// deadlocking permanently on the first cron claim (review round 3
	// blocker on PR #243).
	rt, err := Assemble(testParams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	defs := config.CronConfig{
		Schedules: []config.CronSchedule{{
			Name:   "test-cron",
			Expr:   "* * * * *",
			Prompt: "cron test prompt",
		}},
	}
	sched, err := WireCron(rt.Store.DB(), &defs, rt.Queue)
	if err != nil {
		t.Fatal(err)
	}

	// Enqueue a cron-tagged thought and claim it (simulates the pool).
	tt := &thought.Thought{
		ID: "cron-thought-1", Prompt: "cron prompt",
		Status:   thought.ThoughtPending,
		Metadata: map[string]string{"schedule": "test-cron"},
	}
	if err := rt.Queue.Enqueue(tt); err != nil {
		t.Fatal(err)
	}
	claimed, ok := rt.Queue.Dequeue()
	if !ok {
		t.Fatal("claim failed")
	}
	if claimed.Metadata["schedule"] != "test-cron" {
		t.Fatalf("metadata schedule = %q", claimed.Metadata["schedule"])
	}
	_ = sched
}

func TestWireCronRecordFireLastFiredAssert(t *testing.T) {
	// Strengthened: asserts LastFired actually lands in schedule_state
	// (not just that the enqueue succeeded).
	rt, err := Assemble(testParams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Store.Close()

	defs := config.CronConfig{
		Schedules: []config.CronSchedule{{
			Name:   "lf-cron",
			Expr:   "* * * * *",
			Prompt: "prompt",
		}},
	}
	sched, err := WireCron(rt.Store.DB(), &defs, rt.Queue)
	if err != nil {
		t.Fatal(err)
	}

	tt := &thought.Thought{
		ID: "lf-1", Prompt: "p", Status: thought.ThoughtPending,
		Metadata: map[string]string{"schedule": "lf-cron"},
	}
	if err := rt.Queue.Enqueue(tt); err != nil {
		t.Fatal(err)
	}
	_, ok := rt.Queue.Dequeue()
	if !ok {
		t.Fatal("claim failed")
	}
	_ = sched

	// LastFired must land (the WireCron handler records on EventStarted).
	got, gerr := rt.Store.DB().Query(
		`SELECT last_fired FROM schedule_state WHERE schedule_name = ?`, "lf-cron")
	if gerr != nil {
		t.Fatal(gerr)
	}
	defer got.Close()
	hasRow := got.Next()
	if !hasRow {
		t.Fatal("schedule_state.last_fired row missing for lf-cron")
	}
}
