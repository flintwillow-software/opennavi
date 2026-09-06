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

	defs := config.DefinitionCronConfig{
		Schedules: []config.CronScheduleDef{{
			Name:   "test-cron",
			Expr:   "* * * * *",
			Prompt: "cron test prompt",
		}},
	}
	sched, err := WireCron(rt.Store.DB(), defs, rt.Queue)
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
