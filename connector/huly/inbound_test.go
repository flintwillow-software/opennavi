package huly

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the repo's pure-Go SQLite driver, for the persisted seen-set test

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

// fakeTS is a full thought.ThoughtSystem that records what is enqueued. It is
// mutex-guarded so a test can drive the poll loop directly or in a goroutine.
type fakeTS struct {
	mu     sync.Mutex
	queued []*thought.Thought
}

func (f *fakeTS) Enqueue(t *thought.Thought) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, t)
	return nil
}
func (f *fakeTS) RouteMessage(id, text string) error     { return nil }
func (f *fakeTS) Pause(id string) error                  { return nil }
func (f *fakeTS) Resume(id string) error                 { return nil }
func (f *fakeTS) Cancel(id string) error                 { return nil }
func (f *fakeTS) BumpPriority(id string, prio int) error { return nil }
func (f *fakeTS) Retry(id string) error                  { return nil }
func (f *fakeTS) Complete(id, result string) error       { return nil }
func (f *fakeTS) Fail(id, detail string) error           { return nil }
func (f *fakeTS) GetThought(id string) (*thought.Thought, error) {
	return nil, thought.ErrThoughtNotFound
}
func (f *fakeTS) GetThoughts() []*thought.Thought {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*thought.Thought(nil), f.queued...)
}
func (f *fakeTS) OnEvent(fn func(thought.Event)) {}

func (f *fakeTS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queued)
}

// inboundClient connects a connector to the fake without launching the poll
// loop, so a test can drive pollOnce synchronously and deterministically.
func inboundClient(t *testing.T, f *fakeHuly, cfg Config) *Connector {
	t.Helper()
	cfg.URL = f.srv.URL
	cfg.Workspace = "flintwillowsoftware"
	if cfg.Credential == "" {
		cfg.Credential = "HULY_TOKEN"
	}
	if cfg.Env == nil {
		cfg.Env = func(string) string { return "account-token" }
	}
	c := New(cfg)
	client := newClient(c.cfg)
	if err := client.connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	c.live.Set(client)
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func pollOnceNow(t *testing.T, c *Connector, ts *fakeTS, seen *seenStore, since *time.Time) {
	t.Helper()
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: ts}, seen, since)
}

// TestInboundAssignedIssueEnqueuesThought is NAVI-561's critical path: a human
// assigns an issue to this navi's Huly user and the navi picks it up.
func TestInboundAssignedIssueEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen, err := newSeenStore(nil)
	if err != nil {
		t.Fatalf("newSeenStore: %v", err)
	}
	since := time.Unix(0, 0)
	pollOnceNow(t, c, ts, seen, &since)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	got := ts.GetThoughts()[0]
	if want := "huly-assigned-issue_assigned-6acd168268c57585aac3fa3f"; got.ID != want {
		t.Errorf("thought id = %q, want %q", got.ID, want)
	}
	if !strings.Contains(got.Prompt, "assigned to you") || !strings.Contains(got.Prompt, "NAVI-408") {
		t.Errorf("prompt = %q, want the assignment and identifier", got.Prompt)
	}
	if got.Metadata["event"] != string(EventIssueAssigned) {
		t.Errorf("event = %q, want issue_assigned", got.Metadata["event"])
	}
	if got.Metadata["identifier"] != "NAVI-408" {
		t.Errorf("identifier = %q, want NAVI-408", got.Metadata["identifier"])
	}
	if got.Metadata["principal"] != "connector:huly" {
		t.Errorf("principal = %q, want the connector's own", got.Metadata["principal"])
	}
}

// TestInboundDedupesAcrossPolls pins ADR 0023 decision 5: the same assigned
// issue is not a second thought on the next poll.
func TestInboundDedupesAcrossPolls(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen, _ := newSeenStore(nil)
	since := time.Unix(0, 0)
	pollOnceNow(t, c, ts, seen, &since)
	pollOnceNow(t, c, ts, seen, &since)
	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts across two polls, want 1", ts.count())
	}
}

// TestInboundStatusChangeEnqueuesThought pins the status-change kind: the first
// sighting is an assignment, a later move is a status change.
func TestInboundStatusChangeEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "s", Project: "NAVI",
		Events: []EventKind{EventIssueAssigned, EventIssueStatusChanged},
	}}})
	seen, _ := newSeenStore(nil)
	since := time.Unix(0, 0)
	pollOnceNow(t, c, ts, seen, &since) // assignment baseline

	f.mu.Lock()
	f.issues[0]["status"] = "tracker:status:Done"
	f.mu.Unlock()
	pollOnceNow(t, c, ts, seen, &since)

	if ts.count() != 2 {
		t.Fatalf("enqueued %d thoughts, want 2 (assigned + status change)", ts.count())
	}
	got := ts.GetThoughts()[1]
	if got.Metadata["event"] != string(EventIssueStatusChanged) {
		t.Errorf("event = %q, want issue_status_changed", got.Metadata["event"])
	}
	if !strings.Contains(got.Prompt, "tracker:status:Done") {
		t.Errorf("prompt = %q, want the new status", got.Prompt)
	}
}

// TestInboundMentionEnqueuesThought pins the mention kind: a comment whose
// markup names the navi's person id is a mention.
func TestInboundMentionEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{{
		"_id": "cm-1", "attachedTo": "6acd168268c57585aac3fa3f", "createdBy": "social-9",
		"createdOn": int64(5000),
		"message": `{"type":"doc","content":[{"type":"paragraph","content":[` +
			`{"type":"text","text":"hey "},` +
			`{"type":"mention","attrs":{"id":"6aa2a3038376b1c099c0403d"}}]}]}`,
	}}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	seen, _ := newSeenStore(nil)
	since := time.Unix(0, 0)
	pollOnceNow(t, c, ts, seen, &since)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	got := ts.GetThoughts()[0]
	if got.Metadata["event"] != string(EventIssueMentioned) {
		t.Errorf("event = %q, want issue_mentioned", got.Metadata["event"])
	}
	if !strings.Contains(got.Context, "hey") {
		t.Errorf("context = %q, want the comment text", got.Context)
	}
}

// TestInboundCommentOnMyIssueEnqueuesThought pins the comment kind: a comment
// on an issue assigned to the navi is work, even without a mention.
func TestInboundCommentOnMyIssueEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{{
		"_id": "cm-2", "attachedTo": "6acd168268c57585aac3fa3f", "createdBy": "social-9",
		"createdOn": int64(6000),
		"message":   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"any update?"}]}]}`,
	}}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "c", Project: "NAVI", Events: []EventKind{EventCommentOnMyIssue},
	}}})
	seen, _ := newSeenStore(nil)
	since := time.Unix(0, 0)
	pollOnceNow(t, c, ts, seen, &since)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	if got := ts.GetThoughts()[0]; got.Metadata["event"] != string(EventCommentOnMyIssue) {
		t.Errorf("event = %q, want comment_on_my_issue", got.Metadata["event"])
	}
}

// TestInboundSeenSetPersistsAcrossRestart pins the persisted half of ADR 0023
// decision 5: a new seen-store over the same database suppresses the event.
func TestInboundSeenSetPersistsAcrossRestart(t *testing.T) {
	f := newFakeHuly(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	since := time.Unix(0, 0)
	seen1, _ := newSeenStore(db)
	pollOnceNow(t, c, ts, seen1, &since)
	if ts.count() != 1 {
		t.Fatalf("first poll enqueued %d, want 1", ts.count())
	}

	// A fresh store over the same database stands in for a restart.
	seen2, _ := newSeenStore(db)
	pollOnceNow(t, c, ts, seen2, &since)
	if ts.count() != 1 {
		t.Fatalf("after a restart the same event enqueued a second thought (%d total)", ts.count())
	}
}

// TestInboundRejectsUnservedSubscriptions pins composition-time validation: a
// declaration the binding cannot serve fails Start rather than dropping events
// silently.
func TestInboundRejectsUnservedSubscriptions(t *testing.T) {
	f := newFakeHuly(t)
	base := func() Config {
		return Config{
			URL: f.srv.URL, Workspace: "flintwillowsoftware", Credential: "HULY_TOKEN",
			Env: func(string) string { return "account-token" },
		}
	}
	cases := []struct {
		name string
		subs []Subscription
		want string
	}{
		{"unknown event", []Subscription{{Name: "x", Events: []EventKind{"nope"}}}, "unknown event kind"},
		{"unserved intent", []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}, Intent: IntentAttach}}, "not served"},
		{"no name", []Subscription{{Events: []EventKind{EventIssueAssigned}}}, "requires a name"},
		{"duplicate", []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}}, {Name: "x", Events: []EventKind{EventIssueAssigned}}}, "duplicate subscription"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Subscriptions = tc.subs
			c := New(cfg)
			err := c.Start(context.Background(), connector.Deps{ThoughtSystem: &fakeTS{}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}

	// Inbound with no thought system is refused: there is nowhere to put work.
	cfg := base()
	cfg.Subscriptions = []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}}}
	if err := New(cfg).Start(context.Background(), connector.Deps{}); err == nil {
		t.Fatal("inbound without a thought system must fail")
	}
}
