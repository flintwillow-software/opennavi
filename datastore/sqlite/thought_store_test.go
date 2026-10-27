package sqlite

import (
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

func openThoughtStore(t *testing.T) (*Store, *ThoughtStore) {
	t.Helper()
	st, err := Open(t.TempDir()+"/thoughts.db", Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts, err := NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return st, ts
}

func TestThoughtStoreRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{
		ID:        "t-1",
		Prompt:    "deploy the thing",
		Context:   "some context",
		Metadata:  map[string]string{"source": "cron"},
		Priority:  2,
		Status:    thought.ThoughtPending,
		LogPath:   "/tmp/x",
		Skills:    []string{"deploy_runbook"},
		Assembled: "assembled context block",
		CreatedAt: testNow(),
		UpdatedAt: testNow(),
	}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Prompt != want.Prompt || got.Context != want.Context ||
		got.Priority != want.Priority || got.Status != want.Status || got.LogPath != want.LogPath {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Metadata["source"] != "cron" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
	if got.Assembled != want.Assembled {
		t.Fatalf("assembled = %q, want %q", got.Assembled, want.Assembled)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "deploy_runbook" {
		t.Fatalf("skills = %v", got.Skills)
	}
}

func TestThoughtStoreCheckpointRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{ID: "t-2", Prompt: "p", Status: thought.ThoughtPaused, Checkpoint: []byte(`{"msgs":3}`)}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-2")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Checkpoint) != `{"msgs":3}` {
		t.Fatalf("checkpoint = %s", got.Checkpoint)
	}
}

func TestThoughtStoreLoadMissing(t *testing.T) {
	_, ts := openThoughtStore(t)
	if _, err := ts.LoadThought("nope"); err != thought.ErrThoughtNotFound {
		t.Fatalf("error = %v", err)
	}
}

func TestThoughtStoreDelete(t *testing.T) {
	_, ts := openThoughtStore(t)
	tt := &thought.Thought{ID: "t-3", Prompt: "p"}
	if err := ts.SaveThought(tt); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteThought("t-3"); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteThought("t-3"); err != thought.ErrThoughtNotFound {
		t.Fatalf("double delete error = %v", err)
	}
}

func TestThoughtStoreQueueCrashRecovery(t *testing.T) {
	// End-to-end: persist running + pending thoughts through the store,
	// reopen the queue, verify running→pending recovery.
	path := t.TempDir() + "/q.db"
	st, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	running := &thought.Thought{ID: "was-running", Prompt: "r", Priority: 4, Status: thought.ThoughtRunning, SlotID: "slot-1", WorkerID: "worker-1"}
	pending := &thought.Thought{ID: "was-pending", Prompt: "p", Priority: 2, Status: thought.ThoughtPending}
	if err := ts.SaveThought(running); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveThought(pending); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Reopen: fresh queue over the same DB recovers.
	st2, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ts2, err := NewThoughtStore(st2.DB())
	if err != nil {
		t.Fatal(err)
	}
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, ts2)
	if err != nil {
		t.Fatal(err)
	}

	recovered, _ := q.GetThought("was-running")
	if recovered == nil {
		t.Fatal("running thought lost in recovery")
	}
	if recovered.Status != thought.ThoughtPending {
		t.Fatalf("recovered status = %q, want pending", recovered.Status)
	}
	if recovered.SlotID != "" || recovered.WorkerID != "" {
		t.Fatalf("recovered slot/worker not cleared: %+v", recovered)
	}
	// Priority order: was-pending (2) before was-running (4).
	first, ok := q.Dequeue()
	if !ok || first.ID != "was-pending" {
		t.Fatalf("first claim = %v, want was-pending", first)
	}
	second, ok := q.Dequeue()
	if !ok || second.ID != "was-running" {
		t.Fatalf("second claim = %v, want was-running", second)
	}
}

func TestThoughtStoreDeferUntilRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	due := testNow().Add(1 * time.Hour)
	want := &thought.Thought{ID: "t-defer", Prompt: "later", Status: thought.ThoughtPending, DeferUntil: due}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-defer")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeferUntil.Compare(due) != 0 {
		t.Fatalf("defer_until = %v, want %v", got.DeferUntil, due)
	}
	// Round-trips through the full scan (LoadThoughts) too.
	all, err := ts.LoadThoughts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].DeferUntil.Compare(due) != 0 {
		t.Fatalf("LoadThoughts defer_until: %+v", all)
	}
	// A thought without a deferral stays empty (zero time).
	plain := &thought.Thought{ID: "t-plain", Prompt: "now"}
	if err := ts.SaveThought(plain); err != nil {
		t.Fatal(err)
	}
	plainLoaded, err := ts.LoadThought("t-plain")
	if err != nil {
		t.Fatal(err)
	}
	if !plainLoaded.DeferUntil.IsZero() {
		t.Fatal("unset defer_until became non-zero")
	}
}

func TestDeferralSurvivesRestartViaSqlite(t *testing.T) {
	// End-to-end durability: a deferred thought enqueued through the queue
	// persists, survives a fresh queue over the same database, and is
	// honoured — skipped while not yet due, claimed once it is.
	//
	// The two halves use deferrals that cannot be raced: a far-future one for
	// "not claimable", an already-past one for "claimable". A short
	// near-future window plus a sleep (this test used now+400ms and 600ms)
	// races the store close and reopen under -race on a loaded runner, and
	// failed intermittently in CI.
	t.Run("not yet due is skipped across the restart", func(t *testing.T) {
		q := restartedQueue(t, "later work", testNow().Add(time.Hour))
		if tk, ok := q.Dequeue(); ok {
			t.Fatalf("deferred thought claimable right after restart: %v", tk)
		}
	})
	t.Run("due is claimable across the restart", func(t *testing.T) {
		q := restartedQueue(t, "due work", testNow().Add(-time.Second))
		tk, ok := q.Dequeue()
		if !ok || tk.Prompt != "due work" {
			t.Fatalf("thought not claimable after its not-before time passed: %v %v", tk, ok)
		}
	})
}

// restartedQueue enqueues one thought deferred until due, closes the store, and
// returns a fresh queue over the same database — the restart boundary these
// tests exist to cross.
func restartedQueue(t *testing.T, prompt string, due time.Time) *thought.Queue {
	t.Helper()
	path := t.TempDir() + "/defer.db"
	st, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := NewThoughtStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}

	q, err := thought.NewQueue(thought.Config{
		MaxConcurrent:  2,
		PrioritySlots:  0,
		SlotExhaustion: thought.PolicyQueue,
		// Legacy sync (NAVI-338): this test's premise is synchronous
		// persistence across the restart boundary (st.Close() follows
		// Enqueue immediately — the dirty-set flusher would race it).
		FlushInterval: -1,
	}, ts)
	if err != nil {
		t.Fatal(err)
	}
	deferred := &thought.Thought{
		ID: "deferred", Prompt: prompt, Priority: 2,
		Status: thought.ThoughtPending, DeferUntil: due,
	}
	if err := q.Enqueue(deferred); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: a fresh queue over the same database recovers the deferral.
	st2, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	ts2, err := NewThoughtStore(st2.DB())
	if err != nil {
		t.Fatal(err)
	}
	q2, err := thought.NewQueue(thought.Config{MaxConcurrent: 2, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, ts2)
	if err != nil {
		t.Fatal(err)
	}
	return q2
}
func testNow() time.Time { return time.Now().UTC() }

// TestThoughtStoreModeRoundTrip covers the per-thought mode (ADR 0007):
// a durable thought's capability profile survives a restart.
func TestThoughtStoreModeRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{ID: "t-mode", Prompt: "p", Status: thought.ThoughtPending, Mode: "plan"}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-mode")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "plan" {
		t.Fatalf("mode round trip = %q, want plan", got.Mode)
	}

	plain := &thought.Thought{ID: "t-no-mode", Prompt: "p"}
	if err := ts.SaveThought(plain); err != nil {
		t.Fatal(err)
	}
	p, err := ts.LoadThought("t-no-mode")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "" {
		t.Fatalf("unset mode became %q", p.Mode)
	}
}

func TestThoughtStoreProviderModelRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{ID: "t-prov", Prompt: "p", Status: thought.ThoughtPending,
		Provider: "mock", Model: "fast-1"}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-prov")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "mock" || got.Model != "fast-1" {
		t.Fatalf("provider/model round trip: %q/%q", got.Provider, got.Model)
	}
	// Unset stays empty.
	plain := &thought.Thought{ID: "t-plain", Prompt: "p"}
	if err := ts.SaveThought(plain); err != nil {
		t.Fatal(err)
	}
	p, err := ts.LoadThought("t-plain")
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "" || p.Model != "" {
		t.Fatalf("unset provider/model became non-empty")
	}
}

// TestThoughtStoreResultRoundTrip covers the completion message: it is
// the only place a thought's outcome lives apart from its session
// transcript, so a persistence regression here silently loses the answer.
func TestThoughtStoreResultRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)

	want := &thought.Thought{ID: "t-result", Prompt: "p", Status: thought.ThoughtComplete,
		Result: "pong, with punctuation: \"quoted\" and a newline\n"}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-result")
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != want.Result {
		t.Fatalf("result round trip:\n got %q\nwant %q", got.Result, want.Result)
	}

	// A thought that has not completed carries no result, and an empty
	// result must stay empty rather than becoming NULL-ish text.
	pending := &thought.Thought{ID: "t-pending", Prompt: "p", Status: thought.ThoughtPending}
	if err := ts.SaveThought(pending); err != nil {
		t.Fatal(err)
	}
	p, err := ts.LoadThought("t-pending")
	if err != nil {
		t.Fatal(err)
	}
	if p.Result != "" {
		t.Fatalf("unset result became %q", p.Result)
	}
}

// TestThoughtStoreDependsOnRoundTrip covers migration 15 (NAVI-378): the
// dependency graph survives persistence, populated and empty, on both the
// single-row and bulk load paths.
func TestThoughtStoreDependsOnRoundTrip(t *testing.T) {
	st, ts := openThoughtStore(t)

	columns, err := tableColumns(st, "thoughts")
	if err != nil {
		t.Fatal(err)
	}
	if !columns["depends_on"] {
		t.Fatalf("migration 15 did not add thoughts.depends_on: %v", columns)
	}

	want := &thought.Thought{
		ID:        "t-dep",
		Prompt:    "stage two",
		Status:    thought.ThoughtPending,
		DependsOn: []string{"t-dep-1", "t-dep-2"},
	}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-dep")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 2 || got.DependsOn[0] != "t-dep-1" || got.DependsOn[1] != "t-dep-2" {
		t.Fatalf("depends_on = %v, want [t-dep-1 t-dep-2]", got.DependsOn)
	}

	// A thought with no dependencies stays empty rather than becoming a
	// JSON fragment.
	if err := ts.SaveThought(&thought.Thought{ID: "t-nodep", Prompt: "alone", Status: thought.ThoughtPending}); err != nil {
		t.Fatal(err)
	}
	none, err := ts.LoadThought("t-nodep")
	if err != nil {
		t.Fatal(err)
	}
	if len(none.DependsOn) != 0 {
		t.Fatalf("empty depends_on = %v", none.DependsOn)
	}

	all, err := ts.LoadThoughts()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		if x.ID == "t-dep" {
			if len(x.DependsOn) != 2 {
				t.Fatalf("LoadThoughts depends_on = %v", x.DependsOn)
			}
			return
		}
	}
	t.Fatal("t-dep missing from LoadThoughts")
}

// TestThoughtStorePreemptionFieldsRoundTrip: the preemption and handoff
// record (NAVI-575/NAVI-581, ADR 0035 decisions 3 and 7) survives a restart,
// so a preemption is not lost across a crash — the release edge must be there
// for the resumed process to unwind it.
func TestThoughtStorePreemptionFieldsRoundTrip(t *testing.T) {
	_, ts := openThoughtStore(t)
	want := &thought.Thought{
		ID:             "t-pre",
		Prompt:         "half-done migration",
		Status:         thought.ThoughtPaused,
		Priority:       5,
		ParkReason:     thought.ParkPreempted,
		PreemptedBy:    "t-urgent",
		HandoffNote:    "interrupted; the migration is half-applied",
		SteerRequested: true,
		CreatedAt:      testNow(),
		UpdatedAt:      testNow(),
	}
	if err := ts.SaveThought(want); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadThought("t-pre")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParkReason != thought.ParkPreempted {
		t.Errorf("park_reason = %q, want %q", got.ParkReason, thought.ParkPreempted)
	}
	if got.PreemptedBy != "t-urgent" {
		t.Errorf("preempted_by = %q, want t-urgent", got.PreemptedBy)
	}
	if got.HandoffNote != "interrupted; the migration is half-applied" {
		t.Errorf("handoff_note = %q", got.HandoffNote)
	}
	if !got.SteerRequested {
		t.Error("steer_requested did not survive the round trip")
	}

	// A thought with no preemption record stays clean rather than picking up
	// a zero-value reason.
	if err := ts.SaveThought(&thought.Thought{ID: "t-clean", Prompt: "x", Status: thought.ThoughtPending, CreatedAt: testNow(), UpdatedAt: testNow()}); err != nil {
		t.Fatal(err)
	}
	clean, err := ts.LoadThought("t-clean")
	if err != nil {
		t.Fatal(err)
	}
	if clean.ParkReason != "" || clean.PreemptedBy != "" || clean.HandoffNote != "" || clean.SteerRequested {
		t.Fatalf("clean thought picked up preemption state: %+v", clean)
	}
}

// TestLoadThoughtsOrdersByArrivalNotActivity pins the ADR 0035 decision 4
// query change: LoadThoughts orders by (priority, created_at), so a restart
// resumes from the TOP of the queue — oldest-first within a band — rather than
// from whatever was last touched. Ordering by updated_at (the previous rule)
// would promote the recently-touched thought.
func TestLoadThoughtsOrdersByArrivalNotActivity(t *testing.T) {
	_, ts := openThoughtStore(t)
	base := time.Now().UTC()

	// Same priority, and the two signals DISAGREE: "older" arrived first but
	// was touched most recently (a status change / retry / metadata write),
	// while "younger" arrived second and was never touched again. Arrival
	// order is [older, younger]; last-activity order is [younger, older], so
	// this test fails if the query reverts to updated_at.
	older := &thought.Thought{
		ID: "older", Prompt: "first", Priority: 4, Status: thought.ThoughtPending,
		CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour),
	}
	younger := &thought.Thought{
		ID: "younger", Prompt: "second", Priority: 4, Status: thought.ThoughtPending,
		CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute),
	}
	for _, th := range []*thought.Thought{younger, older} {
		if err := ts.SaveThought(th); err != nil {
			t.Fatal(err)
		}
	}

	all, err := ts.LoadThoughts()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, x := range all {
		if x.ID == "older" || x.ID == "younger" {
			order = append(order, x.ID)
		}
	}
	if len(order) != 2 || order[0] != "older" || order[1] != "younger" {
		t.Fatalf("LoadThoughts order = %v, want [older younger] (arrival, not last activity)", order)
	}
}

// TestLoadThoughtsOrdersMostUrgentFirst pins the primary key across bands.
func TestLoadThoughtsOrdersMostUrgentFirst(t *testing.T) {
	_, ts := openThoughtStore(t)
	base := time.Now().UTC()
	urgentYoung := &thought.Thought{ID: "urgent", Prompt: "u", Priority: 1, Status: thought.ThoughtPending, CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour)}
	plannedOld := &thought.Thought{ID: "planned", Prompt: "p", Priority: 8, Status: thought.ThoughtPending, CreatedAt: base, UpdatedAt: base}
	for _, th := range []*thought.Thought{urgentYoung, plannedOld} {
		if err := ts.SaveThought(th); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ts.LoadThoughts()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, x := range all {
		if x.ID == "urgent" || x.ID == "planned" {
			order = append(order, x.ID)
		}
	}
	if len(order) != 2 || order[0] != "urgent" {
		t.Fatalf("LoadThoughts order = %v, want the more urgent first", order)
	}
}
